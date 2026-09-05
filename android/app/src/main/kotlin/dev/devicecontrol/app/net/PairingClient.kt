package dev.devicecontrol.app.net

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException

/**
 * Pairs this device with the server: a single unauthenticated `POST {base}/pair` with
 * body `{"code":"..."}`. The server returns `{device_id, token, protocol_version}` and
 * stores only the SHA-256 of the token; this app is the sole plaintext holder.
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
     * @param base the server base URL, e.g. `https://host` 或 `https://host/mcp/device-control`.
     *        A trailing slash is tolerated. 两种填法都支持：先试原样 + `/pair`，
     *        不通再补 `/mcp/device-control/pair`（用户只填域名时也能配对成功）。
     * @param rawCode the pairing code exactly as the user typed it; normalized here.
     */
    fun pair(base: String, rawCode: String): PairingResult {
        val code = normalizeCode(rawCode)
        require(code.isNotEmpty()) { "empty pairing code" }

        val baseClean = base.trimEnd('/')
        // 候选顺序：先试用户原样填的，不行才补 /mcp/device-control 前缀。
        // 这样两种填法都能通，且原样填完整路径时只发一次请求。
        val candidates = listOf(
            "$baseClean/pair",
            "$baseClean/mcp/device-control/pair",
        )

        var lastAddressError: IOException? = null
        candidates.forEachIndexed { idx, url ->
            val request = Request.Builder().url(url).post(body(code)).build()
            try {
                client.newCall(request).execute().use { resp ->
                    if (resp.code == 403) {
                        // 地址是对的，配对码无效/过期/已用。**立刻终止，不再试下一个候选**：
                        // 配对码是单次的，继续试会把已消费的码投给另一个路径，永远 403。
                        throw InvalidCodeException("pairing code invalid, expired, or already used")
                    }
                    if (resp.code == 404 || resp.code == 405) {
                        // 这个路径上没有服务端，试下一个候选。
                        lastAddressError = IOException("HTTP ${resp.code} at $url")
                        return@forEachIndexed
                    }
                    if (!resp.isSuccessful) {
                        lastAddressError = IOException("pairing failed: HTTP ${resp.code}")
                        return@forEachIndexed
                    }
                    val text = resp.body?.string().orEmpty()
                    val reply = json.decodeFromString<PairReply>(text)
                    if (reply.protocolVersion != PROTOCOL_VERSION) {
                        throw ProtocolMismatchException(PROTOCOL_VERSION, reply.protocolVersion)
                    }
                    // 探测中实际生效的 base：第 1 个候选成功是原 base，第 2 个成功补了前缀。
                    val effectiveBase = if (idx == 0) baseClean else "$baseClean/mcp/device-control"
                    return PairingResult(
                        deviceId = reply.deviceId,
                        token = reply.token,
                        serverUrl = effectiveBase,
                    )
                }
            } catch (e: InvalidCodeException) {
                throw e
            } catch (e: ProtocolMismatchException) {
                throw e
            } catch (e: IOException) {
                // 连接异常（DNS/超时/拒绝）：试下一个候选。记录错误，全部失败时报最后一个。
                lastAddressError = e
            }
        }
        throw IOException("未能在该地址找到设备控制服务端", lastAddressError)
    }

    private fun body(code: String) = """{"code":"$code"}""".toRequestBody(JSON)

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
