package dev.devicecontrol.app.command

import android.util.Log
import dev.devicecontrol.app.protocol.CallFrame
import dev.devicecontrol.app.protocol.CallResponseFrame
import dev.devicecontrol.app.protocol.WireError
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.withTimeout

/**
 * One `call` → one `call-response`, with the protocol obligations of §6 handled in
 * one place so the 16 handlers never see them:
 *
 *  - **request_id dedup (per connection)**: the same `request_id` twice on one
 *    connection is a server bug; we answer `duplicate_request` rather than execute a
 *    mutating command twice (a replayed `tap` would tap twice).
 *  - **in-flight cap (≤ [MAX_IN_FLIGHT])**: §5 lets the server pipeline up to 8 calls;
 *    the device may also self-limit. Over the cap we answer `overloaded` — the server
 *    retries with backoff.
 *  - **timeout budget**: `timeout_ms` (default 15s, max 60s, §6) wraps the handler in
 *    `withTimeout`. A `call-cancel` racing a timeout still gets a `call-response`
 *    (§6: cancel is advisory).
 *  - **error mapping**: any thrown exception becomes a well-formed `ok=false` frame
 *    (the server synthesizes `device_error` for malformed responses, `wsdevice.go`).
 *
 * One instance per connection: [seenRequestIds] and [inFlight] are per-connection
 * state (§4.2), recreated on every reconnect via [resetForNewConnection].
 */
class CommandDispatcher(
    private val handlers: Map<String, suspend (kotlinx.serialization.json.JsonObject) -> kotlinx.serialization.json.JsonElement>,
) {
    private val seenRequestIds = HashSet<String>()
    private var inFlight = 0

    /** Clears per-connection state. Called by the WS client after each reconnect. */
    @Synchronized
    fun resetForNewConnection() {
        seenRequestIds.clear()
        inFlight = 0
    }

    /**
     * Executes one call. Never throws: every path returns a [CallResponseFrame], so the
     * WS client can always send something back — a dropped response blocks the
     * server's caller until its own timeout.
     */
    suspend fun dispatch(call: CallFrame): CallResponseFrame {
        synchronized(this) {
            if (!seenRequestIds.add(call.requestId)) {
                return errorResponse(
                    call.requestId,
                    ErrorCode.DUPLICATE_REQUEST,
                    "request_id ${call.requestId} already answered on this connection",
                )
            }
            if (inFlight >= MAX_IN_FLIGHT) {
                seenRequestIds.remove(call.requestId)
                return errorResponse(
                    call.requestId,
                    ErrorCode.OVERLOADED,
                    "device is executing $MAX_IN_FLIGHT calls; retry later",
                    retryable = true,
                )
            }
            inFlight++
        }

        return try {
            val handler = handlers[call.cmd]
            if (handler == null) {
                // The server gates against capabilities at dispatch time (hub.Supports),
                // so an unknown cmd here means capability drift or a server bug.
                errorResponse(call.requestId, ErrorCode.UNSUPPORTED, "unknown command: ${call.cmd}")
            } else {
                val budgetMs = call.timeoutMs.coerceIn(MIN_TIMEOUT_MS, MAX_TIMEOUT_MS).toLong()
                try {
                    val data = withTimeout(budgetMs) { handler(call.args) }
                    CallResponseFrame(
                        requestId = call.requestId,
                        ok = true,
                        data = data,
                    )
                } catch (t: TimeoutCancellationException) {
                    errorResponse(
                        call.requestId,
                        ErrorCode.TIMEOUT,
                        "command timed out after ${budgetMs}ms",
                    )
                } catch (t: Throwable) {
                    Log.e(TAG, "Command '${call.cmd}' failed", t)
                    mapException(t, call.requestId)
                }
            }
        } finally {
            // Only the in-flight slot is released here. The request_id STAYS in
            // seenRequestIds for the rest of the connection (§6: a replayed request_id
            // is a duplicate_request, not a re-execution). Cleared on reconnect.
            synchronized(this) {
                inFlight--
            }
        }
    }

    /** Maps a handler exception onto the wire via [ErrorCode.from]. */
    private fun mapException(
        t: Throwable,
        requestId: String,
    ): CallResponseFrame {
        val code = ErrorCode.from(t)
        // For the catch-all device_error, don't leak exception internals; give the
        // server something actionable without stack details.
        val message = if (code == ErrorCode.DEVICE_ERROR) "command failed on device" else t.message
        return errorResponse(requestId, code, message)
    }

    private fun errorResponse(
        requestId: String,
        code: String,
        message: String?,
        retryable: Boolean = false,
    ): CallResponseFrame =
        CallResponseFrame(
            requestId = requestId,
            ok = false,
            error = WireError(code = code, message = message, retryable = retryable),
        )

    companion object {
        private const val TAG = "DC:CommandDispatcher"

        /** §5: the server may pipeline up to 8 calls; the device self-limits to the same. */
        const val MAX_IN_FLIGHT = 8

        /** §6: timeout_ms is at least 1s and at most 60s. */
        const val MIN_TIMEOUT_MS = 1_000
        const val MAX_TIMEOUT_MS = 60_000
    }
}
