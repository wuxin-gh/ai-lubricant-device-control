package dev.devicecontrol.app.net

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.util.Log
import androidx.core.content.FileProvider
import dev.devicecontrol.app.BuildConfig
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import java.io.File
import java.util.concurrent.TimeUnit

/**
 * In-app update: checks the platform marketplace for a newer device-control APK,
 * downloads it and hands it to the system installer.
 *
 * 数据源是用户所配对服务端的同一个 marketplace（运营方在市场管理页「设备控制 App」
 * 发布 device-control-<version>-android.apk）。检查接口与前端一致：
 * `GET {server-base}/api/v1/marketplace/consumer/device-control-version`。
 *
 * [check] 接收的是配对时存下的服务器地址（纯地址，如 `http://host:port`；旧凭据可能
 * 仍带 `/mcp/device-control` 后缀——HttpUrl.resolve 会丢弃 base 的路径，两种都能拼），
 * 内部用 HttpUrl.resolve 把绝对路径检查端点拼到同一 origin 的站点根，调用方无需自己剥路径。
 *
 * 下载优先走 proxy_download_url（由所配对的服务端代取 GitHub Release，手机直连 GitHub
 * 在国内常超时）；没有再退到 download_url 直链。
 *
 * 版本比较：按点分段比数字，非数字段按字符串比（0.1.10 > 0.1.9，0.2.0 > 0.1.2）。
 */
class UpdateClient(
    private val context: Context,
    private val okHttpClient: OkHttpClient,
) {
    private val json = Json { ignoreUnknownKeys = true }

    /** 检查结果：无更新 / 有更新（附版本与下载地址）。 */
    sealed class UpdateInfo {
        object UpToDate : UpdateInfo()
        data class Available(val version: String, val downloadUrl: String) : UpdateInfo()
    }

    class UpdateCheckException(message: String, cause: Throwable? = null) : Exception(message, cause)

    /** 当前 app 版本（versionName），用于「已是最新版本」提示。 */
    fun currentVersion(): String =
        try {
            context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: BuildConfig.VERSION_NAME
        } catch (_: Exception) {
            BuildConfig.VERSION_NAME
        }

    /**
     * 向 marketplace 查询最新 android 版本。[pairedServerUrl] 是配对时存下的服务器地址
     * （纯地址如 `http://10.0.0.2:11180`；旧凭据带 `/mcp/device-control` 后缀也能用，
     * resolve 按绝对路径拼，base 路径被丢弃），检查端点拼到同一 origin 的站点根。
     */
    suspend fun check(pairedServerUrl: String): UpdateInfo = withContext(Dispatchers.IO) {
        val base = pairedServerUrl.toHttpUrlOrNull()
            ?: throw UpdateCheckException("服务器地址无效")
        val url = base.resolve(CHECK_PATH)
            ?: throw UpdateCheckException("无法拼出检查地址")
        val body = runCatching {
            val response = okHttpClient.newBuilder()
                .callTimeout(20, TimeUnit.SECONDS)
                .build()
                .newCall(Request.Builder().url(url).build())
                .execute()
            response.use { resp ->
                if (!resp.isSuccessful) {
                    throw UpdateCheckException("HTTP ${resp.code}")
                }
                resp.body?.string() ?: throw UpdateCheckException("空响应")
            }
        }.getOrElse { throw UpdateCheckException("检查更新失败", it) }

        val release = runCatching { json.decodeFromString(ReleaseSnapshot.serializer(), body) }
            .getOrElse { throw UpdateCheckException("响应解析失败", it) }
        val android = release.android ?: return@withContext UpdateInfo.UpToDate
        val latest = android.version ?: release.version ?: return@withContext UpdateInfo.UpToDate
        // 优先走服务端代取（手机直连 GitHub 国内常超时），无则退直链。
        // 注意：proxy_download_url 是站内相对路径（/api/v1/marketplace/consumer/...），
        // 必须拼回服务端 origin；download_url 才是 GitHub 绝对直链。
        val raw = android.proxy_download_url ?: android.download_url
            ?: return@withContext UpdateInfo.UpToDate
        val downloadUrl = resolveDownloadUrl(raw, base)
            ?: throw UpdateCheckException("无法解析下载地址")
        if (isNewer(latest, currentVersion())) {
            UpdateInfo.Available(version = latest, downloadUrl = downloadUrl)
        } else {
            UpdateInfo.UpToDate
        }
    }

    /** 相对路径（站内代理）拼回服务端 origin；绝对 URL 原样返回。 */
    private fun resolveDownloadUrl(raw: String, base: okhttp3.HttpUrl): String? {
        if (raw.startsWith("http://") || raw.startsWith("https://")) return raw
        if (raw.startsWith("//")) return base.scheme + ":" + raw
        if (raw.startsWith("/")) {
            return base.newBuilder()
                .encodedPath(raw.substringBefore('?'))
                .query(raw.substringAfter('?', ""))
                .build()
                .toString()
        }
        return null
    }

    /**
     * 下载 APK 到 cacheDir/apk/ 并返回触发系统安装器的 intent。
     * 返回的版本号供 UI 提示。下载走直链（marketplace 发布资产的 download_url）。
     */
    suspend fun downloadAndInstallPrompt(downloadUrl: String): String = withContext(Dispatchers.IO) {
        val dir = File(context.cacheDir, "apk").apply { mkdirs() }
        // 固定文件名：旧包会被覆盖，缓存不累积多版本。
        val target = File(dir, "device-control-update.apk")
        val tmp = File(dir, "device-control-update.apk.part")
        runCatching {
            val client = okHttpClient.newBuilder()
                .callTimeout(15, TimeUnit.MINUTES)
                .build()
            client.newCall(Request.Builder().url(downloadUrl).build()).execute().use { resp ->
                if (!resp.isSuccessful) throw UpdateCheckException("下载失败 HTTP ${resp.code}")
                resp.body?.byteStream()?.use { input ->
                    tmp.outputStream().use { output -> input.copyTo(output) }
                } ?: throw UpdateCheckException("下载响应为空")
            }
            if (tmp.length() == 0L) throw UpdateCheckException("下载内容为空")
            if (target.exists()) target.delete()
            if (!tmp.renameTo(target)) throw UpdateCheckException("缓存写入失败")
        }.getOrElse {
            tmp.delete()
            throw UpdateCheckException("下载失败", it)
        }

        // 共享给系统安装器（跨进程读我们的私有文件必须经 FileProvider）。
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.fileprovider", target)
        val install = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            // 安装器在新任务里打开（我们自己不在 Activity 上下文时也需要）。
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        runCatching {
            context.startActivity(install)
        }.onFailure { Log.e(TAG, "cannot start installer", it) }
        return@withContext target.absolutePath
    }

    /** `0.1.10 > 0.1.9`：按点分段，数字段比数值，非数字段比字符串。 */
    internal fun isNewer(candidate: String, current: String): Boolean {
        val cs = candidate.split('.')
        val cur = current.split('.')
        val n = maxOf(cs.size, cur.size)
        for (i in 0 until n) {
            val a = cs.getOrNull(i).orEmpty()
            val b = cur.getOrNull(i).orEmpty()
            val an = a.toIntOrNull()
            val bn = b.toIntOrNull()
            val cmp = when {
                an != null && bn != null -> an.compareTo(bn)
                else -> a.compareTo(b, ignoreCase = true)
            }
            if (cmp != 0) return cmp > 0
        }
        return false
    }

    /** marketplace 版本快照（前端 DeviceControlAppRelease 同构，仅取需要的字段）。 */
    @Serializable
    private data class ReleaseSnapshot(
        val version: String? = null,
        val android: AssetSnapshot? = null,
    ) {
        @Serializable
        data class AssetSnapshot(
            val version: String? = null,
            val download_url: String? = null,
            val proxy_download_url: String? = null,
        )
    }

    companion object {
        private const val TAG = "DC:Update"
        private const val CHECK_PATH = "/api/v1/marketplace/consumer/device-control-version"
    }
}
