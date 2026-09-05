package dev.devicecontrol.app

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import dev.devicecontrol.app.command.CommandDispatcher
import dev.devicecontrol.app.command.CommandHandlers
import dev.devicecontrol.app.net.ConnectionState
import dev.devicecontrol.app.net.ConnectionStateHolder
import dev.devicecontrol.app.net.PairingClient
import dev.devicecontrol.app.net.WsClient
import dev.devicecontrol.app.storage.CredentialStore
import dev.devicecontrol.app.storage.DeviceCredential
import dev.devicecontrol.app.ui.MainActivity
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient

/**
 * The foreground service that owns the [WsClient] connection loop.
 *
 * Why a foreground service: the device dials the server (NAT traversal), so the
 * connection must outlive the Activity — the user will background the app, the
 * screen will turn off, and Doze would freeze a background process and stall
 * heartbeats → 4010. A foreground service with `foregroundServiceType="specialUse"`
 * (Android 14+) is the contract that keeps us running.
 *
 * The persistent notification is a privacy obligation, not decoration: it says the
 * device is being remotely controlled, so a user who didn't initiate a session
 * sees it. [FOREGROUND_NOTIFICATION_ID] is fixed so the notification is updated in
 * place rather than stacking.
 */
class ControlForegroundService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var wsClient: WsClient? = null
    private var connectJob: Job? = null
    private val credentialStore by lazy { CredentialStore(this) }
    private val okHttpClient by lazy { OkHttpClient.Builder().build() }
    private val pairingClient by lazy { PairingClient(okHttpClient) }

    /** A scope that watches the WsClient state and mirrors it into the holder. */
    private var stateMirrorJob: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(FOREGROUND_NOTIFICATION_ID, buildNotification(getString(R.string.status_connecting)))

        when (intent?.action) {
            ServiceActions.ACTION_PAIR -> {
                val url = intent.getStringExtra(ServiceActions.EXTRA_URL)
                val code = intent.getStringExtra(ServiceActions.EXTRA_CODE)
                if (url == null || code == null) {
                    ConnectionStateHolder.set(ConnectionState.Fatal(reason = "地址或配对码缺失", needsRePair = true))
                } else {
                    pair(url, code)
                }
            }
            ServiceActions.ACTION_UNPAIR -> unpair()
            ServiceActions.ACTION_DISCONNECT -> disconnect()
            else -> {
                // No action (or boot start): (re)start connection loop if we have creds.
                if (connectJob == null || connectJob?.isActive != true) {
                    startConnection()
                }
            }
        }
        return START_STICKY
    }

    private fun startConnection() {
        val credential = credentialStore.load() ?: run {
            ConnectionStateHolder.set(ConnectionState.Idle)
            return
        }
        val app = applicationContext as ControlApplication
        val handlers = CommandHandlers(app.coreGraph, this)
        val dispatcher = CommandDispatcher(handlers.dispatch)
        val client = WsClient(okHttpClient, dispatcher).apply {
            onCredentialWiped = { credentialStore.clear() }
        }
        wsClient = client
        // Mirror the WsClient's state into the holder so the Activity (and the
        // foreground notification) can render it.
        stateMirrorJob?.cancel()
        stateMirrorJob = scope.launch {
            client.state.collect { state ->
                ConnectionStateHolder.set(state)
                updateNotificationForState(state)
            }
        }
        connectJob = scope.launch { client.connectLoop(credential) }
    }

    /**
     * Pairing entry point called from the Activity. Runs the one-shot POST /pair on
     * the IO scope, persists the credential, then starts the connection loop. The
     * result (success / specific failure) is reported via [PairingResult].
     */
    fun pair(serverUrl: String, code: String) {
        scope.launch {
            ConnectionStateHolder.set(ConnectionState.Pairing)
            val result = runCatching { pairingClient.pair(serverUrl, code) }
            result.onSuccess { pairing ->
                credentialStore.save(DeviceCredential(
                    serverUrl = pairing.serverUrl,
                    deviceId = pairing.deviceId,
                    token = pairing.token,
                ))
                // Stop any old loop, then start fresh with the new credential.
                wsClient?.stop(userInitiated = true)
                startConnection()
            }.onFailure { t ->
                val msg = when (t) {
                    is PairingClient.InvalidCodeException -> getString(R.string.pair_invalid_code)
                    is PairingClient.ProtocolMismatchException -> getString(R.string.fatal_version_mismatch)
                    else -> t.message ?: getString(R.string.pair_invalid_code)
                }
                ConnectionStateHolder.set(ConnectionState.Fatal(reason = msg, needsRePair = true))
            }
        }
    }

    /** External stop (from the Activity's "disconnect" button). */
    fun disconnect() {
        wsClient?.stop(userInitiated = true)
    }

    fun unpair() {
        wsClient?.stop(userInitiated = true)
        credentialStore.clear()
        ConnectionStateHolder.set(ConnectionState.Idle)
    }

    private fun updateNotificationForState(state: ConnectionState) {
        val text = when (state) {
            is ConnectionState.Connected -> getString(R.string.status_connected) + " · " + state.deviceId
            ConnectionState.Connecting -> getString(R.string.status_connecting)
            ConnectionState.Pairing -> getString(R.string.status_pairing)
            is ConnectionState.Disconnected -> getString(R.string.status_disconnected)
            is ConnectionState.Fatal -> state.reason
            ConnectionState.Idle -> getString(R.string.status_disconnected)
        }
        val nm = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
        nm.notify(FOREGROUND_NOTIFICATION_ID, buildNotification(text))
    }

    override fun onDestroy() {
        wsClient?.stop(userInitiated = false)
        scope.cancel()
        super.onDestroy()
    }

    private fun buildNotification(text: String): Notification {
        val intent = Intent(this, MainActivity::class.java)
        val pendingFlags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        val contentIntent = PendingIntent.getActivity(this, 0, intent, pendingFlags)

        return Notification.Builder(this, ControlApplication.CHANNEL_ID)
            .setContentTitle(getString(R.string.foreground_service_title))
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setOngoing(true)
            .setContentIntent(contentIntent)
            .build()
    }

    companion object {
        const val FOREGROUND_NOTIFICATION_ID = 4242

        fun start(context: Context) {
            val intent = Intent(context, ControlForegroundService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, ControlForegroundService::class.java))
        }
    }
}
