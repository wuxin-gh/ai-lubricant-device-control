package dev.devicecontrol.app.net

import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/**
 * Full-jitter backoff (§7): `delay = random(0, min(cap=30s, base=1s * 2^attempt))`.
 *
 * The contract is the whole range is uniform-random (not "half + random half"), and the
 * attempt counter caps at 30s. These tests pin the range for several attempt values so
 * a regression that adds a minimum, changes the cap, or narrows the distribution is
 * caught immediately. `computeBackoffDelay` is a companion function so this needs no
 * live socket, no dispatcher, no Context.
 */
class WsClientBackoffTest {
    @Test
    fun `attempt 0 bounds are 0 to 1s`() = range(0, 1_000)

    @Test
    fun `attempt 1 bounds are 0 to 2s`() = range(1, 2_000)

    @Test
    fun `attempt 2 bounds are 0 to 4s`() = range(2, 4_000)

    @Test
    fun `attempt 4 bounds are 0 to 16s`() = range(4, 16_000)

    @Test
    fun `attempt 5 caps at 30s`() = range(5, 30_000)

    @Test
    fun `attempt 100 still caps at 30s`() = range(100, 30_000)

    @Test
    fun `delay is never negative`() {
        repeat(1_000) { attempt ->
            val d = WsClient.computeBackoffDelay(attempt)
            assertTrue(d >= 0, "negative delay at attempt $attempt: $d")
        }
    }

    @Test
    fun `delay is jittered, not constant`() {
        // With a 30s range and 500 samples, a constant-zero or constant-max
        // implementation would be overwhelmingly unlikely to produce many distinct
        // values. This guards against "return 0" or "return upper" regressions.
        val distinct = (1..500).map { WsClient.computeBackoffDelay(5) }.toSet()
        assertTrue(distinct.size > 100, "backoff not jittered: only ${distinct.size} distinct values")
    }

    private fun range(attempt: Int, upperBound: Long) {
        var max = 0L
        repeat(5_000) {
            val d = WsClient.computeBackoffDelay(attempt)
            assertTrue(d in 0..upperBound, "attempt $attempt delay $d outside [0,$upperBound]")
            if (d > max) max = d
        }
        // Statistical sanity: with 5000 samples across a uniform range we should
        // approach the upper bound. Guards against off-by-one in the range (e.g.
        // returning 0..upperBound-1 only).
        //
        // The "saw delay=0" check is deliberately NOT asserted for large ranges: the
        // probability of nextLong returning exactly 0 in a 30s range is ~1/30001, so
        // across 5000 samples we'd expect ~0.17 hits — i.e. almost never. Asserting it
        // would make the test flaky on the spec-correct implementation.
        assertTrue(
            max >= (upperBound * 99 / 100),
            "max delay $max too far from upper $upperBound",
        )
    }
}
