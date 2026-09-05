package dev.devicecontrol.core.accessibility

import dev.devicecontrol.core.CommandIndicator

/** Shows a touch-through accessibility overlay while a command is executing. */
class OverlayCommandIndicator : CommandIndicator {
    @Volatile
    private var enabled = true

    override fun onCommandStarted(command: String) {
        if (enabled) DeviceControlAccessibilityService.showCommandIndicator(command)
    }

    override fun onCommandFinished(command: String) {
        DeviceControlAccessibilityService.hideCommandIndicator()
    }

    override fun setEnabled(enabled: Boolean) {
        this.enabled = enabled
        if (!enabled) DeviceControlAccessibilityService.hideCommandIndicator()
    }
}
