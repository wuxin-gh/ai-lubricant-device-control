// Package devicelink bridges the wda.Client interface onto a real iPhone via
// go-ios: device discovery (USB or network), launching/maintaining the
// WebDriverAgent XCTest runner, port-forwarding WDA's HTTP server to the host,
// and issuing the WebDriver REST calls that implement each wda.Client method.
//
// This is the one package that talks to real iOS. The WDA HTTP protocol itself
// lives in internal/wda (session lifecycle + route table + W3C action
// payloads); this file owns the *device* lifecycle and delegates every
// wda.Client call to that client.
//
// go-ios API surface used (v1.3.2), all in start_goios.go:
//   - ios.ListDevices() → DeviceList (ConnectionType "USB" or "Network")
//   - testmanagerd.RunTestWithConfig → launches the WDA runner and maintains
//     the XCTest session for the lifetime of the passed context
//   - forward.Forward(device, hostPort, phonePort) → localhost TCP forward
//   - installationproxy.BrowseUserApps/BrowseSystemApps → installed app list
//
// REAL-DEVICE GATE: the sequence is implemented against the go-ios v1.3.2 API
// but has not been exercised against a live iPhone. Everything above the
// deviceSession seam is covered by unit tests (fake session + fake WDA HTTP
// server); what a device would prove is that go-ios's runner/forward calls
// behave as documented on the target iOS version.
package devicelink

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// Options configures a device link.
type Options struct {
	// UDID selects the device; if empty, the first available is used.
	UDID string
	// Transport is "usb" (default) or "network".
	Transport string
	// WDABundleID is the WebDriverAgentRunner bundle id (signed build).
	WDABundleID string
	// XCTestConfig is the .xctest config name inside the runner.
	XCTestConfig string
	// WDAPort pins the host-side forwarded port. 0 (recommended) allocates a
	// free port, so N iPhones on one host never collide. The device-side port
	// is fixed by launching the runner with USE_PORT (see wda.DeviceWDAPort),
	// which removes the old "read the port WDA printed" step entirely.
	WDAPort int
	// HTTPTimeout bounds each WDA REST call (default 30s).
	HTTPTimeout time.Duration
	// ReadyTimeout bounds how long Start waits for WDA to answer /status after
	// the runner is launched (default 90s: a cold XCTest runner start plus
	// device unlock is slow).
	ReadyTimeout time.Duration
}

// deviceSession is the go-ios side of a link: the launched runner plus the
// port forward. It is an interface so Link is testable without a device —
// start_goios.go holds the only implementation that touches go-ios.
type deviceSession interface {
	// HostPort is the localhost port WDA is reachable on.
	HostPort() int
	// ListApps returns installed apps via installationproxy.
	ListApps(ctx context.Context, includeSystem bool) ([]wda.App, error)
	// RunnerLog returns whatever the XCTest runner printed so far — the first
	// place to look when WDA never becomes ready.
	RunnerLog() string
	// Close stops the runner and closes the forward. Idempotent.
	Close() error
}

// startSession is indirected for tests: a fake session replaces the go-ios
// handshake, so Link's lifecycle (ready-wait, session create, teardown order)
// is exercised without hardware.
var startSession = startGoIOS

// Link is a wda.Client backed by a real iPhone over go-ios.
type Link struct {
	opts   Options
	client *wda.HTTPClient

	mu      sync.Mutex
	session deviceSession
	closed  bool
}

// New validates options; it does not touch the device. Call Start to connect.
func New(opts Options) *Link {
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 30 * time.Second
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 90 * time.Second
	}
	if opts.Transport == "" {
		opts.Transport = "usb"
	}
	return &Link{opts: opts}
}

// Start picks the device, launches WDA, forwards its port, waits for the HTTP
// server, and creates the WebDriver session. On success the link is ready to
// serve wda.Client calls; on any failure everything started so far is torn
// down before returning, so a caller may retry Start on a fresh Link without
// leaking a runner or a listening port.
func (l *Link) Start(ctx context.Context) error {
	if l.opts.WDABundleID == "" || l.opts.XCTestConfig == "" {
		return fmt.Errorf("devicelink start: WDABundleID and XCTestConfig are required (see README)")
	}
	sess, err := startSession(ctx, l.opts)
	if err != nil {
		return fmt.Errorf("devicelink start: %w", err)
	}

	l.mu.Lock()
	l.session = sess
	l.mu.Unlock()

	client := wda.NewHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", sess.HostPort()), l.opts.HTTPTimeout)
	if err := waitForReady(ctx, client, l.opts.ReadyTimeout); err != nil {
		log := sess.RunnerLog()
		_ = l.teardown()
		if log != "" {
			return fmt.Errorf("devicelink start: WDA did not become ready: %w (runner log tail: %s)", err, tailString(log, 600))
		}
		return fmt.Errorf("devicelink start: WDA did not become ready: %w", err)
	}
	if err := client.CreateSession(ctx); err != nil {
		_ = l.teardown()
		return fmt.Errorf("devicelink start: %w", err)
	}

	l.mu.Lock()
	l.client = client
	l.mu.Unlock()
	return nil
}

// Close deletes the WDA session, then closes the forward and stops the runner.
// Order matters: deleting the session needs the forward still up.
func (l *Link) Close() error {
	l.mu.Lock()
	client := l.client
	l.mu.Unlock()
	if client != nil {
		// Bounded: teardown must not hang on an unresponsive runner.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client.Close(ctx)
		cancel()
	}
	return l.teardown()
}

// teardown stops the go-ios side exactly once.
func (l *Link) teardown() error {
	l.mu.Lock()
	sess := l.session
	already := l.closed
	l.closed = true
	l.session = nil
	l.client = nil
	l.mu.Unlock()
	if sess == nil || already {
		return nil
	}
	return sess.Close()
}

// wdaClient returns the live client or an error when Start has not run.
func (l *Link) wdaClient() (*wda.HTTPClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client == nil {
		return nil, fmt.Errorf("devicelink: not started")
	}
	return l.client, nil
}

// ── wda.Client surface (delegated) ───────────────────────────────────────

func (l *Link) Source(ctx context.Context) (*screen.MultiWindowResult, error) {
	c, err := l.wdaClient()
	if err != nil {
		return nil, err
	}
	return c.Source(ctx)
}

func (l *Link) Screenshot(ctx context.Context) ([]byte, error) {
	c, err := l.wdaClient()
	if err != nil {
		return nil, err
	}
	return c.Screenshot(ctx)
}

func (l *Link) ScreenInfo(ctx context.Context) (screen.ScreenInfo, int, error) {
	c, err := l.wdaClient()
	if err != nil {
		return screen.ScreenInfo{}, 0, err
	}
	return c.ScreenInfo(ctx)
}

func (l *Link) Tap(ctx context.Context, x, y, durationMS int) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Tap(ctx, x, y, durationMS)
}

func (l *Link) DoubleTap(ctx context.Context, x, y int) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.DoubleTap(ctx, x, y)
}

func (l *Link) Swipe(ctx context.Context, x1, y1, x2, y2, durationMS int) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Swipe(ctx, x1, y1, x2, y2, durationMS)
}

func (l *Link) Scroll(ctx context.Context, direction string) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Scroll(ctx, direction)
}

func (l *Link) TypeText(ctx context.Context, text string) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.TypeText(ctx, text)
}

func (l *Link) SetValue(ctx context.Context, nodeID, text string) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.SetValue(ctx, nodeID, text)
}

func (l *Link) Key(ctx context.Context, key string) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Key(ctx, key)
}

func (l *Link) DismissKeyboard(ctx context.Context) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.DismissKeyboard(ctx)
}

func (l *Link) Home(ctx context.Context) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Home(ctx)
}

func (l *Link) Recents(ctx context.Context) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.Recents(ctx)
}

// ListApps goes to go-ios installationproxy rather than WDA: the app-list
// route differs across WDA forks, while installationproxy is a stable device
// service.
func (l *Link) ListApps(ctx context.Context, includeSystem bool) ([]wda.App, error) {
	l.mu.Lock()
	sess := l.session
	l.mu.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("devicelink: not started")
	}
	return sess.ListApps(ctx, includeSystem)
}

func (l *Link) LaunchApp(ctx context.Context, bundleID string) error {
	c, err := l.wdaClient()
	if err != nil {
		return err
	}
	return c.LaunchApp(ctx, bundleID)
}

// ── helpers ──────────────────────────────────────────────────────────────

// waitForReady polls WDA /status until it answers or the budget expires. A
// freshly launched XCTest runner takes seconds to bind its port, and the
// forward accepts connections before WDA listens, so a single probe would
// almost always fail.
func waitForReady(ctx context.Context, c *wda.HTTPClient, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var last error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.Status(probeCtx)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %w", budget, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// freePort asks the OS for an unused TCP port. There is an inherent race
// between closing the probe listener and the forward binding it; the window is
// microseconds and the alternative (a fixed port per device) collides far more
// often on a multi-phone host.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate host port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, fmt.Errorf("release probe listener: %w", err)
	}
	return port, nil
}

func tailString(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Compile-time check that Link satisfies wda.Client.
var _ wda.Client = (*Link)(nil)
