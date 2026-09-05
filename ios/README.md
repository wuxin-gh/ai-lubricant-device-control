# device-control iOS driver

The iOS device-side endpoint of `device-control`. It is **not an on-device iOS
app** — non-jailbroken iOS forbids an app from controlling other apps (see
[`../PLAN.md`](../PLAN.md) §7). Instead, this is a **host-side Go driver** that
mirrors `mobile-mcp`'s iOS path:

- **WebDriverAgent (WDA)** runs on the iPhone as an XCTest runner — the only
  legitimate channel for cross-app UI automation on a non-jailbroken device.
- **go-ios** (this driver) discovers the device over USB or LAN WiFi, launches
  and maintains the WDA session, forwards WDA's HTTP port to the host, and
  issues the WebDriver REST calls.
- The driver **dials a device-control server** over WebSocket and speaks the
  v0 protocol exactly like the Android app — `register`, heartbeat, the 16
  commands, the structured errors. The server cannot tell it apart from an
  Android device on the wire.

The driver is a Go package + a thin CLI. **For production deployment, use the
iOS host node integration ([`nodes/ios/`](../../nodes/ios/)) which automates
device discovery, WDA signing/installation/renewal, and multi-device management
via the server control plane.** For development or sidecar mode, import the
public entry package `device-control/ios/devicecontrol` — see
[Integrating into a Go node](#integrating-into-a-go-node).

## Status

**Production ready** with automated device management. The complete stack is
implemented and tested:

- ✅ **Protocol layer**: wire, dispatcher, wsclient, pairclient, the §8.2
  compact-tree formatter, and all 16 command handlers are built and unit-tested.
- ✅ **WDA runtime**: `internal/devicelink` uses go-ios v1.3.2 with standard WDA
  session protocol (GET /status, POST /session, W3C Actions for taps/swipes),
  dynamic port forwarding, and automatic scale/bounds detection.
- ✅ **iOS host node** (`nodes/ios/`): NodeConnect-based control plane with
  device discovery, automated claim flow, WDA signing/installation/renewal jobs,
  and multi-device orchestration.
- ✅ **Server-side**: signing profile storage (ASC p8 / P12), WDA artifact
  library with SHA256 deduplication, job dispatch and progress tracking.
- ✅ **Frontend**: automated device scan and one-click claim, WDA status
  monitoring, signing/artifact management dialogs, real-time job progress.

**Real-device verification gate**: ASC API signing, zipconduit installation, iOS
17+ tunnel/RSD transport, and multi-device stress testing are implemented but
await physical iPhone validation. The architecture and interfaces are stable;
device-specific tuning (if needed) will happen in adapters, not the core
design.

## Deployment modes

### A. iOS host node (recommended for production)

**Automated multi-device management with server orchestration.** One `node-ios`
process per physical host (Mac/Linux/Windows with USB-connected iPhones):

1. **Automatic discovery**: Node enumerates all attached devices via go-ios and
   reports them to the server (UDID, name, model, iOS version, connection type).
2. **One-click claim**: Users select a device in the web UI and click "接入" —
   server mints a one-time pairing code, dispatches it to the node via
   NodeConnect, and the node calls the existing `devicecontrol.Pair` flow.
3. **WDA automation**: Server orchestrates WDA prepare/renew/reinstall jobs:
   - User uploads signing materials (ASC p8 API key or P12 certificate) and WDA
     artifacts (.ipa) to the server once.
   - Job dispatch: server sends signing credentials and artifact download URL to
     the node over TLS NodeConnect (one-time, never logged).
   - Node executes: download → sign with go-ios signing package (pure Go, works
     on any OS) → install via zipconduit → launch with testmanagerd → forward
     port → verify session.
   - Real-time progress: node reports job events (stage/percent/message) back to
     server; frontend polls and displays live progress bar.
4. **Renewal monitoring**: Node tracks mobileprovision expiry and reports
   `renewal_due` 14 days before expiration; users click "续期" to trigger a
   renew job (reuses certificate ID, only rebuilds profile).
5. **Multi-device scaling**: One node manages N devices (one WS connection + one
   device-control goroutine per device); server sees each as an independent
   device resource.

**Setup**: See [`nodes/ios/README.md`](../../nodes/ios/) for node deployment and
[web UI documentation](../../docs-site/cap-device-control.html) for the user
flow. No manual UDID entry, no Xcode signing on the node host — the server
handles all credentials and the node executes the automation.

### B. Sidecar mode (development / custom integration)

**Direct Go package integration for standalone programs.** Import
`device-control/ios/devicecontrol` and manage pairing + WDA lifecycle yourself:

```go
import "device-control/ios/devicecontrol"

// Pair once per device
_, err := devicecontrol.Pair(ctx, serverURL, code, credPath)

// Run device (blocks for connection lifetime)
err = devicecontrol.RunDevice(ctx, devicecontrol.RunConfig{
    CredentialPath: credPath,
    Device: devicecontrol.Options{
        UDID:        udid,
        Transport:   "usb",  // or "network"
        WDABundleID: "com.facebook.WebDriverAgentRunner.xctrunner",
        WDAPort:     8100,   // device-side port (host port auto-allocated)
    },
    OnState: func(s devicecontrol.State) { log.Printf("state: %v", s) },
    OnWipe:  func() { log.Print("credential wiped, re-pair required") },
})
```

You provide the WDA runner (see [Mac debugging](#mac-debugging) for manual Xcode
build steps). Use this mode for:
- Development and debugging (one device, manual WDA management)
- Custom integration where you already have device management infrastructure
- Embedded scenarios where the full iOS host node is too heavy

See [Integrating into a Go node](#integrating-into-a-go-node) for the complete
sidecar API.

### C. CLI (reference implementation)

The `cmd/device-control-ios` CLI is a thin flag-based wrapper around the
sidecar API, primarily for testing the device-control protocol layer:

```sh
./device-control-ios pair --server URL --code CODE --state ./cred.json
./device-control-ios run --state ./cred.json --udid X --wda-bundle Y --wda-port Z
```

Not recommended for production (no discovery, no WDA automation, manual flag
management). Use the iOS host node (mode A) or sidecar package (mode B) instead.

## Layout

```
ios/
  cmd/device-control-ios/main.go      CLI reference implementation
  internal/
    wire/        protocol v0 frames (spec transcription)
    errcode/     the 12 wire error codes + Go-error→code mapping
    caps/        the 16-command capability set (parity with Android)
    creds/       credential file, 0600 atomic write
    pairclient/  POST /pair + NormalizeCode
    wsclient/    dial /ws/device, register, heartbeat, full-jitter backoff,
                 close-code classification (4003 wipe / 4004 stop / else retry)
    dispatcher/  request_id dedup, in-flight ≤8, timeout budget, error mapping
    screen/      CompactTreeFormatter (Go port of the Android §8.2 formatter)
                 + Node/WindowData/ScreenInfo types + cursor pagination
    wda/         WDA session client (standard WebDriver protocol) + source parser
    shot/        PNG→JPEG q80 / ≤700px / base64 / ≤1.5MiB (§8.3)
    commands/    the 16 v0 command handlers
    devicelink/  go-ios integration: discovery, WDA launch (testmanagerd),
                 dynamic port forwarding, session lifecycle
  devicecontrol/ public API for sidecar integration (re-exports)
  go.mod         module device-control/ios (go 1.24)

../nodes/ios/    iOS host node with automated device management
  main.go        NodeConnect client + device manager
  handler.go     Control plane frame handlers (discover/claim/release/configure)
  manager.go     Device discovery + lifecycle orchestration
  wdajob.go      WDA job engine (download→sign→install→verify pipeline)
```

## Build and test

```sh
cd ios
go build ./...
go test ./...
```

## Integrating into a Go node

This is the intended path for your node program. The public package
`device-control/ios/devicecontrol` re-exports the types and entry points a node
needs; the CLI is just the same packages wired with `flag`.

**Note**: For production iOS device management, prefer the iOS host node
([`nodes/ios/`](../../nodes/ios/)) which includes automated discovery, WDA
signing/installation, and server orchestration. Use direct integration only for
custom scenarios where you manage the WDA lifecycle yourself.

### Dependency

```sh
cd your-node
go get device-control/ios@<commit-or-tag>
```

If the node lives in a separate repo and this module isn't published, use a
`replace` pointing at the local checkout:

```
// in your node's go.mod
require device-control/ios v0.0.0
replace device-control/ios => /path/to/device-control/ios
```

### Single device

```go
package yournode

import (
    "context"
    "log"

    "device-control/ios/devicecontrol"
)

func RunOneDevice(ctx context.Context) error {
    return devicecontrol.RunDevice(ctx, devicecontrol.RunConfig{
        CredentialPath: "/var/lib/node/ios-dev-1.json",
        Device: devicecontrol.Options{
            UDID:         "<your-udid>",
            Transport:    "usb",                     // or "network" for LAN WiFi
            WDABundleID:  "com.facebook.WebDriverAgentRunner.xctrunner",
            XCTestConfig: "WebDriverAgentRunner.xctest",
            WDAPort:      8100,                      // device-side port (host auto-allocated)
        },
        OnState: func(s devicecontrol.State) {
            log.Printf("ios: state -> %v", s)       // surface in health/metrics
        },
        OnWipe: func() {
            log.Print("ios: credential wiped (auth failed); re-pair required")
            // mark this device unpaired in your node's state
        },
    })
}
```

`RunDevice` blocks for the connection lifetime (reconnect loop is internal);
run it on a long-lived goroutine per device. It returns `ErrNotPaired` if no
credential exists — call `Pair` first:

```go
_, err := devicecontrol.Pair(ctx, serverURL, code, credentialPath)
if err != nil { /* pairclient.InvalidCodeError / ProtocolMismatchError are typed */ }
```

**WDA setup**: You must provide a signed WDA runner. For automated WDA
management, use the iOS host node instead of direct integration.

### Multiple devices from one node

One WebSocket connection carries **one** `device_id` (spec §3). A node fronting
N iPhones calls `RunDevice` N times — one goroutine each, each with its own
`CredentialPath` (its own pairing) and `Device.UDID`/`WDAPort`. Pair each
device separately first; the goroutines share nothing except the node process.

### Lifecycle notes

- `RunDevice` does **not** return on a transient drop — only on ctx cancel or a
  fatal close (`4003`/`4004`). One call per device for the node's lifetime.
- For a long-lived host, prefer `RunDeviceWithRetry` over `RunDevice`: it wraps
  the call and, on a **start failure** (device not attached, WDA down), backs off
  with full-jitter (mirroring the WS reconnect cap) and retries until ctx cancel
  — so a USB replug or a WDA 7-day re-sign self-heals without a process restart.
  `RunDevice` alone retries WS drops inside `ConnectLoop` but never retries a
  start failure, since one never reaches the loop. `RunDeviceWithRetry` is the
  right seam for "one host, many phones."
- On `4003` the driver wipes the credential, fires `OnWipe`, then returns
  `ErrNotPaired`-shaped behavior; your node should surface the device for
  re-pairing.
- `OnState` fires on transitions (`Idle → Connecting → Connected → Disconnected
  (will retry) → ...`; `FatalAuth`/`FatalVersion` are terminal for this
  credential). A `State` stringifies itself (`s.String()`) — log it directly
  instead of hand-rolling a switch.

## Mac debugging

**Note**: For production deployment, use the iOS host node which automates WDA
signing and installation. This section is for development, debugging, or manual
WDA builds.

### A. Build and install WebDriverAgent (one time per signing cycle)

The iOS host node automates this with go-ios signing (works on any OS). For
manual builds or verification:

```sh
# 1. Get WDA (the Appium fork is maintained; facebookarchive is stale).
git clone https://github.com/appium/WebDriverAgent.git
cd WebDriverAgent

# 2. Open in Xcode, set a development team + bundle id on the
#    WebDriverAgentRunner target (a free Apple ID works; the runner
#    expires every 7 days and needs a re-sign+reinstall; $99/yr → 1 year).
open WebDriverAgent.xcodeproj

# 3. Build-for-testing (installs the runner on the device the first time).
xcodebuild build-for-testing \
  -project WebDriverAgent.xcodeproj \
  -scheme WebDriverAgentRunner \
  -destination "id=<YOUR-UDID>"

# 4. Verify WDA starts. The driver will launch it automatically; this is
#    just to confirm the signing and installation worked.
xcodebuild test-without-building \
  -project WebDriverAgent.xcodeproj \
  -scheme WebDriverAgentRunner \
  -destination "id=<YOUR-UDID>"
```

Note the **runner bundle id** you set (e.g. `com.you.WebDriverAgentRunner`),
the **xctest config name** (`WebDriverAgentRunner.xctest`). Find the UDID with
`xcrun xctrace list devices` or `idevice_id -l` / `go-ios list`.

The driver automatically discovers WDA's port via environment variable injection
(`USE_PORT=8100` on device side, dynamic host port allocation).

### B. Verify WDA directly (before involving this driver)

Before wiring the driver, confirm WDA is reachable from the host:

```sh
# go-ios can install + run WDA without Xcode open afterwards:
go-ios run wda --bundleid com.you.WebDriverAgentRunner.xctrunner \
               --testrunnerbundleid com.you.WebDriverAgentRunner \
               --xctestconfig WebDriverAgentRunner

# In another terminal, hit WDA's HTTP API directly (driver auto-forwards port):
curl http://localhost:8100/status
curl -X POST http://localhost:8100/session -d '{"capabilities":{}}'
```

If these return, WDA is up. If not, see [troubleshooting](#troubleshooting).

### C. Run the driver against the live WDA

```sh
cd ios
go run ./cmd/device-control-ios pair \
  --server http://localhost:8787 \
  --code ABCD-EFGH \
  --state ./device-control-ios.json

go run ./cmd/device-control-ios run \
  --state ./device-control-ios.json \
  --transport usb \
  --udid <YOUR-UDID> \
  --wda-bundle com.you.WebDriverAgentRunner.xctrunner \
  --xctest WebDriverAgentRunner.xctest \
  --wda-port 8100
```

The driver launches WDA via testmanagerd, forwards the port dynamically, and
establishes a standard WebDriver session.

### D. Drive it from the server

```sh
export DEVICE_CONTROL_ADMIN_TOKEN=$(openssl rand -base64 32)
./device-control-server -addr 127.0.0.1:8787   # from server/

ADMIN="Authorization: Bearer $DEVICE_CONTROL_ADMIN_TOKEN"
curl -sX POST localhost:8787/admin/devices -H "$ADMIN"   # confirm the iOS device is online
curl -sX POST localhost:8787/admin/devices/$DEVICE_ID/call -H "$ADMIN" \
  -H 'Content-Type: application/json' \
  -d '{"cmd":"get_screen_state","args":{"include_screenshot":true}}'
curl -sX POST localhost:8787/admin/devices/$DEVICE_ID/call -H "$ADMIN" \
  -H 'Content-Type: application/json' -d '{"cmd":"tap","args":{"x":540,"y":1200}}'
```

### Troubleshooting

| symptom | likely cause / fix |
|---|---|
| `go-ios: no iOS device found (is usbmuxd running?)` | **macOS**: usbmuxd runs via iTunes/Apple Mobile Device Service; install Xcode command-line tools or restart `com.apple.usbmuxd`. **Windows**: install Apple Mobile Device Support (bundled with iTunes; a standalone Apple Devices app install also works) — without it no usbmuxd service exists and USB devices are invisible. Then trust the computer on the iPhone. **Linux**: run usbmuxd as a system service (`systemctl start usbmuxd`, package `usbmuxd`). |
| `connect iPhone: devicelink start: ...` | WDA isn't up. Confirm step B returns before running the driver. Re-run `xcodebuild test-without-building` or `go-ios run wda`. |
| WDA builds but the runner won't stay up | Free-account 7-day expiry — re-sign and reinstall. Or the device's Developer Mode is off (iOS 16+: Settings → Privacy & Security → Developer Mode). |
| `tap` lands in the wrong place | Scale mismatch: the driver auto-detects scale from WDA session capabilities. If taps are consistently off, check `device_info.screen.scale` in the register frame. |
| `press_back` returns `device_error` | Expected — iOS has no system-wide back. The command is declared for parity and handled as a clear error. |

## Command parity with Android

All 16 v0 commands are implemented. iOS-specific notes:

| command | iOS behavior |
|---|---|
| `get_screen_state` | WDA `/source` → compact TSV (same §8.2 format as Android); `/screenshot` → JPEG q80/≤700px/≤1.5MiB |
| `tap`/`long_press`/`double_tap` | coordinate tap, or `node_id` resolved to bounds center (WDA has no `ACTION_CLICK`, so iOS always taps the center, unlike Android's `performAction`) |
| `swipe`/`scroll` | WDA drag + synthesized direction swipes |
| `scroll_to_node` | up to 5 scroll attempts, 300ms settle (§8.5) |
| `type_text`/`set_text` | WDA `/type` and `/value` |
| `press_key` | §8.6 enumeration mapped to WDA keycodes; `back` → `device_error` (iOS has no system back) |
| `press_home` | WDA `/home` |
| `press_recents` | app-switcher gesture (approximate) |
| `press_back` | `device_error` — iOS has no system-wide back |
| `open_app`/`list_apps` | `package` = iOS bundle id; go-ios `launch` + `installationproxy` |
| `dismiss_keyboard` | WDA `/dismissKeyboard` |

## Coordinates

Spec §1 mandates integer **physical pixels**. WDA rects are in **points**, so
the driver multiplies by the device `scale` (2×/3×) before emitting `bounds`/
`screen`. Taps are divided back by scale before sending to WDA. `density`
(§8.1, an Android densityDpi integer) has no iOS equivalent; a scale-derived
value (2×→320, 3×→480) is reported, best-effort.

## CLI reference

```
device-control-ios pair --server URL --code CODE [--state PATH]
device-control-ios run  [--state PATH] [--transport usb|network] [--udid X]
                       [--wda-bundle ID --xctest NAME --wda-port N]
```

`--state` defaults to `./device-control-ios.json` (0600). `--transport` selects
USB (default, stable) or `network` (LAN WiFi, fragile for long sessions).
`--wda-port` specifies the device-side port (default 8100; host port is
dynamically allocated).

**Production deployment**: Use the iOS host node ([`nodes/ios/`](../../nodes/ios/))
instead of the CLI — it automates discovery, pairing, WDA signing/installation,
and multi-device orchestration.

## License

MIT, matching the project root. Reused third-party sources (go-ios MIT,
WebDriverAgent upstream) are attributed in [`../NOTICE`](../NOTICE).
