package dev.devicecontrol.app.net

import android.os.Build
import dev.devicecontrol.app.command.CommandDispatcher
import dev.devicecontrol.app.protocol.Auth
import dev.devicecontrol.app.protocol.Capabilities
import dev.devicecontrol.app.protocol.CallFrame
import dev.devicecontrol.app.protocol.CallResponseFrame
import dev.devicecontrol.app.protocol.EventFrame
import dev.devicecontrol.app.protocol.HeartbeatFrame
import dev.devicecontrol.app.protocol.PROTOCOL_VERSION
import dev.devicecontrol.app.protocol.RegisterFrame
import dev.devicecontrol.app.protocol.RegisteredFrame
import dev.devicecontrol.app.protocol.WireType
import dev.devicecontrol.app.storage.DeviceCredential
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import kotlin.math.min
import kotlin.random.Random

/**
 * The persistent WebSocket connection to the server, with the full §7 lifecycle:
 * full-jitter backoff, register-on-open, heartbeat, call dispatch, and close-code
 * classification.
 *
 * Threading model: OkHttp's [WebSocketListener] callbacks run on OkHttp's worker
 * thread. We bridge them onto a single [connectionScope] coroutine so the protocol
 * logic (register → wait → serve) is sequential and cancellable. Outbound sends use
 * [WebSocket.send], which is synchronous and safe to call from any thread.
 *
 * Reconnect rules (§7 + §13):
 *  - `4003` auth_failed → the token is dead. Wipe the credential, surface [Fatal] with
 *    [Fatal.needsRePair]=true, and STOP. Reconnecting would loop forever against a
 *    deleted/revoked record.
 *  - `4004` protocol_version_unsupported → the app is too old/new for this server.
 *    Surface [Fatal] with needsRePair=false (the credential is fine) and STOP.
 *  - `4002/4007/4008/4009/4010` and 1000-range closes → backoff and reconnect.
 *  - Attempt counter resets to 0 only after a connection stayed up longer than
 *    [BACKOFF_CAP_MS] (30s). Resetting on register-success would let a register-then-
 *    drop loop hammer the server.
 *  - On every reconnect, all in-flight state is discarded (§7): running calls are
 *    abandoned WITHOUT sending responses, the dispatcher's dedup set is cleared, and
 *    the heartbeat seq resets.
 *
 * Non-idempotent commands are never retried across a reconnect (§6) — we don't even
 * try; the server has already abandoned the request_ids.
 */
class WsClient(
    private val client: OkHttpClient,
    private val dispatcher: CommandDispatcher,
) {
    /** Called after the credential is wiped on 4003. */
    var onCredentialWiped: (() -> Unit)? = null

    /**
     * 服务端上报用：每次 register 现造 device_info（app 版本 / 系统 / 机型 /
     * 无障碍开关 / 上次错误）。参数是本连接循环记录的 [lastError]，供服务端
     * 网页显示「上次错误」。由持有 Context 的一侧（服务）注入。
     */
    var deviceInfoProvider: ((lastError: String?) -> kotlinx.serialization.json.JsonObject?)? = null

    /** 无障碍开关探测器：心跳里轮询，变化时上报 device_status 事件。 */
    var accessibilityStateProvider: (() -> Boolean?)? = null

    private val _state = MutableStateFlow<ConnectionState>(ConnectionState.Idle)
    val state: StateFlow<ConnectionState> = _state.asStateFlow()

    /** Set true by [stop] to break the reconnect loop. */
    @Volatile
    private var stopped = false

    /** Whether the user explicitly asked to disconnect (vs. a network drop). */
    @Volatile
    private var userInitiatedStop = false

    /**
     * 当前 [runOnce] 的活动连接句柄。`stop()` 用它**真正关闭** socket——
     * 之前只置 `stopped` 标志位、从不调 `ws.close()`，已建立的连接会一直挂到
     * 服务端超时（表现=「断开连接按钮没反应」，服务端持续显示已连接）。
     */
    @Volatile
    private var activeSession: WebSocketSession? = null

    /** 最近一次断开/失败原因（人话），随下一次 register 上报服务端显示。 */
    @Volatile
    private var lastError: String? = null

    /**
     * Runs the connect loop until [stop] is called or a fatal close is received.
     * Suspends the caller for the lifetime of the connection — run it on a
     * foreground-service scope, not the UI thread.
     */
    suspend fun connectLoop(credential: DeviceCredential) {
        var attempt = 0
        while (!stopped) {
            val upSinceMs = System.currentTimeMillis()
            val outcome = runOnce(credential)

            // stop() 在 runOnce 期间被调用时，socket 已被关闭，runOnce 走
            // RetryableClose 返回。这里先拦一道：不闪「已断开/重试中」、不做
            // 退避延迟，直接落 Idle 退出。否则用户点断开后还会看到一轮重试再退出。
            if (stopped) {
                _state.value = ConnectionState.Idle
                return
            }

            when (outcome) {
                ConnectionOutcome.FatalAuth -> {
                    lastError = "认证失败（token 无效或已撤销）"
                    android.util.Log.w("DC:WsClient", "outcome=FatalAuth (4003) — credential wiped, stop")
                    onCredentialWiped?.invoke()
                    _state.value = ConnectionState.Fatal(
                        reason = "认证失败：token 已失效，请重新配对",
                        needsRePair = true,
                    )
                    return
                }
                ConnectionOutcome.FatalVersion -> {
                    lastError = "协议版本不匹配，请升级 app"
                    android.util.Log.w("DC:WsClient", "outcome=FatalVersion (4004) — stop")
                    _state.value = ConnectionState.Fatal(
                        reason = "协议版本不匹配，请升级 app",
                        needsRePair = false,
                    )
                    return
                }
                ConnectionOutcome.RetryableClose -> {
                    if (System.currentTimeMillis() - upSinceMs > BACKOFF_CAP_MS) {
                        attempt = 0
                    } else {
                        attempt++
                    }
                    val delayMs = backoffDelay(attempt)
                    android.util.Log.i("DC:WsClient", "outcome=RetryableClose attempt=$attempt backoff=${delayMs}ms")
                    _state.value = ConnectionState.Disconnected(
                        reason = "连接断开，${delayMs / 1000.0}s 后重试",
                        willRetry = true,
                    )
                    delay(delayMs)
                }
            }
        }
        _state.value = ConnectionState.Idle
    }

    /** One dial → register → serve → close cycle. */
    private suspend fun runOnce(credential: DeviceCredential): ConnectionOutcome {
        _state.value = ConnectionState.Connecting
        dispatcher.resetForNewConnection()

        val url = wsUrl(credential.serverUrl)
        val request = Request.Builder().url(url).build()
        android.util.Log.i("DC:WsClient", "dial url=$url device=${credential.deviceId}")

        val session = WebSocketSession()
        val ws = client.newWebSocket(request, SessionListener(session, url))
        session.ws = ws
        activeSession = session
        try {
            // Send register immediately (§4.2.1: within 10s of open). Capabilities are
            // trimmed to what THIS platform can actually do — on API < 33 the accessibility
            // IME does not exist, so type_text is not declared and the server won't route it.
            // device_info 是给服务端网页展示的上报块（版本/系统/机型/无障碍/上次错误），
            // 每次连接现造——reconnect 即刷新，服务端无需额外推送通道。
            session.outbound(RegisterFrame(
                protocolVersion = PROTOCOL_VERSION,
                deviceId = credential.deviceId,
                auth = Auth(scheme = "token", token = credential.token),
                capabilities = Capabilities.forApiLevel(Build.VERSION.SDK_INT),
                deviceInfo = deviceInfoProvider?.invoke(lastError),
            ))

            // Wait for registered (or a close) with a 10s budget — matches the server's
            // 4008 register-timeout so we don't hang forever if the server is silent.
            val registered = when (val first = session.waitForRegisteredOrClose(REGISTER_TIMEOUT_MS)) {
                is RegisteredResult.Registered -> first.frame
                RegisteredResult.Closed -> {
                    android.util.Log.w("DC:WsClient", "register wait: closed before registered, closeCode=${session.closeCode}")
                    return finishRunOnce(session.closeCode)
                }
                RegisteredResult.Timeout -> {
                    android.util.Log.w("DC:WsClient", "register wait: TIMEOUT ${REGISTER_TIMEOUT_MS}ms (server silent after dial)")
                    return finishRunOnce(-1, "注册超时")
                }
            }

            _state.value = ConnectionState.Connected(credential.deviceId)

            // Heartbeat loop: every heartbeat_interval_s, no ack (§10). 顺带探测无障碍
            // 开关，变化即上报 device_status 事件——服务端网页实时显示开关状态，
            // 不用等下次重连的 register。
            val heartbeatJob = session.scope.launch {
                val intervalMs = registered.heartbeatIntervalS.coerceAtLeast(1) * 1000L
                var seq = 0L
                var lastAccessibility: Boolean? = null
                while (session.scope.isActive && session.isOpen) {
                    session.outbound(HeartbeatFrame(deviceId = credential.deviceId, seq = seq))
                    seq++
                    val acc = accessibilityStateProvider?.invoke()
                    if (acc != null && acc != lastAccessibility) {
                        lastAccessibility = acc
                        session.outbound(EventFrame(
                            kind = "device_status",
                            data = buildJsonObject {
                                put("accessibility_enabled", acc)
                            },
                        ))
                    }
                    delay(intervalMs)
                }
            }

            // Call dispatch loop: pull inbound frames, run calls through the dispatcher.
            while (session.scope.isActive && session.isOpen) {
                val frame = session.nextInbound() ?: break
                when (frame) {
                    is InboundFrame.Call -> {
                        session.scope.launch {
                            val response = dispatcher.dispatch(frame.call)
                            session.outbound(response)
                        }
                    }
                    is InboundFrame.CallCancel -> {
                        // §6: cancel is advisory. The dispatcher has no cooperative cancel,
                        // so the handler (if already finished) will finish and send its
                        // real result; this is explicitly allowed ("either the real result
                        // if already finished"). If the handler hasn't finished yet, the
                        // server will see our late response and may cancel it on its side.
                    }
                    is InboundFrame.Unknown -> {
                        // §3: unknown frame types MUST be ignored, not errored.
                    }
                }
            }

            heartbeatJob.cancel()
            return finishRunOnce(session.closeCode)
        } finally {
            // 无论从哪条路径离开（正常断开 / Timeout 早退 / 注册前关闭）：都掐掉
            // 会话 scope、收尾 socket。心跳与命令处理协程都挂在 session.scope 上，
            // cancel 一并终止。Timeout 早退前 socket 会一直挂着，下一轮又
            // newWebSocket 开新连接，旧连接泄漏——服务端会看到重复 register。
            session.scope.cancel()
            activeSession = null
        }
    }

    /**
     * 记录人话断开原因（随下一次 register 的 device_info.last_error 上报服务端网页）
     * 并归类本次 close。[reason] 供调用方给出比 close code 更准确的说法。
     */
    private fun finishRunOnce(code: Int, reason: String? = null): ConnectionOutcome {
        lastError = reason ?: when (code) {
            4002 -> "发送帧过大被拒"
            4007 -> "同设备重复连接被服务端替换"
            4008 -> "注册超时"
            4009 -> "被新连接替换"
            4010 -> "心跳超时断开"
            -1 -> "网络错误"
            else -> "连接断开（code=$code）"
        }
        return classifyClose(code)
    }

    /** Maps a WS close code to a [ConnectionOutcome] per §13. */
    private fun classifyClose(code: Int): ConnectionOutcome =
        when (code) {
            4003 -> ConnectionOutcome.FatalAuth
            4004 -> ConnectionOutcome.FatalVersion
            4002, 4007, 4008, 4009, 4010,
            1000, 1001, 1002, 1009,
            -1 -> ConnectionOutcome.RetryableClose
            else -> ConnectionOutcome.RetryableClose
        }

    /**
     * Full-jitter backoff (§7): `delay = random(0, min(cap, base * 2^attempt))`.
     * The whole range is uniform-random, not "half + random half".
     *
     * Companion-level so the backoff formula is unit-testable without constructing a
     * [WsClient] (which drags in a dispatcher + handlers + CoreGraph + Context).
     */
    private fun backoffDelay(attempt: Int): Long = computeBackoffDelay(attempt)

    /**
     * Converts the saved server ADDRESS to a ws(s):// URL + the fixed
     * `/mcp/device-control/ws/device` path. 路径契约（2026-09-06 重构）：凭据里存的是
     * 用户填的纯地址（不带路径），WS 路径固定在这里拼——与 PairingClient 拼
     * `/mcp/device-control/pair` 同源，用户永远不需要填路径。
     * 兼容旧凭据：若旧版存过带 `/mcp/device-control` 后缀的地址，剥掉再拼。
     */
    private fun wsUrl(base: String): String {
        val trimmed = base.trimEnd('/').removeSuffix("/mcp/device-control").trimEnd('/')
        return when {
            trimmed.startsWith("https://") -> "wss://" + trimmed.removePrefix("https://") + PATH
            trimmed.startsWith("http://") -> "ws://" + trimmed.removePrefix("http://") + PATH
            trimmed.startsWith("ws://") || trimmed.startsWith("wss://") -> trimmed + PATH
            else -> "ws://$trimmed$PATH" // default to ws://; UI warns on non-loopback http
        }
    }

    /**
     * Stops the loop. [userInitiated] distinguishes a user tap from a network drop.
     *
     * 关键：不能只置标志位——已建立的 socket 不会自己断，serve 循环要等到下一帧才
     * 可能退出（表现=「断开没反应」）。这里直接 close 当前连接 + cancel 会话 scope，
     * runOnce 的 serve 循环随即退出，connectLoop 检查 [stopped] 后落 Idle 返回。
     */
    fun stop(userInitiated: Boolean = true) {
        userInitiatedStop = userInitiated
        stopped = true
        val session = activeSession
        session?.ws?.close(1000, null)
        session?.scope?.cancel()
    }

    private enum class ConnectionOutcome {
        RetryableClose,
        FatalAuth,
        FatalVersion,
    }

    companion object {
        private const val PATH = "/mcp/device-control/ws/device"
        private const val REGISTER_TIMEOUT_MS = 10_000L
        private const val BACKOFF_BASE_MS = 1_000L
        private const val BACKOFF_CAP_MS = 30_000L
        private const val BACKOFF_SHIFT_CAP = 15

        /**
         * Full-jitter backoff (§7): `delay = random(0, min(cap, base * 2^attempt))`.
         * Public for test access — the formula is a protocol invariant worth pinning
         * without constructing a full [WsClient].
         *
         * The shift is capped at [BACKOFF_SHIFT_CAP] (15): 2^15 = 32768 > the 30s cap in
         * milliseconds, so larger attempts saturate at the cap rather than overflowing
         * the Long (1L << 100 would wrap negative and break Random.nextLong's range).
         */
        fun computeBackoffDelay(attempt: Int): Long {
            val shift = attempt.coerceAtMost(BACKOFF_SHIFT_CAP)
            val upper = min(BACKOFF_CAP_MS, BACKOFF_BASE_MS shl shift)
            return Random.nextLong(0, upper + 1)
        }
    }
}