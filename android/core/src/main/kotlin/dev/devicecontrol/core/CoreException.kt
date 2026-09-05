package dev.devicecontrol.core

/**
 * Sealed exception hierarchy for device-control command failures.
 *
 * Vendored from ARC-MCP's `McpToolException` (MIT, see NOTICE). The MCP framing is
 * gone — nothing here catches these to build a `CallToolResult` — but the subclasses
 * are kept because they map one-to-one onto the wire error codes in
 * `spec/protocol-v0.md` §12, so the app layer can translate without inventing a
 * second taxonomy:
 *
 * | subclass            | wire `code`        |
 * |---------------------|--------------------|
 * | [InvalidParams]     | `invalid_args`     |
 * | [InternalError]     | `internal`         |
 * | [PermissionDenied]  | `permission_denied`|
 * | [NodeNotFound]      | `stale_node`       |
 * | [ActionFailed]      | `action_failed`    |
 * | [Timeout]           | `timeout`          |
 */
sealed class CoreException(
    message: String,
    cause: Throwable? = null,
) : Exception(message, cause) {
    class InvalidParams(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)

    class InternalError(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)

    class PermissionDenied(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)

    class NodeNotFound(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)

    class ActionFailed(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)

    class Timeout(
        message: String,
        cause: Throwable? = null,
    ) : CoreException(message, cause)
}
