package dev.devicecontrol.app

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build
import dev.devicecontrol.app.storage.CredentialStore
import dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService

/**
 * The app entry point. Two responsibilities, both must happen before anything tries
 * to use the connection or the accessibility service:
 *
 *  1. Create the one [CoreGraph] and wire the accessibility service's static
 *     [DeviceControlAccessibilityService.nodeCacheProvider] hook to it. That hook is
 *     read exactly once, in the service's `onServiceConnected()` (core source
 *     `DeviceControlAccessibilityService.kt:327-335`), so it must be set here in
 *     `onCreate()` — before the system can bind the service. The same
 *     [AccessibilityNodeCacheImpl] instance is shared between [CoreGraph.actionExecutor]
 *     and the service; two instances would mean the cache never invalidates and
 *     node_id lookups would silently target stale trees (tap the wrong element).
 *
 *  2. Create the notification channel the foreground service posts to.
 *
 * No default server address is ever written here (PLAN.md §1): the address comes
 * solely from the user's pairing flow, persisted in [CredentialStore].
 */
class ControlApplication : Application() {
    lateinit var coreGraph: CoreGraph
        private set

    override fun onCreate() {
        super.onCreate()
        coreGraph = CoreGraph(this)
        // The accessibility service reads this hook once when it connects. Set it
        // now so the cache is ready before any call can arrive.
        DeviceControlAccessibilityService.nodeCacheProvider = { coreGraph.nodeCache }
        createNotificationChannel()
    }

    private fun createNotificationChannel() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        // IMPORTANCE_LOW: the "being remotely controlled" notice is a persistent
        // privacy indicator, not an attention-grabbing alert. It must stay visible
        // while control is active, but it shouldn't beep on every frame.
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notification_channel_name),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = getString(R.string.foreground_service_text)
            setShowBadge(false)
        }
        nm.createNotificationChannel(channel)
    }

    companion object {
        const val CHANNEL_ID = "device_control_connection"
    }
}
