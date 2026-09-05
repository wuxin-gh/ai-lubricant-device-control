package dev.devicecontrol.app.protocol

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/** Pins platform-dependent capability negotiation separately from the static command vocabulary. */
class CapabilitiesTest {
    @Test
    fun `API 33 and newer advertise all implemented commands`() {
        assertEquals(Capabilities.ALL, Capabilities.forApiLevel(33))
        assertEquals(Capabilities.ALL, Capabilities.forApiLevel(34))
    }

    @Test
    fun `API 31 and 32 omit only the API 33 accessibility IME command`() {
        val expected = Capabilities.ALL - "type_text"
        assertEquals(expected, Capabilities.forApiLevel(31))
        assertEquals(expected, Capabilities.forApiLevel(32))
        assertFalse("type_text" in Capabilities.forApiLevel(31))
    }

    @Test
    fun `API 31 retains non-IME key and navigation capabilities`() {
        val actual = Capabilities.forApiLevel(31)
        assertTrue("press_key" in actual)
        assertTrue("press_back" in actual)
        assertTrue("press_home" in actual)
        assertTrue("press_recents" in actual)
        assertTrue("set_text" in actual)
    }

    @Test
    fun `ALL contains the complete v0 command vocabulary exactly once`() {
        val expected = setOf(
            "get_screen_state", "tap", "long_press", "double_tap", "swipe", "scroll",
            "scroll_to_node", "type_text", "set_text", "press_key", "dismiss_keyboard",
            "press_back", "press_home", "press_recents", "open_app", "list_apps",
        )
        assertEquals(expected, Capabilities.ALL.toSet())
        assertEquals(expected.size, Capabilities.ALL.size)
    }
}
