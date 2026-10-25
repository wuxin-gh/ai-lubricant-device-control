// DeviceKit JSON-RPC runner client.
//
// go-ios 1.3.2 ships the DeviceKit runner (the new default UI automation
// backend, replacing WebDriverAgent): a device-side app whose XCTest runner
// serves JSON-RPC 2.0 on device port 12004. The host forwards that port
// (devicelink owns the forward) and speaks:
//
//	POST /rpc  {"jsonrpc":"2.0","method":"device.io.tap","params":{...},"id":1}
//	GET  /health
//
// The method names and param shapes mirror go-ios's cmd_ui.go devicekit client
// exactly (that CLI is the reference consumer), so a divergence found on a
// real device is a one-line fix in the method table below.
//
// This file implements the same wda.Client interface the command handlers
// depend on, so commands.go needs zero changes. Methods DeviceKit does not
// expose (SetValue, DismissKeyboard) return a stable unsupported error rather
// than approximating with taps — a wrong tap is a side effect, an explicit
// error is not.
//
// REAL-DEVICE GATE: the RPC surface is taken from go-ios v1.3.2 source
// (cmd_ui.go); the device.dump.ui response schema is NOT documented upstream,
// so Source() probes the format at runtime (XML → reuse the WDA parser;
// JSON → adapt; unknown → structured error with a body tail for the log).
package devicelink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// DeviceKit device-side constants. The runner always listens on 12004 (go-ios
// forwards there without setting USE_PORT), so the device-side endpoint is
// deterministic; the host-side forwarded port is what this client dials and
// is allocated by devicelink.
const (
	DeviceKitDevicePort = 12004
	DeviceKitHealthPath = "/health"
	DeviceKitRPCPath    = "/rpc"
)

// DeviceKit RPC method names (go-ios cmd_ui.go reference client).
const (
	dkTap         = "device.io.tap"
	dkLongpress   = "device.io.longpress"
	dkSwipe       = "device.io.swipe"
	dkText        = "device.io.text"
	dkButton      = "device.io.button"
	dkScreenshot  = "device.screenshot"
	dkDumpUI      = "device.dump.ui"
	dkInfo        = "device.info"
	dkAppsLaunch  = "device.apps.launch"
	dkAppsTerm    = "device.apps.terminate"
	dkOrientation = "device.io.orientation.get"
)

// ErrUnsupported is returned for wda.Client methods DeviceKit has no RPC for.
// The command layer surfaces it as a stable wire error; it never approximates
// the operation with synthesized taps.
var ErrUnsupported = wda.ErrUnsupported

// DeviceKitClient speaks JSON-RPC 2.0 to the forwarded DeviceKit runner port.
// Safe for concurrent use.
type DeviceKitClient struct {
	baseURL string
	http    *http.Client

	mu    sync.Mutex
	scale int
	info  screen.ScreenInfo
	// sourceIsXML caches the result of the first dump-format probe so repeated
	// Source() calls do not re-probe.
	sourceIsXML *bool
}

// NewDeviceKitClient dials the DeviceKit runner at baseURL (the forwarded host
// port). It performs no I/O until Status/Source.
func NewDeviceKitClient(baseURL string, timeout time.Duration) *DeviceKitClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &DeviceKitClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
		scale:   2,
	}
}

// Status reports whether the runner is up (GET /health). It creates no state.
func (c *DeviceKitClient) Status(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+DeviceKitHealthPath, nil)
	if err != nil {
		return fmt.Errorf("devicekit health: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("devicekit health: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("devicekit health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// CreateSession is a no-op: DeviceKit is sessionless. Kept so the runnerClient
// interface is uniform with the WDA client.
func (c *DeviceKitClient) CreateSession(ctx context.Context) error { return nil }

// Close is a no-op: nothing to tear down server-side.
func (c *DeviceKitClient) Close(ctx context.Context) {
	c.mu.Lock()
	c.sourceIsXML = nil
	c.mu.Unlock()
}

// Session is always "" — DeviceKit is sessionless. Exists so DeviceKitClient
// satisfies the runnerClient interface alongside the WDA backend.
func (c *DeviceKitClient) Session() string { return "" }

// rpc issues one JSON-RPC call and unmarshals result into out (when non-nil).
func (c *DeviceKitClient) rpc(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		return fmt.Errorf("devicekit %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+DeviceKitRPCPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("devicekit %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("devicekit %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("devicekit %s: read body: %w", method, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("devicekit %s: HTTP %d: %s", method, resp.StatusCode, tailBytes(raw, 300))
	}
	var envelope struct {
		Result  json.RawMessage `json:"result"`
		Error   *dkRPCError      `json:"error"`
		ID      json.RawMessage  `json:"id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("devicekit %s: bad rpc envelope: %w (body tail: %s)", method, err, tailBytes(raw, 200))
	}
	if envelope.Error != nil {
		return fmt.Errorf("devicekit %s: rpc error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out == nil {
		return nil
	}
	result := bytes.TrimSpace(envelope.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(result, out); err != nil {
		// Some methods return bare strings; only error when out expects JSON.
		return fmt.Errorf("devicekit %s: decode result: %w", method, err)
	}
	return nil
}

// dkRPCError is the JSON-RPC 2.0 error object.
type dkRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── wda.Client ───────────────────────────────────────────────────────────

// toPoints divides physical pixels by the device scale (min 1). DeviceKit
// takes POINTS, same as WDA.
func (c *DeviceKitClient) toPoints(x, y int) (int, int) {
	s := c.Scale()
	if s <= 0 {
		s = 1
	}
	return x / s, y / s
}

func (c *DeviceKitClient) Tap(ctx context.Context, x, y, durationMS int) error {
	px, py := c.toPoints(x, y)
	if durationMS > 0 {
		return c.rpc(ctx, dkLongpress, map[string]any{"x": px, "y": py, "duration": float64(durationMS) / 1000.0}, nil)
	}
	return c.rpc(ctx, dkTap, map[string]any{"x": px, "y": py}, nil)
}

func (c *DeviceKitClient) DoubleTap(ctx context.Context, x, y int) error {
	px, py := c.toPoints(x, y)
	// Two taps in quick succession — DeviceKit has no dedicated double-tap RPC.
	if err := c.rpc(ctx, dkTap, map[string]any{"x": px, "y": py}, nil); err != nil {
		return err
	}
	// Best-effort gap: on cancellation the second tap is skipped, which
	// degrades to a single tap instead of hanging the handler.
	select {
	case <-time.After(80 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.rpc(ctx, dkTap, map[string]any{"x": px, "y": py}, nil)
}

func (c *DeviceKitClient) Swipe(ctx context.Context, x1, y1, x2, y2, durationMS int) error {
	ax, ay := c.toPoints(x1, y1)
	bx, by := c.toPoints(x2, y2)
	return c.rpc(ctx, dkSwipe, map[string]any{
		"fromX": ax, "fromY": ay, "toX": bx, "toY": by,
		"duration": float64(durationMS) / 1000.0,
	}, nil)
}

// Scroll synthesizes a direction swipe from the screen centre. screenSnapshot
// is physical pixels now, but the DeviceKit swipe RPC takes points, so the
// centre is scaled back.
func (c *DeviceKitClient) Scroll(ctx context.Context, direction string) error {
	info := c.screenSnapshot()
	if info.Width <= 0 || info.Height <= 0 {
		return fmt.Errorf("devicekit scroll: unknown screen size (info not probed?)")
	}
	cx, cy := c.toPoints(info.Width/2, info.Height/2)
	_, mag := c.toPoints(0, info.Height/4)
	var dx, dy int
	switch direction {
	case "up":
		dy = mag
	case "down":
		dy = -mag
	case "left":
		dx = mag
	case "right":
		dx = -mag
	default:
		return fmt.Errorf("devicekit scroll: unsupported direction %q", direction)
	}
	return c.rpc(ctx, dkSwipe, map[string]any{
		"fromX": cx, "fromY": cy, "toX": cx + dx, "toY": cy + dy,
		"duration": 0.3,
	}, nil)
}

func (c *DeviceKitClient) TypeText(ctx context.Context, text string) error {
	return c.rpc(ctx, dkText, map[string]any{"text": text}, nil)
}

// SetValue is not exposed by DeviceKit; refuse rather than synthesize taps.
func (c *DeviceKitClient) SetValue(ctx context.Context, nodeID, text string) error {
	return fmt.Errorf("%w: devicekit has no element set-value rpc", ErrUnsupported)
}

// Key maps the §8.6 text keys DeviceKit can type; hardware/special keys have
// no RPC (button covers home/volume on DeviceKit but the wire spec's dpad/escape
// are not confirmed safe — refuse them rather than typing control characters).
func (c *DeviceKitClient) Key(ctx context.Context, key string) error {
	name, ok := dkTextKey(key)
	if !ok {
		return fmt.Errorf("%w: devicekit cannot synthesize key %q", ErrUnsupported, key)
	}
	return c.rpc(ctx, dkText, map[string]any{"text": name}, nil)
}

// DismissKeyboard has no DeviceKit RPC; refuse.
func (c *DeviceKitClient) DismissKeyboard(ctx context.Context) error {
	return fmt.Errorf("%w: devicekit has no dismiss-keyboard rpc", ErrUnsupported)
}

// Home presses the home button via device.io.button.
func (c *DeviceKitClient) Home(ctx context.Context) error {
	return c.rpc(ctx, dkButton, map[string]any{"button": "home"}, nil)
}

// Recents approximates the app-switcher with a bottom-up swipe. DeviceKit has
// no dedicated recents RPC; if this proves unstable on a real device, swap the
// body for an unsupported error (the switch here is one line).
func (c *DeviceKitClient) Recents(ctx context.Context) error {
	info := c.screenSnapshot()
	if info.Width <= 0 || info.Height <= 0 {
		return fmt.Errorf("devicekit recents: unknown screen size (info not probed?)")
	}
	// The swipe RPC takes points; screenSnapshot is physical pixels now.
	cx, bottomY := c.toPoints(info.Width/2, info.Height-10)
	_, midY := c.toPoints(0, info.Height/2)
	return c.rpc(ctx, dkSwipe, map[string]any{
		"fromX": cx, "fromY": bottomY, "toX": cx, "toY": midY,
		"duration": 0.35,
	}, nil)
}

func (c *DeviceKitClient) LaunchApp(ctx context.Context, bundleID string) error {
	return c.rpc(ctx, dkAppsLaunch, map[string]any{"bundleId": bundleID}, nil)
}

// ForegroundApp reads the frontmost app from DeviceKit. The go-ios reference
// client (cmd_ui.go) exposes device.apps.foreground; a runner without it fails
// here and the command layer maps the error onto unsupported.
func (c *DeviceKitClient) ForegroundApp(ctx context.Context) (string, error) {
	var reply struct {
		BundleID string `json:"bundleId"`
	}
	if err := c.rpc(ctx, "device.apps.foreground", map[string]any{}, &reply); err != nil {
		return "", err
	}
	if reply.BundleID == "" {
		return "", fmt.Errorf("devicekit apps.foreground: empty bundleId")
	}
	return reply.BundleID, nil
}

// OpenURL has no DeviceKit RPC in the reference runner; refuse rather than
// approximate by launching Safari + typing (which is fragile and not a real
// "open URL" — it would silently mistype and leave the user in a text field).
func (c *DeviceKitClient) OpenURL(ctx context.Context, url string) error {
	return fmt.Errorf("%w: devicekit has no open-url rpc", ErrUnsupported)
}

func (c *DeviceKitClient) Screenshot(ctx context.Context) ([]byte, error) {
	var b64 string
	if err := c.rpc(ctx, dkScreenshot, map[string]any{}, &b64); err != nil {
		return nil, err
	}
	return decodeBase64PNG(b64)
}

func (c *DeviceKitClient) Source(ctx context.Context) (*screen.MultiWindowResult, error) {
	// device.dump.ui may return a string (XML payload, like go-ios's ui source)
	// or a structured object (JSON tree). Decode into json.RawMessage first so
	// a string and an object both survive the round-trip, then dispatch on the
	// content. This is the one place that knows the dump payload shape.
	var raw json.RawMessage
	if err := c.rpc(ctx, dkDumpUI, map[string]any{}, &raw); err != nil {
		return nil, err
	}
	return c.parseSource(raw, c.Scale())
}

// parseSource adapts the dump payload. The first call probes the format:
// XML (WDA-shaped XCUIElement tree) reuses the WDA parser; JSON is adapted by
// field name (helpers.go); anything else is a structured error with a body tail.
// raw may be a JSON string (whose content is the XML payload) or a JSON
// object/array (the JSON tree itself).
func (c *DeviceKitClient) parseSource(raw []byte, scale int) (*screen.MultiWindowResult, error) {
	c.mu.Lock()
	cached := c.sourceIsXML
	c.mu.Unlock()

	payload := raw
	// A JSON string result carries the payload inside the quotes.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		payload = []byte(asString)
	}

	trimmed := bytes.TrimSpace(payload)
	isXML := bytes.HasPrefix(trimmed, []byte("<"))
	if cached != nil && *cached != isXML && !isXML {
		// Previously detected JSON; honor it (XML never flips to JSON).
		isXML = false
	}
	if isXML {
		t := true
		c.mu.Lock()
		c.sourceIsXML = &t
		c.mu.Unlock()
		return wda.ParseSource(payload, scale)
	}
	t := false
	c.mu.Lock()
	c.sourceIsXML = &t
	c.mu.Unlock()
	return parseDeviceKitJSONSource(payload, scale)
}

func (c *DeviceKitClient) ScreenInfo(ctx context.Context) (screen.ScreenInfo, int, error) {
	// Probe once (like CreateSession does for WDA); later calls use the cache.
	c.mu.Lock()
	haveInfo := c.info.Width > 0
	c.mu.Unlock()
	if !haveInfo {
		c.probeScreenInfo(ctx)
	}
	return c.screenSnapshot(), c.Scale(), nil
}

// probeScreenInfo fills scale + window geometry from device.info /
// device.io.orientation.get. Best-effort: failures keep defaults (2x scale).
func (c *DeviceKitClient) probeScreenInfo(ctx context.Context) {
	var info struct {
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		Scale       float64 `json:"scale"`
		PixelRatio  float64 `json:"pixelRatio"`
		Orientation string  `json:"orientation"`
	}
	if err := c.rpc(ctx, dkInfo, map[string]any{}, &info); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Scale first: device.info reports points, and width/height must be
	// multiplied by the FINAL scale to reach physical pixels.
	ratio := info.Scale
	if ratio <= 0 {
		ratio = info.PixelRatio
	}
	if ratio >= 1 {
		c.scale = int(ratio)
	}
	if c.scale <= 0 {
		c.scale = 2
	}
	if info.Width > 0 {
		// device.info reports points; the wire spec (§1, §8.1) wants physical
		// pixels and the tree bounds are already scaled, so scale here too.
		c.info.Width = info.Width * c.scale
		c.info.Height = info.Height * c.scale
	}
	switch {
	case c.scale == 2:
		c.info.DensityDpi = 320
	case c.scale >= 3:
		c.info.DensityDpi = 480
	default:
		c.info.DensityDpi = 160 * c.scale
	}
	if info.Orientation == "landscape" {
		c.info.Orientation = "landscape"
	} else {
		c.info.Orientation = "portrait"
	}
}

// Scale reports the device pixel ratio (2 or 3 on real hardware).
func (c *DeviceKitClient) Scale() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scale
}

func (c *DeviceKitClient) screenSnapshot() screen.ScreenInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// ListApps stays on installationproxy (a stable lockdown service) — same as
// the WDA path; the devicelink.Link override serves it. The method exists so
// DeviceKitClient satisfies wda.Client for direct (test) use.
func (c *DeviceKitClient) ListApps(ctx context.Context, includeSystem bool) ([]wda.App, error) {
	return nil, fmt.Errorf("devicekit: ListApps not supported over rpc; use devicelink (installationproxy)")
}

// dkTextKey maps §8.6 key names onto literal text DeviceKit can type. Only
// keys with a confirmed textual representation are mapped.
func dkTextKey(key string) (string, bool) {
	switch key {
	case "enter", "return":
		return "\n", true
	case "tab":
		return "\t", true
	case "space":
		return " ", true
	}
	return "", false
}

// Compile-time check that DeviceKitClient satisfies Client.
var _ wda.Client = (*DeviceKitClient)(nil)

// tailBytes returns the last n bytes of b as a string (for error messages).
func tailBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
