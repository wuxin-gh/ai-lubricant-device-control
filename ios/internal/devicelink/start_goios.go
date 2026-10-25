package devicelink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/forward"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"
	"github.com/danielpaulus/go-ios/ios/tunnel"

	"device-control/ios/internal/wda"
)

// goiosSession is the production deviceSession: it drives go-ios to launch
// the WDA XCTest runner and forward its HTTP port to localhost.
//
// REAL-DEVICE GATE: every go-ios call here is implemented against the v1.3.2
// API from the vendored source, but none of it has run against a physical
// iPhone yet. The sequence (pick device → launch runner → forward → wait) is
// exercised by unit tests through the deviceSession seam in devicelink.go;
// what a device verifies is go-ios's behavior on the target iOS version
// (notably iOS 17+, which routes through the CoreDevice/RSD tunnel).
type goiosSession struct {
	device   ios.DeviceEntry
	hostPort int

	mu       sync.Mutex
	forward  *forward.ConnListener
	cancel   context.CancelFunc
	runnerErr chan error
	log      bytes.Buffer
	closed   bool
}

// HostPort implements deviceSession.
func (s *goiosSession) HostPort() int { return s.hostPort }

// RunnerLog implements deviceSession.
func (s *goiosSession) RunnerLog() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.String()
}

// ListApps implements deviceSession via installationproxy (stable device
// service; WDA's app-list route differs across forks).
func (s *goiosSession) ListApps(ctx context.Context, includeSystem bool) ([]wda.App, error) {
	conn, err := installationproxy.New(s.device)
	if err != nil {
		return nil, fmt.Errorf("installationproxy connect: %w", err)
	}
	defer conn.Close()

	browse := conn.BrowseUserApps
	if includeSystem {
		browse = conn.BrowseAllApps
	}
	infos, err := browse()
	if err != nil {
		return nil, fmt.Errorf("installationproxy browse: %w", err)
	}
	apps := make([]wda.App, 0, len(infos))
	for _, a := range infos {
		apps = append(apps, wda.App{BundleID: a.CFBundleIdentifier(), Name: a.CFBundleName()})
	}
	return apps, nil
}

// Close implements deviceSession: close the forward first (stops new WDA
// traffic), then cancel the runner context.
func (s *goiosSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	fwd := s.forward
	cancel := s.cancel
	s.mu.Unlock()

	var err error
	if fwd != nil {
		err = fwd.Close()
	}
	if cancel != nil {
		cancel()
	}
	return err
}

// startGoIOS performs the real go-ios handshake and launches WDA. It returns
// as soon as the runner is launched and the forward is bound — Link.Start is
// responsible for the readiness wait and session creation.
func startGoIOS(ctx context.Context, opts Options) (deviceSession, error) {
	device, err := pickDevice(opts)
	if err != nil {
		return nil, err
	}

	// Host-side port: caller-pinned (legacy configs) or dynamically allocated.
	hostPort := opts.WDAPort
	if hostPort == 0 {
		hostPort, err = freePort()
		if err != nil {
			return nil, err
		}
	}

	s := &goiosSession{device: device, hostPort: hostPort, runnerErr: make(chan error, 1)}

	// Launch the automation runner on a cancellable context; RunTestWithConfig
	// blocks for the test lifetime and returns when the context is cancelled.
	runnerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// The runner's device-side port is fixed by the backend (DeviceKit listens
	// on 12004; WDA was pinned via USE_PORT=8100). DeviceKit does not read
	// USE_PORT — go-ios's own `ui run devicekit` forwards straight to 12004 —
	// so only the WDA driver keeps the env.
	listener := testmanagerd.NewTestListener(&s.log, &s.log, "")
	var env map[string]any
	devicePort := DeviceKitDevicePort
	if opts.RunnerDriver == "wda" {
		env = map[string]any{"USE_PORT": fmt.Sprint(wda.DeviceWDAPort)}
		devicePort = wda.DeviceWDAPort
	}
	go func() {
		_, err := testmanagerd.RunTestWithConfig(runnerCtx, testmanagerd.TestConfig{
			TestRunnerBundleId: opts.WDABundleID,
			XctestConfigName:   opts.XCTestConfig,
			Device:             device,
			Env:                env,
			Listener:           listener,
		})
		s.runnerErr <- err
	}()

	// Bind the forward immediately: go-ios accepts host connections and opens
	// the device-side socket lazily per connection, so the listener is safe to
	// create before the runner binds its port; the readiness probe retries anyway.
	fwd, err := forward.Forward(device, uint16(hostPort), uint16(devicePort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("forward runner port %d → device:%d: %w", hostPort, devicePort, err)
	}
	s.forward = fwd

	// Early-exit: if the runner dies before it ever binds, surface that rather
	// than letting the caller's readiness wait time out silently.
	select {
	case runErr := <-s.runnerErr:
		_ = s.Close()
		if runErr == nil {
			return nil, fmt.Errorf("runner exited before becoming ready (log tail: %s)", tailString(s.RunnerLog(), 600))
		}
		return nil, fmt.Errorf("runner failed: %w (log tail: %s)", runErr, tailString(s.RunnerLog(), 600))
	default:
	}

	return s, nil
}

// pickDevice selects the device by UDID or transport, defaulting to the first
// available.
func pickDevice(opts Options) (ios.DeviceEntry, error) {
	list, err := ios.ListDevices()
	if err == nil {
		for _, d := range list.DeviceList {
			if opts.UDID != "" && d.Properties.SerialNumber != opts.UDID {
				continue
			}
			if opts.Transport == "network" && d.Properties.ConnectionType != "Network" {
				continue
			}
			if opts.Transport == "usb" && d.Properties.ConnectionType == "Network" {
				continue
			}
			return enrichWithTunnel(d)
		}
	}
	// usbmuxd knows nothing about the device. A phone that is only reachable
	// over the LAN (unplugged) still has a live RemotePairing tunnel, so build
	// its entry from the tunnel-info endpoint instead of failing: that entry
	// carries Address/Rsd/UserspaceTUN, which every service below needs.
	if opts.UDID != "" && opts.Transport != "usb" {
		if d, lerr := LanDeviceEntry(opts.UDID); lerr == nil {
			return d, nil
		}
	}
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("list devices: %w", err)
	}
	return ios.DeviceEntry{}, fmt.Errorf("no device matching udid=%q transport=%q",
		opts.UDID, opts.Transport)
}

// LanDeviceEntry fabricates a device entry for a phone with no usbmuxd
// presence, from the local tunnel-info agent. It mirrors enrichWithTunnel but
// builds the entry itself — GetDeviceWithAddress is useless here since it
// re-lists usbmuxd. The UserspaceTUN fields are stamped BEFORE the RSD
// handshake because NewWithAddrPortDevice dials through them.
//
// Exported because a host that talks to the device outside a link (install,
// app listing) needs the same entry, and must not reimplement the stamping.
func LanDeviceEntry(udid string) (ios.DeviceEntry, error) {
	host := ios.HttpApiHost()
	port := ios.HttpApiPort()
	info, err := tunnel.TunnelInfoForDevice(udid, host, port)
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("lan: no RSD tunnel for %s at %s:%d: %w", udid, host, port, err)
	}
	d := ios.DeviceEntry{
		Properties: ios.DeviceProperties{
			SerialNumber:   udid,
			ConnectionType: "Network",
			// No usbmuxd DeviceID on purpose: it marks the entry as LAN-only.
			// The iOS version comes from the RSD handshake itself (go-ios's
			// fork reads it back off the provider), so nothing else is needed.
		},
		Address:          info.Address,
		UserspaceTUN:     info.UserspaceTUN,
		UserspaceTUNHost: host,
		UserspaceTUNPort: info.UserspaceTUNPort,
	}
	rsdService, err := ios.NewWithAddrPortDevice(info.Address, info.RsdPort, d)
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("lan: connect RSD for %s at [%s]:%d: %w", udid, info.Address, info.RsdPort, err)
	}
	defer rsdService.Close()
	rsdProvider, err := rsdService.Handshake()
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("lan: RSD handshake for %s at [%s]:%d: %w", udid, info.Address, info.RsdPort, err)
	}
	d.Rsd = rsdProvider
	return d, nil
}

// enrichWithTunnel fills the RSD tunnel details on iOS 17+ devices, which the
// XCTest runner (RunTestWithConfig → NewTunnelConnection) requires to reach the
// device's remote services. ios.ListDevices() never populates DeviceEntry.Rsd —
// that only comes from a running go-ios tunnel agent — so a bare iOS 17+ device
// makes RunTestWithConfig fail with "missing tunnel address and RSD port".
//
// Older iOS (<17) tunnels over usbmuxd directly and needs no enrichment, so the
// device is returned untouched. On 17+ the local tunnel-info agent (started by
// node-ios) is queried; its address/RSD port are attached exactly as the go-ios
// CLI's deviceWithRsdProvider does. A missing or not-yet-ready tunnel is a hard
// error here rather than a silent pass-through: launching WDA without it would
// only fail later with a far less actionable message.
func enrichWithTunnel(d ios.DeviceEntry) (ios.DeviceEntry, error) {
	if d.SupportsRsd() {
		return d, nil // already has an RSD provider (e.g. injected by a caller)
	}

	udid := d.Properties.SerialNumber
	host := ios.HttpApiHost()
	port := ios.HttpApiPort()

	// Ask for a tunnel FIRST, for USB and Network alike: a Network-type usbmuxd
	// entry may well be the same phone the node's LAN agent tunnels
	// (Wi-Fi-synced). Only when there is no tunnel do we fall back.
	info, err := tunnel.TunnelInfoForDevice(udid, host, port)
	if err != nil {
		if !errors.Is(err, tunnel.ErrTunnelNotFound) {
			return ios.DeviceEntry{}, fmt.Errorf("query tunnel info for %s at %s:%d: %w", udid, host, port, err)
		}
		if d.Properties.ConnectionType == "Network" {
			// Legacy Wi-Fi-sync device with no LAN tunnel: go-ios reaches its
			// lockdown services over the network mux. iOS 17+ services
			// (testmanagerd) will fail later with a clearer message.
			return d, nil
		}
		version, verr := ios.GetProductVersion(d)
		if verr == nil && version.Major() < 17 {
			// iOS <17 tunnels over usbmuxd directly; no RSD tunnel is involved.
			return d, nil
		}
		if verr != nil {
			return ios.DeviceEntry{}, fmt.Errorf("read iOS version for %s: %w", udid, verr)
		}
		return ios.DeviceEntry{}, fmt.Errorf(
			"no RSD tunnel for iOS %s device %s: the go-ios tunnel agent has not started it yet (userspace tunnels need iOS 17.4+; 17.0–17.3 require a root tunnel agent)",
			version.String(), udid)
	}

	// Replicate go-ios CLI deviceWithRsdProvider: dial the device over the
	// tunnel, RSD-handshake to learn its service ports, and stamp them onto the
	// entry. UserspaceTUN details come from the tunnel-info response (the agent
	// runs userspace tunnels), and the userspace host is the agent host.
	d.UserspaceTUN = info.UserspaceTUN
	d.UserspaceTUNHost = host
	d.UserspaceTUNPort = info.UserspaceTUNPort

	rsdService, err := ios.NewWithAddrPortDevice(info.Address, info.RsdPort, d)
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("connect RSD for %s at [%s]:%d: %w", udid, info.Address, info.RsdPort, err)
	}
	defer rsdService.Close()
	rsdProvider, err := rsdService.Handshake()
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("RSD handshake for %s at [%s]:%d: %w", udid, info.Address, info.RsdPort, err)
	}
	// The entry may be a LAN fabrication (no usbmuxd record to re-list), so
	// stamp the provider here instead of re-resolving through GetDeviceWithAddress.
	d.Address = info.Address
	d.Rsd = rsdProvider
	return d, nil
}
