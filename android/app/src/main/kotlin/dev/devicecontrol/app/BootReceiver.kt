package dev.devicecontrol.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import dev.devicecontrol.app.storage.CredentialStore

/**
 * 开机自启：已配对（本地有凭据）就直接拉起前台服务重连服务器，免去手动开 App。
 *
 * 无障碍不需要在这里处理：开关存在系统设置里（持久），开机后系统会按声明自动
 * 绑定服务——前提是进程活着，前台服务先把进程撑起来正好满足。
 *
 * BOOT_COMPLETED 在「从后台启动前台服务」的豁免清单里（targetSdk=34 有效）。
 * 厂商 ROM 若把广播拦了（典型是「自启动」开关没开），这里收不到——MainActivity
 * 里有跳厂商设置的引导。
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        if (CredentialStore(context).load() == null) return
        ControlForegroundService.start(context)
    }
}
