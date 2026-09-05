package dev.devicecontrol.core.screencapture

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.util.Log
import dev.devicecontrol.core.CoreException
import dev.devicecontrol.core.accessibility.AccessibilityServiceProvider
import dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService
import javax.inject.Inject

/**
 * Production implementation of [ScreenCaptureProvider] that uses
 * AccessibilityService.takeScreenshot() for capturing screenshots.
 *
 * This approach does NOT require user consent or additional permissions beyond
 * the accessibility service being enabled with canTakeScreenshot="true".
 *
 * Vendored from ARC-MCP (MIT, see NOTICE); `McpToolException` became [CoreException]
 * and the service class was renamed. Stateless.
 */
class ScreenCaptureProviderImpl
    @Inject
    constructor(
        private val screenshotEncoder: ScreenshotEncoder,
        private val apiLevelProvider: ApiLevelProvider,
        private val accessibilityServiceProvider: AccessibilityServiceProvider,
    ) : ScreenCaptureProvider {
        private sealed class ServiceValidation {
            data class Valid(
                val service: DeviceControlAccessibilityService,
            ) : ServiceValidation()

            data class Invalid(
                val error: CoreException,
            ) : ServiceValidation()
        }

        @Suppress("ReturnCount")
        override suspend fun captureScreenshot(
            quality: Int,
            maxWidth: Int?,
            maxHeight: Int?,
        ): Result<ScreenshotData> {
            val validation = validateService()
            if (validation is ServiceValidation.Invalid) {
                return Result.failure(validation.error)
            }
            val service = (validation as ServiceValidation.Valid).service

            val bitmap =
                service.takeScreenshotBitmap()
                    ?: return Result.failure(
                        CoreException.ActionFailed("Screenshot capture failed or timed out"),
                    )

            var resizedBitmap: Bitmap? = null
            return try {
                resizedBitmap = screenshotEncoder.resizeBitmapProportional(bitmap, maxWidth, maxHeight)
                val screenshotData = screenshotEncoder.bitmapToScreenshotData(resizedBitmap, quality)
                Result.success(screenshotData)
            } finally {
                // resizeBitmapProportional returns the SAME bitmap if no resize needed,
                // or a NEW bitmap if resized. Recycle the resized one first (if different),
                // then always recycle the original.
                if (resizedBitmap != null && resizedBitmap !== bitmap) {
                    resizedBitmap.recycle()
                }
                bitmap.recycle()
            }
        }

        @SuppressLint("NewApi")
        @Suppress("ReturnCount", "TooGenericExceptionCaught")
        override suspend fun captureScreenshotBitmap(
            maxWidth: Int?,
            maxHeight: Int?,
        ): Result<Bitmap> {
            val validation = validateService()
            if (validation is ServiceValidation.Invalid) {
                return Result.failure(validation.error)
            }
            val service = (validation as ServiceValidation.Valid).service

            val bitmap =
                service.takeScreenshotBitmap()
                    ?: return Result.failure(
                        CoreException.ActionFailed("Screenshot capture failed or timed out"),
                    )

            return try {
                val resizedBitmap = screenshotEncoder.resizeBitmapProportional(bitmap, maxWidth, maxHeight)
                // If resize produced a new bitmap, recycle the original
                if (resizedBitmap !== bitmap) {
                    bitmap.recycle()
                }
                Result.success(resizedBitmap)
            } catch (e: Exception) {
                // Ensure original bitmap is recycled even if resize throws
                bitmap.recycle()
                Log.e(TAG, "Screenshot resize failed", e)
                Result.failure(
                    CoreException.ActionFailed("Screenshot resize failed"),
                )
            }
        }

        @Suppress("ReturnCount")
        private fun validateService(): ServiceValidation {
            if (apiLevelProvider.getSdkInt() < API_LEVEL_R) {
                return ServiceValidation.Invalid(
                    CoreException.PermissionDenied(
                        "Screenshot capture requires Android 11 (API 30) or higher",
                    ),
                )
            }
            if (!accessibilityServiceProvider.isReady()) {
                return ServiceValidation.Invalid(
                    CoreException.PermissionDenied(
                        "Accessibility service not enabled. Please enable it in Android Settings.",
                    ),
                )
            }
            val service =
                accessibilityServiceProvider.getContext() as? DeviceControlAccessibilityService
                    ?: return ServiceValidation.Invalid(
                        CoreException.PermissionDenied(
                            "Accessibility service not enabled. Please enable it in Android Settings.",
                        ),
                    )
            if (!service.canTakeScreenshot()) {
                return ServiceValidation.Invalid(
                    CoreException.PermissionDenied(
                        "Screenshot capability not available on this device",
                    ),
                )
            }
            return ServiceValidation.Valid(service)
        }

        @Suppress("ReturnCount")
        override fun isScreenCaptureAvailable(): Boolean {
            if (!accessibilityServiceProvider.isReady()) return false
            val service =
                accessibilityServiceProvider.getContext() as? DeviceControlAccessibilityService
                    ?: return false
            return apiLevelProvider.getSdkInt() >= API_LEVEL_R && service.canTakeScreenshot()
        }

        companion object {
            private const val TAG = "DC:ScreenCapture"

            /** Android 11 (API 30) — minimum for AccessibilityService.takeScreenshot(). */
            private const val API_LEVEL_R = 30
        }
    }
