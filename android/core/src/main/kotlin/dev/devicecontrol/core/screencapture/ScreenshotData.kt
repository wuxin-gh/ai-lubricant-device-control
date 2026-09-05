package dev.devicecontrol.core.screencapture

import kotlinx.serialization.Serializable

/**
 * A captured screenshot, ready to be placed in a `call-response` payload.
 *
 * Per `spec/protocol-v0.md` §8.3 the wire format is always JPEG: quality 80, longest
 * side scaled to at most 700px, base64 payload capped at 1.5MiB. [width] and [height]
 * are the dimensions of the *encoded* image after downscaling, which is why they are
 * carried alongside the data — they differ from the values in
 * [dev.devicecontrol.core.accessibility.ScreenInfo], and node bounds are expressed in
 * full-resolution screen pixels, not in these.
 *
 * @property format The image encoding format (always "jpeg").
 * @property data The base64-encoded JPEG image data.
 * @property width The encoded screenshot width in pixels.
 * @property height The encoded screenshot height in pixels.
 */
@Serializable
data class ScreenshotData(
    val format: String = FORMAT_JPEG,
    val data: String,
    val width: Int,
    val height: Int,
) {
    companion object {
        const val FORMAT_JPEG = "jpeg"
    }
}
