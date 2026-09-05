package dev.devicecontrol.app.net

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Process-wide bridge for [ConnectionState] between the foreground service (which owns
 * the [WsClient]) and the UI (which has no handle to the service object).
 *
 * The service is the sole writer; the Activity only collects. This is deliberately a
 * tiny singleton rather than a bound-service IPC dance — the state is one enum-ish
 * value, and binding for a status display is more machinery than the data warrants.
 */
object ConnectionStateHolder {
    private val _state = MutableStateFlow<ConnectionState>(ConnectionState.Idle)
    val state: StateFlow<ConnectionState> = _state.asStateFlow()

    fun set(state: ConnectionState) {
        _state.value = state
    }
}
