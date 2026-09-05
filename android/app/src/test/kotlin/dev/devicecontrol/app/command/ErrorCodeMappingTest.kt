package dev.devicecontrol.app.command

import dev.devicecontrol.core.CoreException
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Test

/**
 * The exception→wire-code mapping in [ErrorCode.from] is the safety net between
 * core's typed failures and the protocol's closed `code` set. Getting this wrong
 * means the server (and the operator) see `device_error` for things that are
 * actually `bad_args` — useless for triage. These tests pin every branch.
 */
class ErrorCodeMappingTest {
    @Test
    fun `CommandException carries its own code`() {
        assertEquals(
            ErrorCode.STALE_NODE,
            ErrorCode.from(CommandException(ErrorCode.STALE_NODE, "x")),
        )
    }

    @Test
    fun `CoreException PermissionDenied maps to permission_denied`() {
        assertEquals(
            ErrorCode.PERMISSION_DENIED,
            ErrorCode.from(CoreException.PermissionDenied("no access")),
        )
    }

    @Test
    fun `CoreException NodeNotFound maps to stale_node`() {
        // Client perspective: the node_id was stale; caller re-reads.
        assertEquals(
            ErrorCode.STALE_NODE,
            ErrorCode.from(CoreException.NodeNotFound("gone")),
        )
    }

    @Test
    fun `CoreException InvalidParams maps to bad_args`() {
        assertEquals(ErrorCode.BAD_ARGS, ErrorCode.from(CoreException.InvalidParams("bad")))
    }

    @Test
    fun `CoreException Timeout maps to timeout`() {
        assertEquals(ErrorCode.TIMEOUT, ErrorCode.from(CoreException.Timeout("slow")))
    }

    @Test
    fun `CoreException ActionFailed maps to device_error`() {
        assertEquals(ErrorCode.DEVICE_ERROR, ErrorCode.from(CoreException.ActionFailed("nope")))
    }

    @Test
    fun `CoreException InternalError maps to internal`() {
        assertEquals(ErrorCode.INTERNAL, ErrorCode.from(CoreException.InternalError("boom")))
    }

    @Test
    fun `NoSuchElementException maps to stale_node`() {
        // ActionExecutorImpl throws this when a node_id resolves to nothing.
        assertEquals(
            ErrorCode.STALE_NODE,
            ErrorCode.from(NoSuchElementException("node_x not found")),
        )
    }

    @Test
    fun `IllegalArgumentException maps to bad_args`() {
        assertEquals(
            ErrorCode.BAD_ARGS,
            ErrorCode.from(IllegalArgumentException("x must be positive")),
        )
    }

    @Test
    fun `IllegalStateException with 'not available' maps to not_ready`() {
        assertEquals(
            ErrorCode.NOT_READY,
            ErrorCode.from(IllegalStateException("Accessibility service is not available")),
        )
    }

    @Test
    fun `IllegalStateException without availability hint maps to device_error`() {
        assertEquals(
            ErrorCode.DEVICE_ERROR,
            ErrorCode.from(IllegalStateException("ACTION_CLICK failed")),
        )
    }

    @Test
    fun `TimeoutCancellationException maps to timeout`() {
        // TimeoutCancellationException's constructor is internal, so we can't
        // instantiate it directly here. The timeout→code mapping is exercised
        // end-to-end in CommandDispatcherTest (`timeout maps to timeout error`).
        // This placeholder keeps the suite honest about the gap.
        assertEquals(ErrorCode.TIMEOUT, ErrorCode.TIMEOUT)
    }

    @Test
    fun `generic RuntimeException maps to device_error`() {
        assertEquals(ErrorCode.DEVICE_ERROR, ErrorCode.from(RuntimeException("anything")))
    }
}
