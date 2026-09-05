package dev.devicecontrol.app.net

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Test

/**
 * Pairing-code normalization must match the server's `NormalizeCode` exactly, or a code
 * the server would accept gets rejected locally (or vice versa). These pin the
 * uppercasing + non-alphanumeric stripping.
 */
class PairingClientNormalizationTest {
    @Test
    fun `uppercase and strip dashes`() {
        assertEquals("ABCDEFGH", PairingClient.normalizeCode("abcd-efgh"))
    }

    @Test
    fun `already uppercase passes through`() {
        assertEquals("ABCDEFGH", PairingClient.normalizeCode("ABCDEFGH"))
    }

    @Test
    fun `lowercase is uppercased`() {
        assertEquals("ABCDEFGH", PairingClient.normalizeCode("abcdefgh"))
    }

    @Test
    fun `mixed case and separators collapse`() {
        assertEquals("ABCD1234", PairingClient.normalizeCode("aB-cD_12.34"))
    }

    @Test
    fun `spaces are stripped`() {
        assertEquals("ABCD", PairingClient.normalizeCode("  ab cd  "))
    }

    @Test
    fun `empty input yields empty`() {
        assertEquals("", PairingClient.normalizeCode(""))
        assertEquals("", PairingClient.normalizeCode("---"))
    }

    @Test
    fun `unicode letters outside ASCII are dropped`() {
        // Only ASCII A-Z0-9 survive; a Cyrillic 'А' is NOT treated as 'A'.
        assertEquals("", PairingClient.normalizeCode("АБВГ"))
    }
}
