package dev.devicecontrol.app

/**
 * Intent action constants and extras shared between [MainActivity] and
 * [ControlForegroundService]. In a top-level object so neither has to import the
 * other's companion just to reference a string constant.
 */
object ServiceActions {
    const val ACTION_PAIR = "dev.devicecontrol.action.PAIR"
    const val ACTION_UNPAIR = "dev.devicecontrol.action.UNPAIR"
    const val ACTION_DISCONNECT = "dev.devicecontrol.action.DISCONNECT"
    const val EXTRA_URL = "url"
    const val EXTRA_CODE = "code"
}
