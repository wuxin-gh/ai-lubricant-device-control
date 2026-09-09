package dev.devicecontrol.app.net

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
/**
 * Pairs this device with the server: a single unauthenticated `POST {base}/mcp/device-control/pair`
 * with body `{"code":"...", "device_info":{...}}`. The server returns
 * `{device_id, token, protocol_version}` and stores only the SHA-256 of the token; this app
 * is the sole plaintext holder.
 *
 * 路径契约（2026-09-06 重构）：用户**只填服务器地址**（host:port 或 http(s)://host:port），
 * 请求路径 `/mcp/device-control/pair` 由 app 固定拼接；WS 拨号路径
 * `/mcp/device-control/ws/device` 由 [WsClient.wsUrl] 同样固定拼接。这样用户无需关心路径，
 * 也不会因路径填错（多一截 / 少一截 / 大小写）而连不上——之前的「永远连接中」怀疑
 * 就是路径不确定导致的。保存的 [PairingResult.serverUrl] 是纯地址（不带路径），WS 拨号
 * 时再拼。
 *
 * 兼容旧填法：若用户仍贴了 `/mcp/device-control` 后缀，先剥掉再拼，不报错。
 *
 * The pairing code is normalized locally to match the server's `NormalizeCode`
 * (`store.go:148-156`): uppercase, strip everything outside `[A-Z0-9]`. The server's
 * alphabet deliberately excludes confusable letters I, O and digits 0, 1, so a code
 * like `ABCD-EFGH` and `abcdefgh` both arrive as `ABCDEFGH`. We normalize the same way
 * so a typo in case or dash placement still pairs — and so we never send a code the
 * server would reject for shape reasons.
 *
 * 403 is the server's deliberately-indistinguishable reply for not-found / expired /
 * already-used / replayed (`httpapi.go:99-103`): codes are single-use, TTL 10 min, and
 * wiped on server restart. We surface one unified message for all of them.
 */
class PairingClient(
    private val client: OkHttpClient,
    private val json: Json = Json { ignoreUnknownKeys = true },
) {
    /** Thrown when the server returns 403 — code invalid/expired/used. */
    class InvalidCodeException(message: String) : IOException(message)

    /** Thrown when the server speaks a protocol version we don't support. */
    class ProtocolMismatchException(expected: Int, actual: Int) :
        IOException("protocol version mismatch: expected $expected, got $actual")

    /**
     * @param base 服务器地址，如 `http://host:8001`。用户只填地址；请求路径由本方法
     *        固定拼 `/mcp/device-control/pair`。兼容旧填法：若仍带 `/mcp/device-control`
     *        后缀会被剥掉。trailing slash 也容忍。
     * @param rawCode 配对码，原样传入，此处归一化。
     * @param deviceInfo 配对时一并上报的本机信息（版本/系统/机型/无障碍/内网 IP）。服务端
     *        落到设备记录的 device_info——这样即便 WS 还没 register 上来，服务端网页已能
     *        展示手机信息（正是「永远连接中」排障要看到的东西）。
     */
    fun pair(base: String, rawCode: String, deviceInfo: JsonObject? = null): PairingResult {
        val code = normalizeCode(rawCode)
        require(code.isNotEmpty()) { "empty pairing code" }

        // 用户只填地址：剥掉可能残留的 /mcp/device-control 后缀，再固定拼请求路径。
        val origin = base.trimEnd('/').removeSuffix("/mcp/device-control").trimEnd('/')
        val url = "$origin/mcp/device-control/pair"

        val request = Request.Builder().url(url).post(body(code, deviceInfo).toRequestBody(JSON)).build()
        client.newCall(request).execute().use { resp ->
            if (resp.code == 403) {
                // 配对码无效/过期/已用——单次使用，不要重试。
                throw InvalidCodeException("pairing code invalid, expired, or already used")
            }
            if (!resp.isSuccessful) {
                throw IOException("pairing failed: HTTP ${resp.code} at $url")
            }
            val text = resp.body?.string().orEmpty()
            val reply = json.decodeFromString<PairReply>(text)
            if (reply.protocolVersion != PROTOCOL_VERSION) {
                throw ProtocolMismatchException(PROTOCOL_VERSION, reply.protocolVersion)
            }
            return PairingResult(
                deviceId = reply.deviceId,
                token = reply.token,
                serverUrl = origin,   // 纯地址，不带路径；WS 拨号时由 wsUrl 再拼
            )
        }
    }

    private fun body(code: String, deviceInfo: JsonObject?): String {
        val obj = buildJsonObject {
            put("code", code)
            if (deviceInfo != null) put("device_info", deviceInfo)
        }
        return obj.toString()
    }

    companion object {
        const val PROTOCOL_VERSION = 0
        private val JSON = "application/json; charset=utf-8".toMediaType()

        /**
         * Server-side `NormalizeCode`: uppercase, keep only `[A-Z0-9]`. The alphabet
         * excludes I/O/0/1, but we don't enforce that here — the server will reject
         * any code containing those as "not found" (403), and we surface the same
         * unified message. Stripping dashes and uppercasing is the part that
         * materially improves the user's chance of pairing on a first try.
         */
        fun normalizeCode(raw: String): String =
            raw.uppercase().filter { it in 'A'..'Z' || it in '0'..'9' }
    }
}

@Serializable
private data class PairReply(
    @SerialName("device_id") val deviceId: String,
    val token: String,
    @SerialName("protocol_version") val protocolVersion: Int,
)

/** The persisted result of a successful pairing. */
data class PairingResult(
    val serverUrl: String,
    val deviceId: String,
    val token: String,
)
