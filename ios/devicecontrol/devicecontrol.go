// Package devicecontrol is the public integration surface of the iOS driver
// for an existing Go service node. It re-exports the types and entry points a
// node needs, so callers outside this module can import a single package
// instead of the internal/ ones (which Go forbids importing across modules).
//
// Import path: device-control/ios/devicecontrol
//
//	import "device-control/ios/devicecontrol"
//
//	dc.RunDevice(ctx, devicecontrol.RunConfig{...})
//
// The internal/ packages remain the implementation; this package is the stable
// seam. Anything not exposed here is intentionally internal.
package devicecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"device-control/ios/internal/commands"
	"device-control/ios/internal/creds"
	"device-control/ios/internal/devicelink"
	"device-control/ios/internal/dispatcher"
	"device-control/ios/internal/pairclient"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wsclient"
)

// Options configures the connection to one iPhone. It is a re-export of
// devicelink.Options so callers do not import internal/.
type Options = devicelink.Options

// Credential is the persisted pairing result (server URL, device id, token).
type Credential = creds.Credential

// State is the WS connection state surfaced to the host.
type State = wsclient.State

const (
	StateIdle         = wsclient.StateIdle
	StateConnecting   = wsclient.StateConnecting
	StateConnected    = wsclient.StateConnected
	StateDisconnected = wsclient.StateDisconnected
	StateFatalAuth    = wsclient.StateFatalAuth
	StateFatalVersion = wsclient.StateFatalVersion
)

// State has a String method defined on it in wsclient, so integrators can log
// a State directly (s.String() / slog's auto-stringify) without hand-rolling a
// switch that drifts as new states are added.

// RunConfig is the full per-device config a node passes to RunDevice.
type RunConfig struct {
	// CredentialPath is the 0600 file written by Pair. One per device.
	CredentialPath string
	// Device holds the go-ios + WDA connection params.
	Device Options
	// OnState is invoked (on a separate goroutine) on every state transition;
	// nil disables it. Use it to surface health/metrics in the node.
	OnState func(State)
	// OnWipe is invoked after the credential is wiped on a 4003 close, before
	// RunDevice returns. nil disables it. The node should mark the device
	// unpaired and surface it for re-pairing.
	OnWipe func()
	// DeviceInfoExtra is merged into the register frame's device_info. Keys the
	// driver owns (platform, model, app_version, screen) are reserved and cannot
	// be overridden here — an extra entry under a reserved key is dropped. This is
	// the integration seam for a host that wants to carry its own correlation id
	// (e.g. an agent-compose node_id) without the driver knowing that concept.
	DeviceInfoExtra map[string]any
}

// Pair redeems a pairing code and writes the credential to path (0600).
// It is the programmatic equivalent of `device-control-ios pair`.
func Pair(ctx context.Context, serverURL, code, credentialPath string) (Credential, error) {
	pc := pairclient.New()
	res, err := pc.Pair(serverURL, code)
	if err != nil {
		return Credential{}, err
	}
	c := Credential{
		ServerURL: res.ServerURL,
		DeviceID:  res.DeviceID,
		Token:     res.Token,
	}
	if err := creds.New(credentialPath).Save(c); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// LoadCredential reads the credential file, returning (zero, false, nil) when
// none exists (i.e. the device is not yet paired).
func LoadCredential(path string) (Credential, bool, error) {
	c, err := creds.New(path).Load()
	if err != nil {
		return Credential{}, false, err
	}
	if c == nil {
		return Credential{}, false, nil
	}
	return *c, true, nil
}

// Link is a started WebDriverAgent session on one device: the go-ios runner,
// the forwarded HTTP port, and the WDA client. It is the re-export of
// devicelink.Link so a host can drive WDA directly — used by the node's WDA
// preparation job to verify a freshly installed runner actually answers before
// the device is reported ready.
type Link = devicelink.Link

// NewLink builds an unstarted Link. Call Start to launch WDA and Close to tear
// the runner and port forward down. RunDevice does this internally for the
// device-control path; NewLink is for a caller that only wants to prove WDA
// works (install verification, health check).
func NewLink(opts Options) *Link {
	return devicelink.New(opts)
}

// RunDevice connects to one iPhone, launches WDA, and runs the WebSocket
// connection loop until ctx is cancelled or a fatal close (4003/4004) lands.
// It blocks; run it on a long-lived goroutine. Returns nil on ctx cancel, a
// non-nil error on a fatal close or a start failure.
//
// A start failure (device not attached, WDA down) is wrapped in
// ErrStartFailed so a caller can distinguish "retry" from "give up":
// ErrNotPaired and the 4003/4004 fatal closes are terminal; a start failure is
// transient and is what RunDeviceWithRetry retries.
//
// For N devices, call RunDevice N times (one goroutine each), each with its own
// CredentialPath (its own pairing) and Device.UDID/WDA params.
func RunDevice(ctx context.Context, cfg RunConfig) error {
	cred, ok, err := LoadCredential(cfg.CredentialPath)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotPaired
	}

	link := devicelink.New(cfg.Device)
	if err := link.Start(ctx); err != nil {
		// Wrap so RunDeviceWithRetry can tell a transient start failure from a
		// terminal fatal close. errors.Is(err, ErrStartFailed) is the retry gate.
		return fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	defer link.Close()

	info, scale, _ := link.ScreenInfo(ctx)
	deviceInfoMap := map[string]any{
		"platform":    "ios",
		"model":       cfg.Device.UDID,
		"app_version": "0.1.0",
		"screen": map[string]any{
			"w":       info.Width,
			"h":       info.Height,
			"density": info.DensityDpi,
			"scale":   scale,
		},
	}
	// Merge caller-supplied extras (e.g. a host node_id for server-side
	// device↔node join). Reserved keys are the driver's own — an extra under
	// one is dropped so a host cannot spoof platform/model/screen.
	for k, v := range cfg.DeviceInfoExtra {
		if _, reserved := deviceInfoMap[k]; !reserved {
			deviceInfoMap[k] = v
		}
	}
	deviceInfo, _ := json.Marshal(deviceInfoMap)

	d := dispatcher.New(commands.Build(link, screen.New()))
	client := &wsclient.Client{
		Dispatcher: d,
		DeviceInfo: deviceInfo,
		OnWipe: func() {
			_ = creds.New(cfg.CredentialPath).Clear()
			if cfg.OnWipe != nil {
				cfg.OnWipe()
			}
		},
	}

	if cfg.OnState != nil {
		go func() {
			var last State
			for {
				s := client.State()
				if s != last {
					last = s
					cfg.OnState(s)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(stateTick):
				}
			}
		}()
	}

	return client.ConnectLoop(ctx, &cred)
}

// retryCap bounds the backoff between start-failure retries. It mirrors the
// wsclient reconnect cap (30s) so a transiently-absent device and a dropped WS
// connection heal on the same cadence.
const retryCap = 30 * time.Second

// RunDeviceWithRetry runs RunDevice and, on a transient start failure (device
// not attached, WDA down), backs off and tries again until ctx is cancelled or
// RunDevice returns a terminal result (nil on ctx cancel, ErrNotPaired, or a
// 4003/4004 fatal close).
//
// It exists because a start failure never reaches RunDevice's internal
// ConnectLoop, so it gets zero retries on its own — a USB replug or a WDA
// 7-day re-sign would otherwise take a device offline until a manual process
// restart. The retry is a sleeping loop (near-zero CPU) that self-heals when
// the device reappears, so one flaky phone does not force a restart that drops
// the other N.
//
// Use this instead of RunDevice for any long-lived host. It blocks for the
// device's lifetime; run it on a long-lived goroutine, one per device.
func RunDeviceWithRetry(ctx context.Context, cfg RunConfig) error {
	const base = 1 * time.Second
	attempt := 0
	for {
		err := RunDevice(ctx, cfg)
		if err == nil {
			return nil // ctx cancelled inside RunDevice
		}
		if errors.Is(err, ErrStartFailed) {
			// Transient: the device or WDA is momentarily unreachable. Back off
			// and retry; the device can come back without a process restart.
			delay := backoffDelay(attempt, base, retryCap)
			attempt++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		// ErrNotPaired, 4003/4004 fatal, or a load error: terminal.
		return err
	}
}

// backoffDelay is full-jitter (matching wsclient.ComputeBackoffDelay): a uniform
// random in [0, min(cap, base<<attempt)]). The randomization prevents a fleet
// of retried devices from thundering back onto the host's USB bus in lockstep.
func backoffDelay(attempt int, base, cap time.Duration) time.Duration {
	shift := attempt
	if shift > 15 { // 2^15 > cap in ms; saturate to avoid int64 overflow
		shift = 15
	}
	upper := int64(base) << shift
	if upper > int64(cap) || upper <= 0 {
		upper = int64(cap)
	}
	return time.Duration(rand.Int63n(upper + 1))
}

