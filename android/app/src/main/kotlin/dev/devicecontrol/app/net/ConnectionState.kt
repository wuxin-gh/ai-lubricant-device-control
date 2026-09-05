package dev.devicecontrol.app.net

/**
 * The connection lifecycle the UI renders. Kept as a sealed class (not an enum)
 * because several states carry data: which device is connected, or why we stopped.
 *
 * The split between [Disconnected] and [Fatal] matters: [Disconnected] means "we will
 * keep retrying with backoff" (network blip, replaced, heartbeat timeout), while
 * [Fatal] means "stop retrying entirely" — the token is dead (4003, wiped) or the
 * protocol version is wrong (4004). The UI shows a re-pair prompt only for [Fatal].
 */
sealed class ConnectionState {
    /** Service not started, or no credentials saved yet. */
    object Idle : ConnectionState()

    /** POST /pair in flight. */
    object Pairing : ConnectionState()

    /** WebSocket connecting or waiting for the `registered` frame. */
    object Connecting : ConnectionState()

    /** `registered` received; heartbeats flowing, calls may arrive. */
    data class Connected(val deviceId: String) : ConnectionState()

    /**
     * Connection dropped but we are (or will be) retrying with backoff.
     * [reason] is human-readable; [willRetry] is false only when the user asked to stop.
     */
    data class Disconnected(val reason: String, val willRetry: Boolean = true) : ConnectionState()

    /**
     * Permanently stopped — do not reconnect. [reason] explains why; [needsRePair]
     * is true when the credential was wiped (4003) and the user must pair again.
     */
    data class Fatal(val reason: String, val needsRePair: Boolean) : ConnectionState()
}
