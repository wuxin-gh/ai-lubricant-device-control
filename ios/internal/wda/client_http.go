// Package wda — HTTPClient is the session-aware WebDriverAgent client backed
// by the forwarded WDA HTTP port. It implements the Client interface the
// command handlers depend on and replaces the pre-alpha root-route calls:
// every interaction is session-scoped and touch synthesis goes through W3C
// Actions only (see routes.go for the route contract).
//
// Lifecycle: New → Status (readiness) → CreateSession → ready. Close deletes
// the session (best-effort). An expired session mid-call is rebuilt once and
// the call retried for read-only routes; UI-affecting calls are not retried
// (§8 semantics: side effects must not double-fire).
package wda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"device-control/ios/internal/screen"
)

// HTTPClient speaks WebDriverAgent over HTTP. Safe for concurrent use.
type HTTPClient struct {
	baseURL string
	http    *http.Client

	mu      sync.Mutex
	session string
	scale   int
	info    screen.ScreenInfo
}

// NewHTTPClient dials WDA at baseURL (the forwarded host port). It performs
// no I/O until Status/CreateSession.
func NewHTTPClient(baseURL string, timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
		scale:   2,
	}
}

// Status reports whether WDA is up. It does not create a session.
func (c *HTTPClient) Status(ctx context.Context) error {
	var raw json.RawMessage
	_, err := c.do(ctx, http.MethodGet, routeStatus, nil, &raw)
	return err
}

// CreateSession creates the WebDriver session and fills scale/screen info
// from the capabilities + window size. Call Status first for a friendlier
// error when the runner is not up.
func (c *HTTPClient) CreateSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" {
		return nil // idempotent: one session per client lifetime
	}
	// do() unwraps {"value": ...}, so the shape here is the session payload
	// itself, not the envelope.
	var reply struct {
		SessionID    string         `json:"sessionId"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if _, err := c.do(ctx, http.MethodPost, routeNewSession, NewSessionPayload(), &reply); err != nil {
		return fmt.Errorf("wda create session: %w", err)
	}
	if reply.SessionID == "" {
		return fmt.Errorf("wda create session: empty sessionId")
	}
	c.session = reply.SessionID
	c.readScreenInfo(ctx)
	return nil
}

// Close deletes the session (best-effort) and drops cached screen info.
func (c *HTTPClient) Close(ctx context.Context) {
	c.mu.Lock()
	s := c.session
	c.session = ""
	c.mu.Unlock()
	if s == "" {
		return
	}
	_, _ = c.do(ctx, http.MethodDelete, routeDeleteSession(s), nil, nil)
}

// Session returns the live session id ("" when none).
func (c *HTTPClient) Session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// readScreenInfo probes window size + pixel ratio. Best-effort: failures keep
// the defaults (points-sized window, 2× scale) and the caller sees a usable
// but possibly mis-scaled link. REAL-DEVICE GATE: WDA's pixelRatio surface is
// /session/{id}/wda/screen {statusBarSize, pixelRatio}; a fork without it
// falls back to the scale default.
func (c *HTTPClient) readScreenInfo(ctx context.Context) {
	var size struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if _, err := c.do(ctx, http.MethodGet, routeWindowSize(c.session), nil, &size); err == nil && size.Width > 0 {
		c.info.Width = size.Width
		c.info.Height = size.Height
	}
	var scr struct {
		PixelRatio float64 `json:"pixelRatio"`
	}
	if _, err := c.do(ctx, http.MethodGet, routeScreen(c.session), nil, &scr); err == nil && scr.PixelRatio >= 1 {
		c.scale = int(scr.PixelRatio)
	}
	if c.scale <= 0 {
		c.scale = 2
	}
	switch {
	case c.scale == 2:
		c.info.DensityDpi = 320
	case c.scale >= 3:
		c.info.DensityDpi = 480
	default:
		c.info.DensityDpi = 160 * c.scale
	}
	c.info.Orientation = "portrait"
}

// Scale reports the device pixel ratio (2 or 3 on real hardware).
func (c *HTTPClient) Scale() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scale
}

// ScreenInfo returns the probed window geometry.
func (c *HTTPClient) screenSnapshot() screen.ScreenInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// ── wda.Client ───────────────────────────────────────────────────────────

func (c *HTTPClient) Source(ctx context.Context) (*screen.MultiWindowResult, error) {
	// WDA returns /source as a JSON string (the XML), not an object, so decode
	// into a string — unmarshaling a JSON string straight into []byte would
	// base64-decode it.
	var s string
	if err := c.sessionCall(ctx, true, func() error {
		_, err := c.do(ctx, http.MethodGet, routeSource(c.session), nil, &s)
		return err
	}); err != nil {
		return nil, err
	}
	return ParseSource([]byte(s), c.Scale())
}

func (c *HTTPClient) Screenshot(ctx context.Context) ([]byte, error) {
	var b64 string
	if err := c.sessionCall(ctx, true, func() error {
		_, err := c.do(ctx, http.MethodGet, routeScreenshot(c.session), nil, &b64)
		return err
	}); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(b64)
}

func (c *HTTPClient) ScreenInfo(ctx context.Context) (screen.ScreenInfo, int, error) {
	return c.screenSnapshot(), c.Scale(), nil
}

func (c *HTTPClient) Tap(ctx context.Context, x, y, durationMS int) error {
	px, py := c.toPoints(x, y)
	if durationMS > 0 {
		return c.sessionCall(ctx, false, func() error {
			_, err := c.do(ctx, http.MethodPost, routeActions(c.session), LongPressPayload(px, py, durationMS), nil)
			return err
		})
	}
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeActions(c.session), TapPayload(px, py), nil)
		return err
	})
}

func (c *HTTPClient) DoubleTap(ctx context.Context, x, y int) error {
	px, py := c.toPoints(x, y)
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeActions(c.session), DoubleTapPayload(px, py), nil)
		return err
	})
}

func (c *HTTPClient) Swipe(ctx context.Context, x1, y1, x2, y2, durationMS int) error {
	ax, ay := c.toPoints(x1, y1)
	bx, by := c.toPoints(x2, y2)
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeActions(c.session), SwipePayload(ax, ay, bx, by, durationMS), nil)
		return err
	})
}

// Scroll synthesizes a direction swipe from the screen centre. It works in
// POINTS throughout (screenSnapshot is points, the payload wants points) and
// therefore posts the action itself instead of going through Swipe, which
// expects physical pixels and would scale the values a second time.
func (c *HTTPClient) Scroll(ctx context.Context, direction string) error {
	info := c.screenSnapshot()
	if info.Width <= 0 || info.Height <= 0 {
		return fmt.Errorf("wda scroll: unknown screen size (session not created?)")
	}
	// Swiping the content up reveals what is below, so "down" moves the finger
	// up. Quarter-screen reach, matching the Android driver's feel.
	mag := info.Height / 4
	cx, cy := info.Width/2, info.Height/2
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
		return fmt.Errorf("wda scroll: unsupported direction %q", direction)
	}
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeActions(c.session),
			SwipePayload(cx, cy, cx+dx, cy+dy, 300), nil)
		return err
	})
}

func (c *HTTPClient) TypeText(ctx context.Context, text string) error {
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeKeys(c.session), KeyPayload(text), nil)
		return err
	})
}

// SetValue resolves nodeID (an accessibility id from the §8.2 tree) to a WDA
// element and sets its value.
func (c *HTTPClient) SetValue(ctx context.Context, nodeID, text string) error {
	return c.sessionCall(ctx, false, func() error {
		eid, err := c.findElement(ctx, nodeID)
		if err != nil {
			return err
		}
		_, err = c.do(ctx, http.MethodPost, routeElementValue(c.session, eid), SetValuePayload(text), nil)
		return err
	})
}

func (c *HTTPClient) Key(ctx context.Context, key string) error {
	name, ok := keyCodeName(key)
	if !ok {
		return fmt.Errorf("wda key: unsupported key %q", key)
	}
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeKeys(c.session), KeyPayload(name), nil)
		return err
	})
}

func (c *HTTPClient) DismissKeyboard(ctx context.Context) error {
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeDismissKeyboard(c.session), nil, nil)
		return err
	})
}

func (c *HTTPClient) Home(ctx context.Context) error {
	// Appium WDA exposes a session-less homescreen shortcut; the session-scoped
	// pressButton is the portable fallback. Try session route first, fall back.
	if err := c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routePressButton(c.session), PressHomePayload(), nil)
		return err
	}); err == nil {
		return nil
	}
	_, err := c.do(ctx, http.MethodPost, "/wda/homescreen", nil, nil)
	return err
}

func (c *HTTPClient) Recents(ctx context.Context) error {
	info := c.screenSnapshot()
	if info.Width <= 0 {
		return fmt.Errorf("wda recents: unknown screen size (session not created?)")
	}
	// App-switcher gesture approximation (points space).
	a := AppSwitcherPayload(info.Width, info.Height)
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeActions(c.session), a, nil)
		return err
	})
}

func (c *HTTPClient) LaunchApp(ctx context.Context, bundleID string) error {
	return c.sessionCall(ctx, false, func() error {
		_, err := c.do(ctx, http.MethodPost, routeLaunchApp(c.session), LaunchAppPayload(bundleID, true), nil)
		return err
	})
}

// findElement resolves an accessibility id to a WDA element reference.
func (c *HTTPClient) findElement(ctx context.Context, nodeID string) (string, error) {
	// do() unwraps {"value":...}; W3C-shaped builds return the element id
	// under the element-6066 key, legacy builds under "ELEMENT".
	var reply struct {
		ELEMENT    string `json:"ELEMENT"`
		W3CElement string `json:"element-6066-11e4-a52e-4f735466cecf"`
	}
	if _, err := c.do(ctx, http.MethodPost, routeElement(c.session), ElementQueryPayload(nodeID), &reply); err != nil {
		return "", err
	}
	if reply.W3CElement != "" {
		return reply.W3CElement, nil
	}
	if reply.ELEMENT != "" {
		return reply.ELEMENT, nil
	}
	return "", fmt.Errorf("wda element %q not found", nodeID)
}

// ── plumbing ─────────────────────────────────────────────────────────────

// sessionCall guards a session-scoped operation. On an invalid-session error
// it rebuilds the session once; readOnly ops are then retried (their result
// is not observable by the device), side-effecting ops return the error.
func (c *HTTPClient) sessionCall(ctx context.Context, readOnly bool, op func() error) error {
	err := op()
	if err == nil || !isInvalidSession(err) {
		return err
	}
	c.mu.Lock()
	c.session = ""
	c.mu.Unlock()
	if err := c.CreateSession(ctx); err != nil {
		return fmt.Errorf("wda session rebuild: %w", err)
	}
	if readOnly {
		return op()
	}
	return fmt.Errorf("wda session expired during side-effecting call: %w", err)
}

func isInvalidSession(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Session does not exist") ||
		strings.Contains(msg, "invalid session id")
}

// toPoints divides physical pixels by the device scale (min 1).
func (c *HTTPClient) toPoints(x, y int) (int, int) {
	s := c.Scale()
	if s <= 0 {
		s = 1
	}
	return x / s, y / s
}

// do issues one HTTP call against WDA, unwrapping the {"value": ...} envelope.
// data may be nil. Non-2xx replies surface as errors carrying the body tail.
func (c *HTTPClient) do(ctx context.Context, method, path string, payload, out any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("wda %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wda %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("wda %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		return raw, fmt.Errorf("wda %s %s: HTTP %d: %s", method, path, resp.StatusCode, tail(raw, 400))
	}
	if out == nil {
		return raw, nil
	}
	// WDA wraps replies in {"value": ...}; unwrap when present.
	var wrap struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Value) > 0 {
		raw = wrap.Value
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// Some endpoints return bare strings/arrays; only error when out expects JSON.
		return raw, nil
	}
	return raw, nil
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// keyCodeName maps §8.6 key names onto WDA /keys values (Appium WDA accepts
// XCUITest key names and single characters). DPad keys have no iOS equivalent.
func keyCodeName(key string) (string, bool) {
	switch key {
	case "enter", "return":
		return "\n", true
	case "tab":
		return "\t", true
	case "delete", "backspace":
		return "\b", true
	case "escape":
		return "\x1b", true
	case "space":
		return " ", true
	}
	return "", false
}

// ListApps is not served over WDA on this client: the app-list route differs
// across WDA forks, so devicelink.Link overrides it with go-ios
// installationproxy. The method exists only so HTTPClient satisfies the Client
// interface for direct (test) use; production callers go through Link.
func (c *HTTPClient) ListApps(ctx context.Context, includeSystem bool) ([]App, error) {
	return nil, fmt.Errorf("wda: ListApps not supported over HTTP; use devicelink (installationproxy)")
}

// Compile-time check that HTTPClient satisfies Client.
var _ Client = (*HTTPClient)(nil)
