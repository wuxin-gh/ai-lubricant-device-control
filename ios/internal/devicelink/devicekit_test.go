package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// fakeDeviceKit is an httptest server speaking enough JSON-RPC to exercise the
// client: /health, plus a method->result table. Every request is recorded.
type fakeDeviceKit struct {
	server *httptest.Server
	t      *testing.T

	results map[string]any
	errors  map[string]*dkRPCError
	reqs    []dkRequest
}

type dkRequest struct {
	Method string
	Params map[string]any
}

func newFakeDeviceKit(t *testing.T) *fakeDeviceKit {
	f := &fakeDeviceKit{
		t:       t,
		results: map[string]any{},
		errors:  map[string]*dkRPCError{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDeviceKit) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == DeviceKitHealthPath {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	if r.URL.Path != DeviceKitRPCPath {
		http.NotFound(w, r)
		return
	}
	var env struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		f.t.Errorf("bad rpc request body: %v", err)
		http.Error(w, "bad body", 400)
		return
	}
	f.reqs = append(f.reqs, dkRequest{Method: env.Method, Params: env.Params})
	if errObj, ok := f.errors[env.Method]; ok {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"error": errObj,
		})
		return
	}
	result, ok := f.results[env.Method]
	if !ok {
		result = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result":  result,
	})
}

func (f *fakeDeviceKit) client() *DeviceKitClient {
	return NewDeviceKitClient(f.server.URL, 5*time.Second)
}

func (f *fakeDeviceKit) lastReq(method string) *dkRequest {
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Method == method {
			return &f.reqs[i]
		}
	}
	return nil
}

func TestDeviceKitStatus(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	if err := c.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

func TestDeviceKitTapScalesAndRoutes(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	// Probe screen info so Tap knows the scale (width 300 @3x => 900 px).
	f.results[dkInfo] = map[string]any{"width": 300, "height": 600, "scale": 3}
	if _, _, err := c.ScreenInfo(context.Background()); err != nil {
		t.Fatalf("ScreenInfo: %v", err)
	}

	if err := c.Tap(context.Background(), 450, 900, 0); err != nil { // 3x pixels
		t.Fatalf("Tap: %v", err)
	}
	req := f.lastReq(dkTap)
	if req == nil {
		t.Fatal("no device.io.tap request recorded")
	}
	if req.Params["x"] != float64(150) || req.Params["y"] != float64(300) {
		t.Fatalf("tap not scaled to points: %v", req.Params)
	}

	if err := c.Tap(context.Background(), 100, 200, 500); err != nil {
		t.Fatalf("long press: %v", err)
	}
	req = f.lastReq(dkLongpress)
	if req == nil {
		t.Fatal("no device.io.longpress request recorded")
	}
	if req.Params["duration"] != float64(0.5) {
		t.Fatalf("duration not converted to seconds: %v", req.Params["duration"])
	}
}

func TestDeviceKitSwipeAndText(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkInfo] = map[string]any{"width": 300, "height": 600, "scale": 2}

	if err := c.Swipe(context.Background(), 0, 0, 200, 400, 300); err != nil {
		t.Fatalf("Swipe: %v", err)
	}
	req := f.lastReq(dkSwipe)
	if req == nil || req.Params["fromX"] != float64(0) || req.Params["toY"] != float64(200) {
		t.Fatalf("swipe params: %+v", req)
	}
	if req.Params["duration"] != float64(0.3) {
		t.Fatalf("swipe duration: %v", req.Params["duration"])
	}

	if err := c.TypeText(context.Background(), "hello"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	req = f.lastReq(dkText)
	if req.Params["text"] != "hello" {
		t.Fatalf("text params: %v", req.Params)
	}
}

func TestDeviceKitScrollSynthesizesSwipe(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkInfo] = map[string]any{"width": 300, "height": 600, "scale": 2}
	if _, _, err := c.ScreenInfo(context.Background()); err != nil {
		t.Fatalf("ScreenInfo: %v", err)
	}

	if err := c.Scroll(context.Background(), "down"); err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	req := f.lastReq(dkSwipe)
	if req == nil {
		t.Fatal("scroll did not synthesize a swipe")
	}
	// "down" reveals content below: the finger moves UP (toY < fromY).
	if req.Params["fromY"].(float64) <= req.Params["toY"].(float64) {
		t.Fatalf("scroll down should move finger up: %v", req.Params)
	}
}

func TestDeviceKitHomeAndLaunch(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()

	if err := c.Home(context.Background()); err != nil {
		t.Fatalf("Home: %v", err)
	}
	if req := f.lastReq(dkButton); req == nil || req.Params["button"] != "home" {
		t.Fatalf("home button params: %+v", req)
	}

	if err := c.LaunchApp(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if req := f.lastReq(dkAppsLaunch); req == nil || req.Params["bundleId"] != "com.example.app" {
		t.Fatalf("launch app params: %+v", req)
	}
}

func TestDeviceKitScreenshot(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkScreenshot] = "aGVsbG8=" // base64("hello")

	png, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if string(png) != "hello" {
		t.Fatalf("screenshot bytes: %q", png)
	}
}

func TestDeviceKitSourceXMLFallsBackToWdaParser(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkDumpUI] = `<?xml version="1.0"?><App><Window><Button label="OK" x="0" y="0" width="10" height="5"/></Window></App>`

	res, err := c.Source(context.Background())
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if res.Degraded {
		t.Fatal("xml source should parse, not degrade")
	}
	if got := res.Windows[0].Tree.Children[0].Children[0].ContentDescription; got != "OK" {
		t.Fatalf("xml tree label: %q", got)
	}
}

func TestDeviceKitSourceJSONAdapts(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkDumpUI] = map[string]any{
		"root": map[string]any{
			"type":  "Window",
			"frame": map[string]any{"x": 0, "y": 0, "width": 300, "height": 600},
			"children": []any{
				map[string]any{
					"type":       "Button",
					"label":      "登录",
					"identifier": "login-btn",
					"frame":      map[string]any{"x": 10, "y": 20, "width": 50, "height": 30},
					"enabled":    true,
					"visible":    true,
					"clickable":  true,
				},
			},
		},
	}

	res, err := c.Source(context.Background())
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if res.Degraded {
		t.Fatal("json source should parse, not degrade")
	}
	tree := res.Windows[0].Tree
	if tree.ClassName != "Window" {
		t.Fatalf("root class: %q", tree.ClassName)
	}
	if len(tree.Children) != 1 {
		t.Fatalf("children: %d", len(tree.Children))
	}
	btn := tree.Children[0]
	if btn.ClassName != "Button" || btn.Text != "登录" || btn.ResourceID != "login-btn" {
		t.Fatalf("button fields: %+v", btn)
	}
	if !btn.Enabled || !btn.Visible || !btn.Clickable {
		t.Fatalf("button flags: %+v", btn)
	}
	// Points scaled to pixels at default scale 2.
	if btn.Bounds != (screen.Bounds{Left: 20, Top: 40, Right: 120, Bottom: 100}) {
		t.Fatalf("button bounds: %+v", btn.Bounds)
	}
}

func TestDeviceKitSourceUnknownFormatIsError(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkDumpUI] = 42 // number, neither xml nor json-object

	if _, err := c.Source(context.Background()); err == nil {
		t.Fatal("expected structured error for unknown dump format")
	}
}

func TestDeviceKitRPCErrorSurfaces(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.errors[dkTap] = &dkRPCError{Code: -32000, Message: "device locked"}

	err := c.Tap(context.Background(), 1, 2, 0)
	if err == nil || !strings.Contains(err.Error(), "device locked") {
		t.Fatalf("rpc error should surface message, got: %v", err)
	}
}

func TestDeviceKitUnsupportedRefuses(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()

	if err := c.SetValue(context.Background(), "node-1", "text"); err == nil {
		t.Fatal("SetValue must be unsupported on DeviceKit")
	} else if !errors.Is(err, wda.ErrUnsupported) {
		t.Fatalf("SetValue error must wrap ErrUnsupported: %v", err)
	}
	if err := c.DismissKeyboard(context.Background()); err == nil || !errors.Is(err, wda.ErrUnsupported) {
		t.Fatalf("DismissKeyboard must be unsupported: %v", err)
	}
	if err := c.Key(context.Background(), "dpad_down"); err == nil || !errors.Is(err, wda.ErrUnsupported) {
		t.Fatalf("unconfirmed key must be unsupported: %v", err)
	}
	// enter is a confirmed textual key: goes through device.io.text.
	if err := c.Key(context.Background(), "enter"); err != nil {
		t.Fatalf("enter key: %v", err)
	}
	if req := f.lastReq(dkText); req == nil || req.Params["text"] != "\n" {
		t.Fatalf("enter should type \\n: %+v", req)
	}
}

func TestDeviceKitScreenInfoDefaultsAndOrientation(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()
	f.results[dkInfo] = map[string]any{"width": 390, "height": 844, "scale": 3.0}

	info, scale, err := c.ScreenInfo(context.Background())
	if err != nil {
		t.Fatalf("ScreenInfo: %v", err)
	}
	if scale != 3 || info.Width != 1170 || info.Height != 2532 {
		t.Fatalf("info: %+v scale=%d (want points 390x844 × scale 3 = 1170x2532)", info, scale)
	}
	if info.DensityDpi != 480 {
		t.Fatalf("3x density: %d", info.DensityDpi)
	}
}

func TestDeviceKitSessionlessLifecycle(t *testing.T) {
	f := newFakeDeviceKit(t)
	c := f.client()

	if err := c.CreateSession(context.Background()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if c.Session() != "" {
		t.Fatal("DeviceKit is sessionless; Session must be empty")
	}
	c.Close(context.Background())
}
