package dev.devicecontrol.core.accessibility

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Test

class CommandIndicatorFormattingTest {
    @Test
    fun `formats command names for display`() {
        assertEquals("Get screen state", formatCommandName("get_screen_state"))
        assertEquals("Tap", formatCommandName("tap"))
    }
}
