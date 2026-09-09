package dev.devicecontrol.app

import android.app.AlarmManager
import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import android.os.SystemClock
import dev.devicecontrol.app.command.CommandDispatcher
import dev.devicecontrol.app.command.CommandHandlers
import dev.devicecontrol.app.net.ConnectionState
import dev.devicecontrol.app.net.ConnectionStateHolder
import dev.devicecontrol.app.net.PairingClient
import dev.devicecontrol.app.net.WsClient
import dev.devicecontrol.app.storage.CredentialStore
import dev.devicecontrol.app.storage.DeviceCredential
import dev.devicecontrol.app.ui.MainActivity
import kotlinx.coroutines.CoroutineExceptionHandler
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
    // scope 兜底异常处理器：连接循环/配对循环里的任何未捕获异常只记日志，
    // 不再带崩进程（此前一次网络拨号异常 = 整个 app 崩溃 → START_STICKY 拉起
    // → 重连 → 再崩，即「连接中/重试中循环 + 程序崩溃」的观感）。
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.IO +
            CoroutineExceptionHandler { _, t ->
                android.util.Log.e("DC:Service", "connection task failed", t)
                ConnectionStateHolder.set(ConnectionState.Disconnected(reason = "连接异常，稍后自动重试", willRetry = false))
            },
    )
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
            ServiceActions.ACTION_CONNECT -> startConnection()
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
            android.util.Log.w("DC:Service", "startConnection: no credential → Idle")
            ConnectionStateHolder.set(ConnectionState.Idle)
            return
        }
        android.util.Log.i("DC:Service", "startConnection: launch connectLoop device=${credential.deviceId} serverUrl=${credential.serverUrl}")
        // 丢弃上一条连接循环：不 cancel 的话旧 loop 会和新 loop 并行跑
        // （两个 socket、两份 register，服务端看到「连接成功」的假象但
        // 响应全被旧 loop 抢走——正是「服务端已连接、app 却一直重连」的一景）。
        connectJob?.cancel()
        stateMirrorJob?.cancel()
        val app = applicationContext as ControlApplication
        val handlers = CommandHandlers(app.coreGraph, this)
        val dispatcher = CommandDispatcher(handlers.dispatch)
        val client = WsClient(okHttpClient, dispatcher).apply {
            onCredentialWiped = { credentialStore.clear() }
            // 上报给服务端网页：register 帧带 device_info（版本/系统/机型/无障碍/
            // 上次错误），心跳里探测无障碍开关变化发 device_status 事件。
            deviceInfoProvider = { lastError -> dev.devicecontrol.app.net.DeviceInfo.build(this@ControlForegroundService, lastError) }
            accessibilityStateProvider = { dev.devicecontrol.app.net.DeviceInfo.accessibilityEnabled(this@ControlForegroundService) }
        }
        wsClient = client
        // Mirror the WsClient's state into the holder so the Activity (and the
        // foreground notification) can render it.
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
            // 配对请求随带本机信息（版本/系统/机型/无障碍/内网 IP）：服务端在设备记录里
            // 落 device_info，网页在配对完成那一刻就能显示手机信息——不必等 WS register。
            val deviceInfo = runCatching {
                dev.devicecontrol.app.net.DeviceInfo.build(this@ControlForegroundService, null)
            }.getOrNull()
            android.util.Log.i("DC:Service", "pair begin url=$serverUrl deviceInfo=${deviceInfo != null}")
            val result = runCatching { pairingClient.pair(serverUrl, code, deviceInfo) }
            result.onSuccess { pairing ->
                android.util.Log.i("DC:Service", "pair ok device=${pairing.deviceId} serverUrl=${pairing.serverUrl}")
                credentialStore.save(DeviceCredential(
                    serverUrl = pairing.serverUrl,
                    deviceId = pairing.deviceId,
                    token = pairing.token,
                ))
                // Stop any old loop, then start fresh with the new credential.
                wsClient?.stop(userInitiated = true)
                startConnection()
            }.onFailure { t ->
                android.util.Log.e("DC:Service", "pair failed", t)
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

    /**
     * 用户从最近任务里划掉本 App 时，部分 ROM 连 START_STICKY 都不重启。这里挂
     * 一个一秒后的闹钟把服务拉回来：getForegroundService 保证走 startForegroundService
     * 语义（后台态直接 startService 会抛），setAndAllowWhileIdle 不需要精确闹钟
     * 权限、Doze 下也能触发。
     *
     * 只有本地还有凭据（已配对）才挂闹钟：未配对/已解除配对时拉起来也只会转 Idle
     * 并挂着一条多余的前台通知。
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        if (credentialStore.load() == null) return
        val am = getSystemService(Context.ALARM_SERVICE) as AlarmManager
        val restart = Intent(applicationContext, ControlForegroundService::class.java)
        val pi = PendingIntent.getForegroundService(
            applicationContext,
            4243,
            restart,
            PendingIntent.FLAG_ONE_SHOT or PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        am.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME, SystemClock.elapsedRealtime() + 1000, pi)
        super.onTaskRemoved(rootIntent)
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
