package dev.devicecontrol.app.command

import dev.devicecontrol.app.protocol.CallFrame
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotNull
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/**
 * Protocol-level obligations of [CommandDispatcher]: request_id dedup, the 8-call
 * in-flight cap, timeout enforcement, and the exception→error mapping. These are pure
 * logic — no accessibility, no Context, no device — so they run in plain JUnit.
 *
 * The handlers map is a stub: `ok` sleeps briefly and returns `{}`, `slow` sleeps past
 * the timeout, `throw` raises a typed exception. That's enough to exercise every
 * dispatch branch.
 */
class CommandDispatcherTest {
    private fun dispatcher(handlers: Map<String, suspend (JsonObject) -> JsonElement>): CommandDispatcher =
        CommandDispatcher(handlers)

    private val okHandler: suspend (JsonObject) -> JsonElement = {
        delay(10)
        buildJsonObject {}
    }
    private val slowHandler: suspend (JsonObject) -> JsonElement = {
        delay(5_000)
        buildJsonObject {}
    }
    private val throwingHandler: suspend (JsonObject) -> JsonElement = {
        throw IllegalArgumentException("bad x")
    }

    @Test
    fun `successful call returns ok=true with data`() = runBlocking {
        val d = dispatcher(mapOf("tap" to okHandler))
        val r = d.dispatch(call("req_1", "tap"))
        assertTrue(r.ok)
        assertNotNull(r.data)
        assertNull(r.error)
    }

    @Test
    fun `duplicate request_id is rejected`() = runBlocking {
        val d = dispatcher(mapOf("tap" to okHandler))
        val first = d.dispatch(call("req_1", "tap"))
        assertTrue(first.ok)
        // Second with same request_id — must NOT re-execute (would tap twice).
        val second = d.dispatch(call("req_1", "tap"))
        assertFalse(second.ok)
        assertEquals(ErrorCode.DUPLICATE_REQUEST, second.error?.code)
    }

    @Test
    fun `reset clears the dedup set`() = runBlocking {
        val d = dispatcher(mapOf("tap" to okHandler))
        d.dispatch(call("req_1", "tap"))
        val dup = d.dispatch(call("req_1", "tap"))
        assertFalse(dup.ok)
        d.resetForNewConnection()
        // After reset (new connection per §4.2), the same request_id is acceptable again.
        val again = d.dispatch(call("req_1", "tap"))
        assertTrue(again.ok)
    }

    @Test
    fun `unknown command returns unsupported`() = runBlocking {
        val d = dispatcher(emptyMap())
        val r = d.dispatch(call("req_1", "not_a_command"))
        assertFalse(r.ok)
        assertEquals(ErrorCode.UNSUPPORTED, r.error?.code)
    }

    @Test
    fun `timeout maps to timeout error`() = runBlocking {
        val d = dispatcher(mapOf("slow" to slowHandler))
        val r = d.dispatch(call("req_1", "slow", timeoutMs = 100))
        assertFalse(r.ok)
        assertEquals(ErrorCode.TIMEOUT, r.error?.code)
    }

    @Test
    fun `timeout_ms is clamped to minimum`() = runBlocking {
        // A timeout_ms of 0 would instantly time out; the dispatcher clamps to 1s.
        val d = dispatcher(mapOf("tap" to okHandler))
        val r = d.dispatch(call("req_1", "tap", timeoutMs = 0))
        assertTrue(r.ok)
    }

    @Test
    fun `handler exception maps to error code`() = runBlocking {
        val d = dispatcher(mapOf("throw" to throwingHandler))
        val r = d.dispatch(call("req_1", "throw"))
        assertFalse(r.ok)
        assertEquals(ErrorCode.BAD_ARGS, r.error?.code)
    }

    @Test
    fun `ninth in-flight call returns overloaded`() = runBlocking {
        // Launch 8 concurrent slow calls to fill the cap, then a 9th must be rejected.
        val d = dispatcher(mapOf("slow" to slowHandler))
        val filling = (1..8).map { it.toString() }
        coroutineScope {
            val jobs = filling.map { rid ->
                async { d.dispatch(call("rid_$rid", "slow", timeoutMs = 60_000)) }
            }
            // Give the slow handlers time to enter the in-flight section.
            delay(50)
            val ninth = d.dispatch(call("rid_9", "slow"))
            assertFalse(ninth.ok)
            assertEquals(ErrorCode.OVERLOADED, ninth.error?.code)
            assertTrue(ninth.error?.retryable == true)
            // Let the slow handlers time out and clean up.
            jobs.forEach { it.cancel() }
        }
    }

    private fun call(
        requestId: String,
        cmd: String,
        timeoutMs: Int = 15_000,
    ): CallFrame = CallFrame(
        type = "call",
        requestId = requestId,
        cmd = cmd,
        args = buildJsonObject {},
        timeoutMs = timeoutMs,
    )
}
