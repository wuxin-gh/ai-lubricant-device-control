package dev.devicecontrol.app.ui

import android.content.Intent
import android.os.Bundle
import android.provider.Settings
import android.text.Editable
import android.text.TextWatcher
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import dev.devicecontrol.app.ControlForegroundService
import dev.devicecontrol.app.R
import dev.devicecontrol.app.ServiceActions
import dev.devicecontrol.app.net.ConnectionState
import dev.devicecontrol.app.net.ConnectionStateHolder
import dev.devicecontrol.app.net.PairingClient
import dev.devicecontrol.app.storage.CredentialStore
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
    private lateinit var pairingForm: View
    private lateinit var serverUrlEdit: EditText
    private lateinit var pairingCodeEdit: EditText
    private lateinit var pairBtn: Button
    private lateinit var connectedView: View
    private lateinit var deviceIdText: TextView
    private lateinit var connectionStatus: TextView
    private lateinit var disconnectBtn: Button
    private lateinit var unpairBtn: Button

    private val credentialStore by lazy { CredentialStore(this) }
    private var justPaired = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        accessibilityStatus = findViewById(R.id.accessibility_status)
        openAccessibilityBtn = findViewById(R.id.open_accessibility_btn)
        pairingForm = findViewById(R.id.pairing_form)
        serverUrlEdit = findViewById(R.id.server_url)
        pairingCodeEdit = findViewById(R.id.pairing_code)
        pairBtn = findViewById(R.id.pair_btn)
        connectedView = findViewById(R.id.connected_view)
        deviceIdText = findViewById(R.id.device_id)
        connectionStatus = findViewById(R.id.connection_status)
        disconnectBtn = findViewById(R.id.disconnect_btn)
        unpairBtn = findViewById(R.id.unpair_btn)

        setupAccessibilityUi()
        setupPairingForm()
        updateUiForCredentialState()

        // Observe connection state and reflect it in the UI.
        lifecycleScope.launch {
            ConnectionStateHolder.state.collect { state ->
                renderState(state)
            }
        }

        openAccessibilityBtn.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }

        pairBtn.setOnClickListener {
            val url = serverUrlEdit.text.toString().trim()
            // 配对码输入框已格式化成 ABCD-EFGH，这里 trim 后直接用。
            val code = pairingCodeEdit.text.toString().trim()
            if (url.isEmpty() || code.isEmpty()) {
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
            val intent = Intent(this, ControlForegroundService::class.java).apply {
                action = ServiceActions.ACTION_DISCONNECT
            }
            startService(intent)
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
        updateUiForCredentialState()
    }

    private fun renderState(state: ConnectionState) {
        when (state) {
            is ConnectionState.Connected -> {
                connectionStatus.text = getString(R.string.status_connected) + " · " + state.deviceId
                deviceIdText.text = getString(R.string.device_id_label) + state.deviceId
                updateUiForCredentialState()
                // 配对刚成功拨号上来时给一次明确反馈；后续重连不再弹（靠 wasPaired 标志）。
                if (justPaired) {
                    justPaired = false
                    Toast.makeText(this, R.string.pair_succeeded, Toast.LENGTH_SHORT).show()
                }
            }
            is ConnectionState.Disconnected -> {
                connectionStatus.text = getString(R.string.status_disconnected) +
                    if (state.willRetry) "（重试中）" else ""
            }
            ConnectionState.Connecting -> connectionStatus.text = getString(R.string.status_connecting)
            ConnectionState.Pairing -> {
                justPaired = true
                connectionStatus.text = getString(R.string.status_pairing)
            }
            is ConnectionState.Fatal -> {
                justPaired = false
                if (state.needsRePair) {
                    credentialStore.clear()
                    updateUiForCredentialState()
                }
                Toast.makeText(this, state.reason, Toast.LENGTH_LONG).show()
            }
            ConnectionState.Idle -> updateUiForCredentialState()
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
     * 配对码输入框：边输入边格式化成 ABCD-EFGH 形态。
     *
     * - 只保留 A-Z0-9（与 [PairingClient.normalizeCode] 同口径，I/O/0/1 由服务端 403 兜底）
     * - 自动大写、自动在 4 字符后插横线、上限 9 字符（8 字母 + 1 横线）
     * - 凑不满 8 个有效字符就禁用配对按钮，避免发出必然 403 的请求
     */
    private fun setupPairingForm() {
        pairingCodeEdit.addTextChangedListener(object : TextWatcher {
            private var editing = false

            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}

            override fun afterTextChanged(s: Editable?) {
                if (editing || s == null) return
                editing = true
                try {
                    val kept = s.toString().uppercase().filter { it in 'A'..'Z' || it in '0'..'9' }
                    val limited = if (kept.length > 8) kept.substring(0, 8) else kept
                    val formatted = if (limited.length > 4) {
                        limited.substring(0, 4) + "-" + limited.substring(4)
                    } else {
                        limited
                    }
                    if (formatted != s.toString()) {
                        s.replace(0, s.length, formatted, 0, formatted.length)
                    }
                    pairBtn.isEnabled = limited.length == 8
                } finally {
                    editing = false
                }
            }
        })
        // 初始态：空码，按钮禁用。
        pairBtn.isEnabled = false
    }

    private fun setupAccessibilityUi() {
        if (isAccessibilityServiceEnabled()) {
            accessibilityStatus.text = getString(R.string.accessibility_enabled)
            openAccessibilityBtn.isEnabled = false
            openAccessibilityBtn.alpha = 0.5f
        } else {
            accessibilityStatus.text = getString(R.string.accessibility_disabled)
            openAccessibilityBtn.isEnabled = true
            openAccessibilityBtn.alpha = 1f
        }
    }

    private fun isAccessibilityServiceEnabled(): Boolean {
        if (!isAccessibilityEnabled()) return false
        val enabledServices = Settings.Secure.getString(
            contentResolver,
            Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES,
        ) ?: return false
        // The accessibility service lives in this app's package (core module is merged in).
        val serviceName = "dev.devicecontrol.core.accessibility.DeviceControlAccessibilityService"
        val component = "$packageName/$serviceName"
        return enabledServices.contains(component)
    }

    private fun isAccessibilityEnabled(): Boolean =
        Settings.Secure.getInt(contentResolver, Settings.Secure.ACCESSIBILITY_ENABLED, 0) == 1
}
