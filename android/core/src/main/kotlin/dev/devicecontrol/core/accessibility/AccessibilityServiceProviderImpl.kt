package dev.devicecontrol.core.accessibility

import android.content.Context
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityWindowInfo
import dev.devicecontrol.core.CoreException
import javax.inject.Inject

/**
 * Production implementation of [AccessibilityServiceProvider] that delegates
 * to [DeviceControlAccessibilityService.instance] (the system-managed singleton).
 *
 * Vendored from ARC-MCP (MIT, see NOTICE). The @Inject annotation is kept for source
 * fidelity; this library ships no DI runtime, so the app constructs it directly.
 */
class AccessibilityServiceProviderImpl
    @Inject
    constructor() : AccessibilityServiceProvider {
        override fun getRootNode(): AccessibilityNodeInfo? = DeviceControlAccessibilityService.instance?.getRootNode()

        override fun getAccessibilityWindows(): List<AccessibilityWindowInfo> =
            DeviceControlAccessibilityService.instance?.getAccessibilityWindows() ?: emptyList()

        override fun getCurrentPackageName(): String? = DeviceControlAccessibilityService.instance?.getCurrentPackageName()

        override fun getCurrentActivityName(): String? = DeviceControlAccessibilityService.instance?.getCurrentActivityName()

        override fun getScreenInfo(): ScreenInfo =
            DeviceControlAccessibilityService.instance?.getScreenInfo()
                ?: throw CoreException.PermissionDenied("Accessibility service not available")

        override fun isReady(): Boolean = DeviceControlAccessibilityService.instance?.isReady() == true

        override fun getContext(): Context? = DeviceControlAccessibilityService.instance

        override fun clearFrameworkNodeCache() {
            DeviceControlAccessibilityService.instance?.clearFrameworkNodeCache()
        }
    }
