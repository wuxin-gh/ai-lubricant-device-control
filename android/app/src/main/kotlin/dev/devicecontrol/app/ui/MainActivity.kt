package dev.devicecontrol.app.ui

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.text.Editable
import android.text.TextWatcher
import android.view.KeyEvent
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import dev.devicecontrol.app.ControlForegroundService
import dev.devicecontrol.app.R
import dev.devicecontrol.app.ServiceActions
import dev.devicecontrol.app.net.ConnectionState
import dev.devicecontrol.app.net.ConnectionStateHolder
import dev.devicecontrol.app.net.DeviceInfo
import dev.devicecontrol.app.net.PairingClient
import dev.devicecontrol.app.net.UpdateClient
import dev.devicecontrol.app.storage.CredentialStore
import dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch

/**
 * Entry point UI for device-control.
 *
 * State machine:
 *  - No credential → show address + code + Pair button (pairing_form).
 *  - Have credential → show device_id + status + Disconnect/Unpair buttons (connected_view).
 *
 * Accessibility: before any action, the user must enable the device-control accessibility
 * service. The Activity checks `Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES` for the
 * service's component. If absent, the Open Settings button is enabled and the rest of
 * the UI is dimmed.
 *
 * Communication with the service: we don't bind; instead we send an Intent with extras
 * to `startService`. State comes back through [ConnectionStateHolder] (a process-global
 * singleton) so the Activity renders real-time status without bound-service IPC.
 *
 * Plan.md §1: the server address field defaults to EMPTY. No default endpoint is ever
 * bundled.
 */
class MainActivity : AppCompatActivity() {
    private lateinit var accessibilityStatus: TextView
    private lateinit var openAccessibilityBtn: Button
    private lateinit var batteryStatus: TextView
    private lateinit var batteryBtn: Button
    private lateinit var oemBgBtn: Button
    private lateinit var pairingForm: View
    private lateinit var serverUrlEdit: EditText
    private lateinit var localInfo: TextView
    private lateinit var codeBoxes: Array<EditText>
    private lateinit var pairBtn: Button
    private lateinit var connectedView: View
    private lateinit var deviceIdText: TextView
    private lateinit var connectionStatus: TextView
    private lateinit var checkUpdateBtn: Button
    private lateinit var disconnectBtn: Button
    private lateinit var unpairBtn: Button

    private val credentialStore by lazy { CredentialStore(this) }
    private val okHttpClient by lazy { okhttp3.OkHttpClient() }
    private val updateClient by lazy { UpdateClient(this, okHttpClient) }
    private var justPaired = false

    /** 最近一次渲染的连接状态：断开/连接按钮据此在「断开」与「连接」间切换。 */
    private var lastState: ConnectionState = ConnectionState.Idle

    /** 8 格配对码输入框的 id（XXXX-XXXX 的 8 个字符位）。 */
    private val pairCodeIds = intArrayOf(
        R.id.pair_code_0, R.id.pair_code_1, R.id.pair_code_2, R.id.pair_code_3,
        R.id.pair_code_4, R.id.pair_code_5, R.id.pair_code_6, R.id.pair_code_7,
    )

    /** 程序化改写格子内容时挂起 watcher，避免焦点跳转/格式化递归。 */
    private var editingBoxes = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        accessibilityStatus = findViewById(R.id.accessibility_status)
        openAccessibilityBtn = findViewById(R.id.open_accessibility_btn)
        batteryStatus = findViewById(R.id.battery_status)
        batteryBtn = findViewById(R.id.battery_btn)
        oemBgBtn = findViewById(R.id.oem_background_btn)
        pairingForm = findViewById(R.id.pairing_form)
        serverUrlEdit = findViewById(R.id.server_url)
        localInfo = findViewById(R.id.local_info)
        codeBoxes = Array(8) { findViewById<EditText>(pairCodeIds[it]) }
        pairBtn = findViewById(R.id.pair_btn)
        connectedView = findViewById(R.id.connected_view)
        deviceIdText = findViewById(R.id.device_id)
        connectionStatus = findViewById(R.id.connection_status)
        checkUpdateBtn = findViewById(R.id.check_update_btn)
        disconnectBtn = findViewById(R.id.disconnect_btn)
        unpairBtn = findViewById(R.id.unpair_btn)

        // 标题旁的版本号：直接读已安装包的 versionName，永远反映本机当前装的版本。
        findViewById<TextView>(R.id.app_version).text = runCatching {
            "v" + packageManager.getPackageInfo(packageName, 0).versionName
        }.getOrDefault("")

        setupAccessibilityUi()
        setupPairingForm()
        renderLocalInfo()
        updateUiForCredentialState()

        // Live-refresh accessibility + battery status while resumed. The user may
        // toggle the a11y switch in system settings (or the ROM may kill/rebind the
        // service) with no lifecycle callback to us, and the one-shot check in
        // onResume doesn't catch "开关开着但服务进程被清理" — it'd falsely show
        // "未启用". Polling both the Settings declaration AND the live service
        // singleton surfaces the real state within ~1.5s.
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.RESUMED) {
                while (true) {
                    setupAccessibilityUi()
                    renderBatteryUi()
                    delay(1500)
                }
            }
        }

        // Observe connection state and reflect it in the UI.
        lifecycleScope.launch {
            ConnectionStateHolder.state.collect { state ->
                renderState(state)
            }
        }

        openAccessibilityBtn.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }

        batteryBtn.setOnClickListener {
            // Direct system dialog first (needs REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
            // some ROMs swallow the dialog — fall back to the whitelist page.
            try {
                startActivity(
                    Intent(
                        Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                        Uri.parse("package:$packageName"),
                    ),
                )
            } catch (e: Exception) {
                startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
            }
        }

        oemBgBtn.setOnClickListener { openOemBackgroundSettings() }

        pairBtn.setOnClickListener {
            val url = serverUrlEdit.text.toString().trim()
            // 8 格各收 1 字符，拼出 8 位码；带不带横线服务端都会归一化。
            val code = codeString()
            if (url.isEmpty() || code.length < 8) {
                Toast.makeText(this, "请填写地址和配对码", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            // Ensure the foreground service is up, then send the pair command.
            ControlForegroundService.start(this)
            val intent = Intent(this, ControlForegroundService::class.java).apply {
                action = ServiceActions.ACTION_PAIR
                putExtra(ServiceActions.EXTRA_URL, url)
                putExtra(ServiceActions.EXTRA_CODE, code)
            }
            startService(intent)
            Toast.makeText(this, "正在配对…", Toast.LENGTH_SHORT).show()
        }

        disconnectBtn.setOnClickListener {
            val action = when (lastState) {
                // 未在连接（循环已停）：按钮此刻是「连接」，让它重新拨号。
                ConnectionState.Idle -> ServiceActions.ACTION_CONNECT
                else -> ServiceActions.ACTION_DISCONNECT
            }
            val intent = Intent(this, ControlForegroundService::class.java).apply {
                this.action = action
            }
            startService(intent)
        }

        checkUpdateBtn.setOnClickListener {
            // 任何时候点击都走我们的更新逻辑：已配对→查 marketplace 最新版→下载→系统安装；
            // 未配对→提示先填地址完成配对（更新接口与配对地址同源）。
            val cred = credentialStore.load()
            if (cred == null) {
                Toast.makeText(this, R.string.update_check_no_server, Toast.LENGTH_LONG).show()
                return@setOnClickListener
            }
            checkUpdateBtn.isEnabled = false
            Toast.makeText(this, R.string.update_checking, Toast.LENGTH_SHORT).show()
            lifecycleScope.launch {
                try {
                    when (val info = updateClient.check(cred.serverUrl)) {
                        is UpdateClient.UpdateInfo.UpToDate ->
                            Toast.makeText(
                                this@MainActivity,
                                getString(R.string.update_up_to_date, updateClient.currentVersion()),
                                Toast.LENGTH_LONG,
                            ).show()
                        is UpdateClient.UpdateInfo.Available -> {
                            Toast.makeText(
                                this@MainActivity,
                                getString(R.string.update_download_started, info.version),
                                Toast.LENGTH_LONG,
                            ).show()
                            updateClient.downloadAndInstallPrompt(info.downloadUrl)
                            // 系统安装器已拉起；按钮恢复可点以便重试/再查。
                        }
                    }
                } catch (t: Exception) {
                    Toast.makeText(
                        this@MainActivity,
                        getString(R.string.update_check_failed, t.message ?: t.javaClass.simpleName),
                        Toast.LENGTH_LONG,
                    ).show()
                } finally {
                    checkUpdateBtn.isEnabled = true
                }
            }
        }

        unpairBtn.setOnClickListener {
            val intent = Intent(this, ControlForegroundService::class.java).apply {
                action = ServiceActions.ACTION_UNPAIR
            }
            startService(intent)
            // The service clears the credential; refresh the form once it settles.
            lifecycleScope.launch {
                kotlinx.coroutines.delay(300)
                updateUiForCredentialState()
            }
        }

        // If we already have a credential, start the foreground service so it dials.
        if (credentialStore.load() != null) {
            ControlForegroundService.start(this)
        }
    }

    override fun onResume() {
        super.onResume()
        // Accessibility may have been toggled in system settings while we were paused.
        setupAccessibilityUi()
        renderLocalInfo()
        updateUiForCredentialState()
    }

    /**
     * 渲染「本机信息」卡片：型号 / 系统 / 内网 IP / App 版本。配对前/后都显示，
     * 让用户配对当下就能核对自己手机的网段与版本（排障「连不上」时第一眼定位）。
     * onResume 重刷——切 WiFi/网络后内网 IP 会变。
     */
    private fun renderLocalInfo() {
        val sb = StringBuilder()
        sb.append("型号：").append(android.os.Build.MODEL ?: "").append('\n')
        sb.append("系统：Android ").append(android.os.Build.VERSION.RELEASE ?: "")
            .append(" (API ").append(android.os.Build.VERSION.SDK_INT).append(")\n")
        sb.append("厂商：").append(android.os.Build.MANUFACTURER ?: "")
            .append(" / ").append(android.os.Build.BRAND ?: "").append('\n')
        val ips = dev.devicecontrol.app.net.DeviceInfo.lanIps()
        sb.append("内网 IP：").append(if (ips.isEmpty()) "（未获取到）" else ips.joinToString(" / "))
        sb.append('\n')
        sb.append("App 版本：").append(runCatching {
            packageManager.getPackageInfo(packageName, 0).versionName ?: ""
        }.getOrDefault(""))
        localInfo.text = sb.toString()
    }

    private fun renderState(state: ConnectionState) {
        lastState = state
        when (state) {
            is ConnectionState.Connected -> {
                connectionStatus.text = getString(R.string.status_connected) + " · " + state.deviceId
                deviceIdText.text = getString(R.string.device_id_label) + state.deviceId
                updateUiForCredentialState()
                disconnectBtn.text = getString(R.string.disconnect)
                // 配对刚成功拨号上来时给一次明确反馈；后续重连不再弹（靠 wasPaired 标志）。
                if (justPaired) {
                    justPaired = false
                    Toast.makeText(this, R.string.pair_succeeded, Toast.LENGTH_SHORT).show()
                }
            }
            is ConnectionState.Disconnected -> {
                connectionStatus.text = getString(R.string.status_disconnected) +
                    if (state.willRetry) "（重试中）" else ""
                disconnectBtn.text = getString(R.string.disconnect)
            }
            ConnectionState.Connecting -> {
                connectionStatus.text = getString(R.string.status_connecting)
                disconnectBtn.text = getString(R.string.disconnect)
            }
            ConnectionState.Pairing -> {
                justPaired = true
                connectionStatus.text = getString(R.string.status_pairing)
                disconnectBtn.text = getString(R.string.disconnect)
            }
            is ConnectionState.Fatal -> {
                justPaired = false
                if (state.needsRePair) {
                    credentialStore.clear()
                    updateUiForCredentialState()
                }
                Toast.makeText(this, state.reason, Toast.LENGTH_LONG).show()
            }
            ConnectionState.Idle -> {
                // 断开/未连接：状态文字不能停留在上一次的「已连接」上。
                connectionStatus.text = getString(R.string.status_disconnected)
                disconnectBtn.text = getString(R.string.connect)
                updateUiForCredentialState()
            }
        }
    }

    private fun updateUiForCredentialState() {
        val cred = credentialStore.load()
        val hasCred = cred != null
        pairingForm.visibility = if (hasCred) View.GONE else View.VISIBLE
        connectedView.visibility = if (hasCred) View.VISIBLE else View.GONE
        if (hasCred && deviceIdText.text.isBlank()) {
            deviceIdText.text = getString(R.string.device_id_label) + "(连接中…)"
        }
    }

    /**
     * 配对码输入：8 格独立 EditText，视觉上按 4-4 分两组（XXXX-XXXX）。
     *
     * - 每格只收 1 个字符；输入合法字符（A-Z0-9，大小写不敏感）后自动跳到下一格，
     *   末格填满且 8 格全满时启用配对按钮。
     * - 在空格按删除键退到上一格，并清掉上一格的字符。
     * - 支持整段粘贴/自动填充：从当前格起把合法字符依次填入后续格。
     * - 字符口径与 [PairingClient.normalizeCode] 一致（大写 + [A-Z0-9]），
     *   拼出的 8 字符码直接可发；I/O/0/1 由服务端 403 兜底。
     */
    private fun setupPairingForm() {
        codeBoxes.forEachIndexed { index, box ->
            box.addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}

                override fun afterTextChanged(s: Editable?) {
                    if (editingBoxes || s == null) return
                    val text = s.toString()
                    if (text.length > 1) {
                        // 粘贴/自动填充进了多字符：清洗后从本格起依次分发。
                        distributePasted(index, text)
                        return
                    }
                    if (text.length == 1) {
                        val ch = text.uppercase().filter { it in 'A'..'Z' || it in '0'..'9' }
                        if (ch.isEmpty()) {
                            s.clear()
                        } else if (ch != text) {
                            editingBoxes = true
                            try {
                                s.replace(0, s.length, ch)
                            } finally {
                                editingBoxes = false
                            }
                        } else if (index < 7) {
                            codeBoxes[index + 1].requestFocus()
                        }
                    }
                    updatePairButton()
                }
            })
            box.setOnKeyListener { _, keyCode, event ->
                // 空格上按删除：退到上一格并清掉那里的字符（否则按键被空格吞掉）。
                if (keyCode == KeyEvent.KEYCODE_DEL &&
                    event.action == KeyEvent.ACTION_DOWN &&
                    box.text.isEmpty() &&
                    index > 0
                ) {
                    val prev = codeBoxes[index - 1]
                    editingBoxes = true
                    try {
                        prev.setText("")
                    } finally {
                        editingBoxes = false
                    }
                    prev.requestFocus()
                    updatePairButton()
                    true
                } else {
                    false
                }
            }
        }
        // 初始态：空码，按钮禁用。
        pairBtn.isEnabled = false
    }

    /** 粘贴/自动填充：把合法字符从 [startIndex] 起依次填入本格与后续格。 */
    private fun distributePasted(startIndex: Int, text: String) {
        editingBoxes = true
        try {
            val kept = text.uppercase().filter { it in 'A'..'Z' || it in '0'..'9' }
            val n = minOf(8 - startIndex, kept.length)
            for (k in 0 until n) {
                codeBoxes[startIndex + k].setText(kept[k].toString())
            }
        } finally {
            editingBoxes = false
        }
        // 焦点落到第一个空格；全部填满则停在末格。
        val next = (startIndex + minOf(8 - startIndex, keptLen(text))).coerceIn(0, 7)
        codeBoxes[if (allCodeFilled()) 7 else next].requestFocus()
        updatePairButton()
    }

    /** [distributePasted] 内已清洗过的合法字符数；这里按同口径重算。 */
    private fun keptLen(text: String): Int =
        text.uppercase().filter { it in 'A'..'Z' || it in '0'..'9' }.length

    private fun allCodeFilled(): Boolean = codeBoxes.all { it.text.length == 1 }

    /** 8 格拼出 8 字符码（无横线，服务端 NormalizeCode 同口径）。 */
    private fun codeString(): String = codeBoxes.joinToString("") { it.text.toString() }

    private fun updatePairButton() {
        pairBtn.isEnabled = allCodeFilled()
    }

    private fun setupAccessibilityUi() {
        // Two independent facts, both must hold for automation to actually work:
        //  1) Settings.Secure lists us (the system "should bind" declaration) — persists
        //     across reboots, but a ROM clean can uncheck it.
        //  2) The service singleton is connected (the system actually bound it into a
        //     live process) — null when the process was killed and not yet rebound.
        // The old code only checked (1), so a killed-but-not-unchecked service showed a
        // false "已启用" and a re-enabled-but-not-yet-bound one showed a false "未启用".
        val settingsEnabled = DeviceInfo.accessibilityEnabled(this)
        val running = DeviceControlAccessibilityService.instance != null
        when {
            !settingsEnabled -> {
                accessibilityStatus.text = getString(R.string.accessibility_disabled)
                openAccessibilityBtn.isEnabled = true
                openAccessibilityBtn.alpha = 1f
            }
            running -> {
                accessibilityStatus.text = getString(R.string.accessibility_enabled_running)
                openAccessibilityBtn.isEnabled = false
                openAccessibilityBtn.alpha = 0.5f
            }
            else -> {
                // 开关开着但服务没起来：进程被系统清了 / 刚开开关还没绑定。重进本页或
                // 在系统设置里关一次再开，系统就会重新绑定。
                accessibilityStatus.text = getString(R.string.accessibility_enabled_not_running)
                openAccessibilityBtn.isEnabled = true
                openAccessibilityBtn.alpha = 1f
            }
        }
    }

    private fun renderBatteryUi() {
        if (isIgnoringBatteryOptimizations()) {
            batteryStatus.text = getString(R.string.battery_status_whitelisted)
            batteryBtn.isEnabled = false
            batteryBtn.alpha = 0.5f
        } else {
            batteryStatus.text = getString(R.string.battery_status_not_whitelisted)
            batteryBtn.isEnabled = true
            batteryBtn.alpha = 1f
        }
    }

    private fun isIgnoringBatteryOptimizations(): Boolean {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        return pm.isIgnoringBatteryOptimizations(packageName)
    }

    /**
     * 跳厂商后台/省电设置：国产 ROM 各自为政，自启动与省电白名单不在标准 API 里，
     * 只能用已知 component 名逐个 resolve 试探，命中就跳；都跳不了退回应用详情页
     * 让用户自行找入口。电池优化白名单（上一按钮）覆盖原生层；这里覆盖厂商层，两
     * 者都要开才稳。
     */
    private fun openOemBackgroundSettings() {
        val candidates = listOf(
            // 小米 MIUI：自启动管理
            ComponentName(
                "com.miui.securitycenter",
                "com.miui.permcenter.autostart.AutoStartManagementActivity",
            ),
            // 华为 EMUI/HarmonyOS：启动管理
            ComponentName(
                "com.huawei.systemmanager",
                "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
            ),
            // OPPO ColorOS：自启动管理
            ComponentName(
                "com.coloros.safecenter",
                "com.coloros.safecenter.permission.startup.StartupAppListActivity",
            ),
            // OPPO 旧版
            ComponentName(
                "com.coloros.safecenter",
                "com.coloros.safecenter.startupapp.StartupAppListActivity",
            ),
            // vivo OriginOS：后台弹出活动管理
            ComponentName(
                "com.vivo.permissionmanager",
                "com.vivo.permissionmanager.activity.BgStartUpManagerActivity",
            ),
            // 三星 One UI：电池优化列表
            ComponentName(
                "com.samsung.android.lool",
                "com.samsung.android.sm.ui.battery.BatteryActivity",
            ),
            // 魅族 Flyme：自启动管理
            ComponentName(
                "com.meizu.safe",
                "com.meizu.safe.security.SHOW_APPSEC",
            ),
        )
        val pm = packageManager
        for (cn in candidates) {
            val intent = Intent().apply {
                component = cn
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            if (intent.resolveActivity(pm) != null) {
                runCatching { startActivity(intent) }.onSuccess { return }
            }
        }
        // 都没命中：退回应用详情页（省电/权限入口大多在此）。
        runCatching {
            startActivity(
                Intent(
                    Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                    Uri.parse("package:$packageName"),
                ),
            )
        }.onFailure {
            startActivity(Intent(Settings.ACTION_SETTINGS))
        }
    }

    private fun isAccessibilityServiceEnabled(): Boolean =
        dev.devicecontrol.app.net.DeviceInfo.accessibilityEnabled(this)
}
