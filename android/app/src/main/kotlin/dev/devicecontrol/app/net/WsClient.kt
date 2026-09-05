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
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
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

    private val _state = MutableStateFlow<ConnectionState>(ConnectionState.Idle)
    val state: StateFlow<ConnectionState> = _state.asStateFlow()

    /** Set true by [stop] to break the reconnect loop. */
    @Volatile
    private var stopped = false

    /** Whether the user explicitly asked to disconnect (vs. a network drop). */
    @Volatile
    private var userInitiatedStop = false

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

            when (outcome) {
                ConnectionOutcome.FatalAuth -> {
                    onCredentialWiped?.invoke()
                    _state.value = ConnectionState.Fatal(
                        reason = "认证失败：token 已失效，请重新配对",
                        needsRePair = true,
                    )
                    return
                }
                ConnectionOutcome.FatalVersion -> {
                    _state.value = ConnectionState.Fatal(
                        reason = "协议版本不匹配，请升级 app",
                        needsRePair = false,
                    )
                    return
                }
                ConnectionOutcome.UserStopped -> {
                    _state.value = ConnectionState.Idle
                    return
                }
                ConnectionOutcome.RetryableClose -> {
                    if (System.currentTimeMillis() - upSinceMs > BACKOFF_CAP_MS) {
                        attempt = 0
                    } else {
                        attempt++
                    }
                    val delayMs = backoffDelay(attempt)
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

        val session = WebSocketSession()
        val ws = client.newWebSocket(request, SessionListener(session))
        session.ws = ws

        // Send register immediately (§4.2.1: within 10s of open). Capabilities are
        // trimmed to what THIS platform can actually do — on API < 33 the accessibility
        // IME does not exist, so type_text is not declared and the server won't route it.
        session.outbound(RegisterFrame(
            protocolVersion = PROTOCOL_VERSION,
            deviceId = credential.deviceId,
            auth = Auth(scheme = "token", token = credential.token),
            capabilities = Capabilities.forApiLevel(Build.VERSION.SDK_INT),
        ))

        // Wait for registered (or a close) with a 10s budget — matches the server's
        // 4008 register-timeout so we don't hang forever if the server is silent.
        val registered = when (val first = session.waitForRegisteredOrClose(REGISTER_TIMEOUT_MS)) {
            is RegisteredResult.Registered -> first.frame
            RegisteredResult.Closed -> return classifyClose(session.closeCode)
            RegisteredResult.Timeout -> return ConnectionOutcome.RetryableClose
        }

        _state.value = ConnectionState.Connected(credential.deviceId)

        // Heartbeat loop: every heartbeat_interval_s, no ack (§10).
        val heartbeatJob = session.scope.launch {
            val intervalMs = registered.heartbeatIntervalS.coerceAtLeast(1) * 1000L
            var seq = 0L
            while (session.scope.isActive && session.isOpen) {
                session.outbound(HeartbeatFrame(deviceId = credential.deviceId, seq = seq))
                seq++
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
        session.scope.cancel()
        return classifyClose(session.closeCode)
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

    /** Converts an http(s):// server base to ws(s):// + the fixed `/ws/device` path. */
    private fun wsUrl(base: String): String {
        val trimmed = base.trimEnd('/')
        return when {
            trimmed.startsWith("https://") -> "wss://" + trimmed.removePrefix("https://") + PATH
            trimmed.startsWith("http://") -> "ws://" + trimmed.removePrefix("http://") + PATH
            trimmed.startsWith("ws://") || trimmed.startsWith("wss://") -> trimmed + PATH
            else -> "ws://$trimmed$PATH" // default to ws://; UI warns on non-loopback http
        }
    }

    /** Stops the loop. [userInitiated] distinguishes a user tap from a network drop. */
    fun stop(userInitiated: Boolean = true) {
        userInitiatedStop = userInitiated
        stopped = true
    }

    private enum class ConnectionOutcome {
        RetryableClose,
        FatalAuth,
        FatalVersion,
        UserStopped,
    }

    companion object {
        private const val PATH = "/ws/device"
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