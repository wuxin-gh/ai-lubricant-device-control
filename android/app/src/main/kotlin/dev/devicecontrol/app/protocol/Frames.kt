package dev.devicecontrol.app.protocol

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

const val PROTOCOL_VERSION = 0

object WireType {
    const val REGISTER = "register"
    const val REGISTERED = "registered"
    const val HEARTBEAT = "heartbeat"
    const val CALL = "call"
    const val CALL_CANCEL = "call-cancel"
    const val CALL_RESPONSE = "call-response"
    const val EVENT = "event"
}

@Serializable
data class Auth(
    val scheme: String = "token",
    val token: String,
)

@Serializable
data class RegisterFrame(
    val type: String = WireType.REGISTER,
    @SerialName("protocol_version") val protocolVersion: Int = PROTOCOL_VERSION,
    @SerialName("device_id") val deviceId: String,
    val auth: Auth,
    val capabilities: List<String>,
    @SerialName("device_info") val deviceInfo: JsonObject? = null,
)

@Serializable
data class RegisteredFrame(
    val type: String,
    @SerialName("protocol_version") val protocolVersion: Int,
    @SerialName("device_id") val deviceId: String,
    @SerialName("accepted_capabilities") val acceptedCapabilities: List<String> = emptyList(),
    @SerialName("heartbeat_interval_s") val heartbeatIntervalS: Int = 15,
    @SerialName("heartbeat_timeout_s") val heartbeatTimeoutS: Int = 60,
)

@Serializable
data class HeartbeatFrame(
    val type: String = WireType.HEARTBEAT,
    @SerialName("device_id") val deviceId: String,
    val seq: Long,
)

@Serializable
data class CallFrame(
    val type: String,
    @SerialName("request_id") val requestId: String,
    val cmd: String,
    val args: JsonObject = buildJsonObject {},
    @SerialName("timeout_ms") val timeoutMs: Int = 15_000,
)

@Serializable
data class CallCancelFrame(
    val type: String,
    @SerialName("request_id") val requestId: String,
)

@Serializable
data class WireError(
    val code: String,
    val message: String? = null,
    val retryable: Boolean? = null,
    val details: JsonElement? = null,
)

@Serializable
data class CallResponseFrame(
    val type: String = WireType.CALL_RESPONSE,
    @SerialName("request_id") val requestId: String,
    val ok: Boolean,
    val data: JsonElement? = null,
    val error: WireError? = null,
)

@Serializable
data class EventFrame(
    val type: String = WireType.EVENT,
    val kind: String,
    val data: JsonElement? = null,
)

fun heartbeat(deviceId: String, seq: Long) = HeartbeatFrame(deviceId = deviceId, seq = seq)
