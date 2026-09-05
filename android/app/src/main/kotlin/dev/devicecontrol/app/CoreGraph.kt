package dev.devicecontrol.app

import android.content.Context
import dev.devicecontrol.core.accessibility.AccessibilityNodeCacheImpl
import dev.devicecontrol.core.accessibility.AccessibilityServiceProviderImpl
import dev.devicecontrol.core.accessibility.AccessibilityTreeParser
import dev.devicecontrol.core.accessibility.CompactTreeFormatter
import dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService
import dev.devicecontrol.core.accessibility.ElementFinder
import dev.devicecontrol.core.accessibility.ScreenReader
import dev.devicecontrol.core.accessibility.ScreenStateSnapshotCacheImpl
import dev.devicecontrol.core.accessibility.TypeInputControllerImpl
import dev.devicecontrol.core.accessibility.WebViewNodeMerger
import dev.devicecontrol.core.accessibility.ActionExecutorImpl
import dev.devicecontrol.core.screencapture.ApiLevelProvider
import dev.devicecontrol.core.screencapture.DefaultApiLevelProvider
import dev.devicecontrol.core.screencapture.ScreenCaptureProviderImpl
import dev.devicecontrol.core.screencapture.ScreenshotEncoder

class CoreGraph(context: Context) {
    val nodeCache = AccessibilityNodeCacheImpl()
    private val provider = AccessibilityServiceProviderImpl()
    private val parser = AccessibilityTreeParser()
    private val formatter = CompactTreeFormatter()
    private val merger = WebViewNodeMerger()
    private val snapshotCache = ScreenStateSnapshotCacheImpl()
    val screenReader = ScreenReader(provider, parser, nodeCache, merger, formatter, snapshotCache)
    val elementFinder = ElementFinder()
    val actionExecutor = ActionExecutorImpl(nodeCache, parser)
    val typeInputController = TypeInputControllerImpl()
    private val apiLevelProvider: ApiLevelProvider = DefaultApiLevelProvider()
    val screenCapture = ScreenCaptureProviderImpl(ScreenshotEncoder(), apiLevelProvider, provider)

    init {
        DeviceControlAccessibilityService.nodeCacheProvider = { nodeCache }
    }
}
