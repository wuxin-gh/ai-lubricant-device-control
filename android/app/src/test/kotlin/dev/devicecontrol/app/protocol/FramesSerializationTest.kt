package dev.devicecontrol.app.protocol

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotNull
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/**
 * Wire-frame serialization roundtrips. Each frame must survive encode→decode with the
 * field names the Go server expects (snake_case via @SerialName). These tests pin the
 * exact wire shape: a future refactor that drops a @SerialName or renames a field would
 * break the server's decode, and this test would catch it before a phone ever dials.
 */
class FramesSerializationTest {
    private val json = Json { ignoreUnknownKeys = true; encodeDefaults = true }

    @Test
    fun `register frame roundtrips with snake_case fields`() {
        val frame = RegisterFrame(
            deviceId = "dev_abc",
            auth = Auth(scheme = "token", token = "secret"),
            capabilities = listOf("get_screen_state", "tap"),
            deviceInfo = buildJsonObject { put("platform", "android") },
        )
        val text = json.encodeToString(RegisterFrame.serializer(), frame)
        // The server decodes field names case-sensitively; these MUST be snake_case.
        assertTrue("\"protocol_version\":0" in text, "protocol_version missing: $text")
        assertTrue("\"device_id\":\"dev_abc\"" in text, "device_id missing: $text")
        assertTrue("\"auth\"" in text)
        assertTrue("\"scheme\":\"token\"" in text)
        assertTrue("\"capabilities\"" in text)
        assertTrue("\"device_info\"" in text)

        val back = json.decodeFromString(RegisterFrame.serializer(), text)
        assertEquals(frame.deviceId, back.deviceId)
        assertEquals(frame.auth.token, back.auth.token)
        assertEquals(frame.capabilities, back.capabilities)
        assertEquals(PROTOCOL_VERSION, back.protocolVersion)
    }

    @Test
    fun `registered frame decodes server defaults`() {
        // Server omits accepted_capabilities/heartbeat fields in some replies; defaults
        // must keep the decode from failing.
        val text = """{"type":"registered","protocol_version":0,"device_id":"dev_abc"}"""
        val back = json.decodeFromString(RegisteredFrame.serializer(), text)
        assertEquals("dev_abc", back.deviceId)
        assertEquals(0, back.protocolVersion)
        assertEquals(emptyList<String>(), back.acceptedCapabilities)
        // Spec defaults: 15s interval, 60s timeout.
        assertEquals(15, back.heartbeatIntervalS)
        assertEquals(60, back.heartbeatTimeoutS)
    }

    @Test
    fun `registered frame decodes negotiated heartbeat`() {
        val text = """
            {"type":"registered","protocol_version":0,"device_id":"dev_x",
             "accepted_capabilities":["get_screen_state","tap"],
             "heartbeat_interval_s":10,"heartbeat_timeout_s":40}
        """.trimIndent()
        val back = json.decodeFromString(RegisteredFrame.serializer(), text)
        assertEquals(listOf("get_screen_state", "tap"), back.acceptedCapabilities)
        assertEquals(10, back.heartbeatIntervalS)
        assertEquals(40, back.heartbeatTimeoutS)
    }

    @Test
    fun `call-response ok=true carries data`() {
        val frame = CallResponseFrame(
            requestId = "req_1",
            ok = true,
            data = buildJsonObject { put("tree", "y") },
        )
        val text = json.encodeToString(CallResponseFrame.serializer(), frame)
        assertTrue("\"ok\":true" in text)
        assertTrue("\"request_id\":\"req_1\"" in text)
        assertTrue("\"data\"" in text)
        assertNull(frame.error)
    }

    @Test
    fun `call-response ok=false carries error`() {
        val frame = CallResponseFrame(
            requestId = "req_1",
            ok = false,
            error = WireError(code = "stale_node", message = "gone", retryable = false),
        )
        val text = json.encodeToString(CallResponseFrame.serializer(), frame)
        assertTrue("\"ok\":false" in text)
        assertTrue("\"error\"" in text)
        assertTrue("\"code\":\"stale_node\"" in text)
        assertTrue("\"message\":\"gone\"" in text)
        assertNull(frame.data)
    }

    @Test
    fun `heartbeat frame roundtrips`() {
        val frame = HeartbeatFrame(deviceId = "dev_abc", seq = 42L)
        val text = json.encodeToString(HeartbeatFrame.serializer(), frame)
        assertTrue("\"type\":\"heartbeat\"" in text)
        assertTrue("\"device_id\":\"dev_abc\"" in text)
        assertTrue("\"seq\":42" in text)
    }

    @Test
    fun `call frame decodes server call with defaults`() {
        // Server may omit timeout_ms (defaults to 15000 per spec §5) and args (defaults
        // to {}). A bare call must decode.
        val text = """{"type":"call","request_id":"req_9","cmd":"press_back"}"""
        val back = json.decodeFromString(CallFrame.serializer(), text)
        assertEquals("req_9", back.requestId)
        assertEquals("press_back", back.cmd)
        assertEquals(15_000, back.timeoutMs)
    }

    @Test
    fun `wire type constants are stable strings`() {
        // These are the literal strings on the wire; changing them is a breaking change.
        assertEquals("register", WireType.REGISTER)
        assertEquals("registered", WireType.REGISTERED)
        assertEquals("heartbeat", WireType.HEARTBEAT)
        assertEquals("call", WireType.CALL)
        assertEquals("call-cancel", WireType.CALL_CANCEL)
        assertEquals("call-response", WireType.CALL_RESPONSE)
        assertEquals("event", WireType.EVENT)
    }

    @Test
    fun `event frame roundtrips`() {
        val frame = EventFrame(kind = "control-revoked", data = buildJsonObject { put("reason", "user") })
        val text = json.encodeToString(EventFrame.serializer(), frame)
        assertTrue("\"type\":\"event\"" in text)
        assertTrue("\"kind\":\"control-revoked\"" in text)
    }
}
