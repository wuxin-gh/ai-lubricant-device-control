package dev.devicecontrol.core

/**
 * Surfaces command activity to the device user without exposing arguments or results.
 * Implementations must be non-blocking so they never delay a command.
 *
 * Vendored from ARC-MCP's `ToolCallIndicator` (MIT, see NOTICE), renamed because this
 * project dispatches protocol commands rather than MCP tool calls. Showing the device
 * user that something is driving their phone is a privacy requirement, not a nicety —
 * `core/` keeps the hook and the app decides how to render it.
 */
interface CommandIndicator {
    fun onCommandStarted(command: String)

    fun onCommandFinished(command: String)

    fun setEnabled(enabled: Boolean) = Unit

    companion object {
        val NONE: CommandIndicator =
            object : CommandIndicator {
                override fun onCommandStarted(command: String) = Unit

                override fun onCommandFinished(command: String) = Unit
            }
    }
}
