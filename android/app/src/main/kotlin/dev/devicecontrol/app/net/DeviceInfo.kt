package dev.devicecontrol.app.net

import android.content.Context
import android.os.Build
import android.provider.Settings
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 上报到服务端的设备信息：版本 / 系统 / 机型 / 无障碍开关 / 上次错误。
 *
 * 服务端网页「设备控制」资源页要显示这些（区分设备、判断无障碍是否就绪、排障），
 * 手机自己不需要展示——所以这里只负责采集，不负责显示。
 *
 * `build()` 返回的 JsonObject：
 * - 用于 register 帧的 `device_info`（每次连接现造，最新鲜）；
 * - 用于 device_status 事件帧的 data（无障碍开关变化时单独上报）。
 */
object DeviceInfo {
    /** 无障碍服务全限定名（core 模块合并进本 app 包名）。 */
    private const val ACCESSIBILITY_SERVICE =
        "dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService"

    /** 本机 app 版本（versionName），上报给服务端便于判断是否需要升级。 */
    fun appVersion(context: Context): String =
        runCatching {
            context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: ""
        }.getOrDefault("")

    /** 无障碍服务（远程控制根）是否已在系统设置里启用。 */
    fun accessibilityEnabled(context: Context): Boolean {
        if (Settings.Secure.getInt(
                context.contentResolver,
                Settings.Secure.ACCESSIBILITY_ENABLED,
                0,
            ) != 1
        ) {
            return false
        }
        val enabled = Settings.Secure.getString(
            context.contentResolver,
            Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES,
        ) ?: return false
        val component = "${context.packageName}/$ACCESSIBILITY_SERVICE"
        return enabled.contains(component)
    }

    /**
     * 完整设备信息快照。[lastError] 非空时附上（服务端显示「上次错误」，便于排障）。
     */
    fun build(context: Context, lastError: String?): JsonObject = buildJsonObject {
        put("app_version", appVersion(context))
        put("app_version_code", runCatching {
            context.packageManager.getPackageInfo(context.packageName, 0).longVersionCode
        }.getOrDefault(0L))
        put("os_version", Build.VERSION.RELEASE ?: "")
        put("os_api", Build.VERSION.SDK_INT)
        put("model", Build.MODEL ?: "")
        put("manufacturer", Build.MANUFACTURER ?: "")
        put("brand", Build.BRAND ?: "")
        put("accessibility_enabled", accessibilityEnabled(context))
        val ips = lanIps()
        if (ips.isNotEmpty()) {
            put("lan_ips", buildJsonArray { ips.forEach { add(it) } })
        }
        if (!lastError.isNullOrEmpty()) {
            put("last_error", lastError)
        }
    }

    /** 仅无障碍开关——心跳里探测到变化时单独发 device_status 事件。 */
    fun accessibilityStatus(context: Context): JsonObject = buildJsonObject {
        put("accessibility_enabled", accessibilityEnabled(context))
    }

    /**
     * 本机局域网 IPv4 列表（排除回环）。无需任何权限：NetworkInterface 枚举不走
     * WiFi/telephony API。上报给服务端网页展示「内网地址」，排障时能定位手机
     * 在哪个网段；手机不在场也能对着 IP 排查。
     */
    fun lanIps(): List<String> {
        val result = mutableListOf<String>()
        runCatching {
            val interfaces = java.net.NetworkInterface.getNetworkInterfaces() ?: return emptyList()
            for (intf in interfaces) {
                if (intf.isLoopback || !intf.isUp) continue
                for (addr in intf.inetAddresses) {
                    if (addr.isLoopbackAddress) continue
                    if (addr is java.net.Inet4Address) {
                        val ip = addr.hostAddress ?: continue
                        result.add(ip)
                    }
                }
            }
        }
        return result
    }
}
