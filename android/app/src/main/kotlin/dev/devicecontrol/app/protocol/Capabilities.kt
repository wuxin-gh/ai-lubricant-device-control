package dev.devicecontrol.app.protocol

/**
 * The command set this device declares at registration. The server gates every inbound `call`
 * against this list (`hub.Supports`), so undeclared commands never reach the wire — over-declaring
 * is the bug, under-declaring is safe.
 *
 * This matches the spec §8 vocabulary and exactly the commands the app implements in
 * [dev.devicecontrol.app.command.CommandDispatcher]. Keeping it as one list, not scattered
 * strings, prevents drift between what is registered and what is handled.
 */
object Capabilities {
    /**
     * What this build implements. Not what a given device can do — see [forApiLevel].
     */
    val ALL: List<String> =
        listOf(
            "get_screen_state",
            "tap",
            "long_press",
            "double_tap",
            "swipe",
            "scroll",
            "scroll_to_node",
            "type_text",
            "set_text",
            "press_key",
            "dismiss_keyboard",
            "press_back",
            "press_home",
            "press_recents",
            "open_app",
            "list_apps",
        )

    /**
     * Commands that need the accessibility IME (`AccessibilityService.onCreateInputMethod`,
     * API 33+). On older platforms `TypeInputController.isReady()` can never become true, so
     * declaring these would only buy the caller a round-trip to learn that.
     */
    private val REQUIRES_ACCESSIBILITY_IME = setOf("type_text")

    /** API level at which the accessibility IME became available. */
    const val MIN_SDK_FOR_IME = 33

    /**
     * What THIS device can actually do, as opposed to what the app implements ([ALL]).
     *
     * Under-declaring is safe: the server gates every `call` against this list
     * (`hub.Supports`), so a command left out here never reaches the wire. Over-declaring is
     * the bug — it makes the caller discover the gap only after a failed command.
     *
     * `press_key` stays declared below API 33 because `back`/`home` route through global
     * actions rather than the IME; the remaining keys report `unsupported`. Protocol v0 has
     * no way to say "partially supported", and those two keys are the common case.
     */
    fun forApiLevel(sdkInt: Int): List<String> =
        if (sdkInt >= MIN_SDK_FOR_IME) ALL else ALL.filterNot { it in REQUIRES_ACCESSIBILITY_IME }
}
