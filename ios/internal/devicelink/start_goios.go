package devicelink

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/forward"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"

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

	// Launch the WDA runner on a cancellable context; RunTestWithConfig blocks
	// for the test lifetime and returns when the context is cancelled.
	wdaCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// USE_PORT pins WDA's device-side HTTP port so the forward target is
	// deterministic (no log parsing). WDA reads USE_PORT on startup.
	listener := testmanagerd.NewTestListener(&s.log, &s.log, "")
	env := map[string]any{"USE_PORT": fmt.Sprint(wda.DeviceWDAPort)}
	go func() {
		_, err := testmanagerd.RunTestWithConfig(wdaCtx, testmanagerd.TestConfig{
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
	// create before WDA binds its port; the readiness probe retries anyway.
	fwd, err := forward.Forward(device, uint16(hostPort), uint16(wda.DeviceWDAPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("forward WDA port %d → device:%d: %w", hostPort, wda.DeviceWDAPort, err)
	}
	s.forward = fwd

	// Early-exit: if the runner dies before WDA ever binds, surface that rather
	// than letting the caller's readiness wait time out silently.
	select {
	case runErr := <-s.runnerErr:
		_ = s.Close()
		if runErr == nil {
			return nil, fmt.Errorf("WDA runner exited before becoming ready (log tail: %s)", tailString(s.RunnerLog(), 600))
		}
		return nil, fmt.Errorf("WDA runner failed: %w (log tail: %s)", runErr, tailString(s.RunnerLog(), 600))
	default:
	}

	return s, nil
}

// pickDevice selects the device by UDID or transport, defaulting to the first
// available.
func pickDevice(opts Options) (ios.DeviceEntry, error) {
	list, err := ios.ListDevices()
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("list devices: %w", err)
	}
	if len(list.DeviceList) == 0 {
		return ios.DeviceEntry{}, fmt.Errorf("no iOS device found (is usbmuxd running?)")
	}
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
		return d, nil
	}
	return ios.DeviceEntry{}, fmt.Errorf("no device matching udid=%q transport=%q",
		opts.UDID, opts.Transport)
}
