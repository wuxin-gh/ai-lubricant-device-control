package dev.devicecontrol.app.net

import dev.devicecontrol.app.protocol.CallFrame
import dev.devicecontrol.app.protocol.CallResponseFrame
import dev.devicecontrol.app.protocol.EventFrame
import dev.devicecontrol.app.protocol.HeartbeatFrame
import dev.devicecontrol.app.protocol.RegisterFrame
import dev.devicecontrol.app.protocol.RegisteredFrame
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.KSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener

/**
 * Shared JSON config for the wire. `ignoreUnknownKeys` is the §3 forward-compat hinge
 * (unknown fields MUST be ignored); `encodeDefaults` keeps `protocol_version`,
 * `scheme`, etc. on the wire even when they equal the default.
 */
internal val WireJson: Json = Json {
    ignoreUnknownKeys = true
    encodeDefaults = true
}

/**
 * The per-connection state bridging OkHttp's callback-based [WebSocketListener] onto
 * coroutines. One instance per dial.
 *
 * Holds:
 *  - the [WebSocket] once the listener's `onOpen` fires,
 *  - a [scope] for call-handler coroutines launched on this connection,
 *  - a [registered] deferred resolved by the first `registered` frame (or failed on
 *    close before it arrives),
 *  - an [inbound] channel carrying `call`/`call-cancel` frames for the dispatch loop,
 *  - the [closeCode] the server (or transport) ended with, for classification.
 */
class WebSocketSession {
    var ws: WebSocket? = null
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val registered = CompletableDeferred<RegisteredFrame>()
    private val inbound = Channel<InboundFrame>(Channel.UNLIMITED)
    private val _open = MutableStateFlow(false)
    val open: StateFlow<Boolean> = _open
    val isOpen: Boolean get() = _open.value

    /** Set by the listener before the channel is closed. -1 = failure without a code. */
    var closeCode: Int = -1
        private set

    /** Sends a frame as JSON. Returns false if the WS rejected the send (backpressure). */
    fun outbound(frame: Any): Boolean {
        val text = WireJson.encodeToString(serializerFor(frame), frame)
        return ws?.send(text) ?: false
    }

    /**
     * Waits up to [timeoutMs] for `registered` or a close. Returns [RegisteredResult.Closed]
     * if the connection ended before `registered` arrived.
     */
    suspend fun waitForRegisteredOrClose(timeoutMs: Long): RegisteredResult {
        val won = withTimeoutOrNull(timeoutMs) { registered.await() }
        return when {
            won != null -> RegisteredResult.Registered(won)
            registered.isCompleted -> RegisteredResult.Registered(registered.getCompleted())
            !isOpen -> RegisteredResult.Closed
            else -> RegisteredResult.Timeout
        }
    }

    /** Next inbound call/cancel frame, or null when the stream is closed. */
    suspend fun nextInbound(): InboundFrame? = inbound.receiveCatching().getOrNull()

    fun onRegistered(frame: RegisteredFrame) {
        registered.complete(frame)
    }

    fun onInbound(frame: InboundFrame) {
        inbound.trySend(frame)
    }

    fun onClose(code: Int) {
        closeCode = code
        _open.value = false
        if (!registered.isCompleted) {
            registered.completeExceptionally(ClosedBeforeRegister(code))
        }
        inbound.close()
    }

    fun onOpen() {
        _open.value = true
    }
}

class ClosedBeforeRegister(val code: Int) : Exception("connection closed before registered (code=$code)")

/** What the dispatch loop got when waiting for the first frame after register. */
sealed class RegisteredResult {
    data class Registered(val frame: RegisteredFrame) : RegisteredResult()
    object Timeout : RegisteredResult()
    object Closed : RegisteredResult()
}

/** A typed inbound frame from the server. Only `call` and `call-cancel` are actionable. */
sealed class InboundFrame {
    data class Call(val call: CallFrame) : InboundFrame()
    data class CallCancel(val requestId: String) : InboundFrame()
    /** Unknown `type` — MUST be ignored per §3, surfaced for completeness. */
    data class Unknown(val type: String) : InboundFrame()
}

/**
 * The OkHttp listener. Parses each text frame into a typed [InboundFrame] (or a
 * [RegisteredFrame]) and forwards to the [session]. All callbacks run on OkHttp's
 * worker thread; the only mutation they do is post to the session's channel/deferred,
 * which are thread-safe.
 */
class SessionListener(
    private val session: WebSocketSession,
) : WebSocketListener() {
    override fun onOpen(webSocket: WebSocket, response: Response) {
        session.ws = webSocket
        session.onOpen()
    }

    override fun onMessage(webSocket: WebSocket, text: String) {
        val type = parseType(text) ?: return
        when (type) {
            "registered" -> {
                val frame = WireJson.decodeFromString(RegisteredFrame.serializer(), text)
                session.onRegistered(frame)
            }
            "call" -> {
                val frame = WireJson.decodeFromString(CallFrame.serializer(), text)
                session.onInbound(InboundFrame.Call(frame))
            }
            "call-cancel" -> {
                val requestId = WireJson.parseToJsonElement(text)
                    .jsonObject["request_id"]?.jsonPrimitive?.contentOrNull ?: return
                session.onInbound(InboundFrame.CallCancel(requestId))
            }
            else -> session.onInbound(InboundFrame.Unknown(type))
        }
    }

    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
        webSocket.close(1000, null)
    }

    override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
        session.onClose(code)
    }

    override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
        session.onClose(-1)
    }

    private fun parseType(text: String): String? =
        runCatching {
            WireJson.parseToJsonElement(text).jsonObject["type"]?.jsonPrimitive?.contentOrNull
        }.getOrNull()
}

/** Picks the serializer for an outbound frame by its runtime type. */
@Suppress("UNCHECKED_CAST")
private fun serializerFor(frame: Any): KSerializer<Any> = when (frame) {
    is RegisterFrame -> RegisterFrame.serializer()
    is HeartbeatFrame -> HeartbeatFrame.serializer()
    is CallResponseFrame -> CallResponseFrame.serializer()
    is EventFrame -> EventFrame.serializer()
    else -> throw IllegalArgumentException("no wire serializer for ${frame::class}")
} as KSerializer<Any>
