package dev.devicecontrol.core.accessibility

import kotlinx.serialization.Serializable

/**
 * Represents the device screen dimensions, density, and orientation.
 *
 * Rendered into the `screen:<w>x<h> density:<dpi> orientation:<o>` header line of the
 * compact tree (`spec/protocol-v0.md` §8.1). [densityDpi] is an integer DPI, not a
 * scaling factor — the wire format says `density:420`, never `density:2.625`.
 *
 * @property width Screen width in pixels.
 * @property height Screen height in pixels.
 * @property densityDpi Screen density in dots per inch.
 * @property orientation Screen orientation: "portrait" or "landscape".
 */
@Serializable
data class ScreenInfo(
    val width: Int,
    val height: Int,
    val densityDpi: Int,
    val orientation: String,
) {
    companion object {
        const val ORIENTATION_PORTRAIT = "portrait"
        const val ORIENTATION_LANDSCAPE = "landscape"
    }
}
