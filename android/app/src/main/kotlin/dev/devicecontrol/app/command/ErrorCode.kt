package dev.devicecontrol.app.command

/**
 * The closed set of wire `error.code` values (protocol v0 §12). The device only ever
 * emits codes from this list; the server synthesizes `device_error` if a `call-response`
 * arrives malformed (`wsdevice.go:167-181`), so the app must produce well-formed
 * responses itself.
 *
 * The [from] mapper turns the exceptions thrown by `core/` (and by the handlers) into
 * one of these codes. The mapping is deliberately conservative: anything not
 * clearly a bad-arg/stale/permission/timeout is `device_error`, never `internal` —
 * `internal` is reserved for genuine device-side invariant violations the server
 * should surface to an operator, and over-reporting it would drown real signals.
 */
object ErrorCode {
    const val UNSUPPORTED = "unsupported"
    const val BAD_ARGS = "bad_args"
    const val STALE_NODE = "stale_node"
    const val NOT_FOUND = "not_found"
    const val TIMEOUT = "timeout"
    const val CANCELLED = "cancelled"
    const val DUPLICATE_REQUEST = "duplicate_request"
    const val OVERLOADED = "overloaded"
    const val PERMISSION_DENIED = "permission_denied"
    const val NOT_READY = "not_ready"
    const val DEVICE_ERROR = "device_error"
    const val INTERNAL = "internal"

    /**
     * Maps a [Throwable] from a handler to a wire error code.
     *
     * Order matters: the most specific types first.
     *  - [CommandException] carries its own explicit code (handler knew the answer).
     *  - [CoreException.PermissionDenied] / [NodeNotFound] / [InvalidParams] / [Timeout]
     *    map one-to-one onto the wire codes (see CoreException's own table).
     *  - `NoSuchElementException` is what `ActionExecutorImpl` throws when a `node_id`
     *    resolves to nothing — that is `stale_node` (the caller's id is stale, re-read).
     *  - `IllegalArgumentException` from `require(...)` in a handler is `bad_args`.
     *  - `IllegalStateException` whose message says the service is unavailable is
     *    `not_ready`; any other `IllegalStateException` is `device_error`.
     *  - `kotlinx.coroutines.TimeoutCancellationException` is the `withTimeout` budget
     *    expiring — `timeout`.
     *  - Everything else is `device_error`.
     */
    fun from(t: Throwable): String {
        if (t is CommandException) return t.code
        when (t) {
            is dev.devicecontrol.core.CoreException.PermissionDenied -> return PERMISSION_DENIED
            is dev.devicecontrol.core.CoreException.NodeNotFound -> return STALE_NODE
            is dev.devicecontrol.core.CoreException.InvalidParams -> return BAD_ARGS
            is dev.devicecontrol.core.CoreException.Timeout -> return TIMEOUT
            is dev.devicecontrol.core.CoreException.InternalError -> return INTERNAL
            is dev.devicecontrol.core.CoreException.ActionFailed -> return DEVICE_ERROR
        }
        when (t) {
            is NoSuchElementException -> return STALE_NODE
            is IllegalArgumentException -> return BAD_ARGS
            is kotlinx.coroutines.TimeoutCancellationException -> return TIMEOUT
            is kotlinx.coroutines.CancellationException -> return CANCELLED
            is IllegalStateException -> {
                val msg = t.message.orEmpty()
                return if ("not available" in msg || "not enabled" in msg || "not ready" in msg) {
                    NOT_READY
                } else {
                    DEVICE_ERROR
                }
            }
        }
        return DEVICE_ERROR
    }
}
