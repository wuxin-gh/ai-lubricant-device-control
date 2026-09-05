package wda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWDA is an httptest server that speaks just enough of the WDA HTTP
// surface to pin the client's route/payload contract. It records requests so
// each test can assert the exact path and body the client produced — if a real
// device later proves a route wrong, the fix lands in routes.go and one of
// these assertions flips, making the divergence loud.
type fakeWDA struct {
	t       *testing.T
	session string

	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	body   string
}

func newFakeWDA(t *testing.T) (*fakeWDA, *httptest.Server) {
	f := &fakeWDA{t: t, session: "sess-1"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeWDA) handle(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{r.Method, r.URL.Path, body})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/status":
		writeValue(w, map[string]any{"build": map[string]any{"version": "test"}})
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		writeValue(w, map[string]any{
			"sessionId": f.session,
			"capabilities": map[string]any{
				"device":     "iphone",
				"pixelRatio": 3,
			},
		})
	case r.Method == http.MethodGet && r.URL.Path == "/session/"+f.session+"/window/size":
		writeValue(w, map[string]any{"width": 393, "height": 852})
	case r.Method == http.MethodGet && r.URL.Path == "/session/"+f.session+"/wda/screen":
		writeValue(w, map[string]any{"pixelRatio": 3, "statusBarSize": map[string]any{"width": 393, "height": 54}})
	case r.Method == http.MethodGet && r.URL.Path == "/session/"+f.session+"/source":
		writeValue(w, `<App><Window><Button name="OK" label="OK" enabled="true" visible="true" x="10" y="20" width="30" height="40"/></Window></App>`)
	case r.Method == http.MethodGet && r.URL.Path == "/session/"+f.session+"/screenshot":
		writeValue(w, "aGVsbG8=") // "hello"
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions"):
		writeValue(w, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys"):
		writeValue(w, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wda/keyboard/dismiss"):
		writeValue(w, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wda/pressButton"):
		writeValue(w, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wda/launchApp"):
		writeValue(w, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/element"):
		writeValue(w, map[string]any{"element-6066-11e4-a52e-4f735466cecf": "el-1"})
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/element/") && strings.HasSuffix(r.URL.Path, "/value"):
		writeValue(w, nil)
	case r.Method == http.MethodDelete && r.URL.Path == "/session/"+f.session:
		writeValue(w, nil)
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeValue(w http.ResponseWriter, v any) {
	if v == nil {
		v = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"value": v})
}

func readBody(r *http.Request) string {
	var b strings.Builder
	if r.Body != nil {
		buf := make([]byte, 1<<20)
		n, _ := r.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		_ = r.Body.Close()
	}
	return b.String()
}

func (f *fakeWDA) last() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return recordedRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeWDA) find(path string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, r := range f.requests {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func newTestClient(t *testing.T, srv *httptest.Server) *HTTPClient {
	t.Helper()
	c := NewHTTPClient(srv.URL, 5*time.Second)
	if err := c.CreateSession(context.Background()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return c
}

func TestCreateSessionReadsScaleAndScreenInfo(t *testing.T) {
	_, srv := newFakeWDA(t)
	c := newTestClient(t, srv)

	if got := c.Session(); got != "sess-1" {
		t.Fatalf("session = %q, want sess-1", got)
	}
	if got := c.Scale(); got != 3 {
		t.Fatalf("scale = %d, want 3 (pixelRatio from wda/screen)", got)
	}
	info, scale, err := c.ScreenInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 393 || info.Height != 852 {
		t.Fatalf("screen = %dx%d, want 393x852", info.Width, info.Height)
	}
	if scale != 3 {
		t.Fatalf("reported scale = %d, want 3", scale)
	}
	if info.DensityDpi != 480 {
		t.Fatalf("density = %d, want 480 for 3x", info.DensityDpi)
	}
}

func TestCreateSessionIdempotent(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.CreateSession(context.Background()); err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if n := len(f.find("/session")); n != 1 {
		t.Fatalf("POST /session count = %d, want 1 (idempotent)", n)
	}
}

func TestTapUsesW3CActions(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)

	// 900, 1200 pixels at scale 3 → 300, 400 points.
	if err := c.Tap(context.Background(), 900, 1200, 0); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.path != "/session/sess-1/actions" {
		t.Fatalf("tap path = %q, want /session/sess-1/actions", r.path)
	}
	var payload struct {
		Actions []struct {
			Type    string `json:"type"`
			Actions []struct {
				Type string  `json:"type"`
				X    float64 `json:"x"`
				Y    float64 `json:"y"`
			} `json:"actions"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(r.body), &payload); err != nil {
		t.Fatalf("tap payload not JSON: %v\nbody: %s", err, r.body)
	}
	if len(payload.Actions) != 1 {
		t.Fatalf("pointer sequences = %d, want 1", len(payload.Actions))
	}
	items := payload.Actions[0].Actions
	if len(items) != 4 {
		t.Fatalf("tap action items = %d, want 4 (move,down,pause,up): %s", len(items), r.body)
	}
	if items[0].Type != "pointerMove" || items[0].X != 300 || items[0].Y != 400 {
		t.Fatalf("tap coords = (%v,%v) %s, want (300,400) pointerMove", items[0].X, items[0].Y, items[0].Type)
	}
}

func TestLongPressCarriesDuration(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.Tap(context.Background(), 300, 300, 1500); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.last().body, `"duration":1500`) {
		t.Fatalf("long press payload missing duration 1500: %s", f.last().body)
	}
}

func TestSwipePayload(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.Swipe(context.Background(), 300, 900, 300, 300, 600); err != nil {
		t.Fatal(err)
	}
	body := f.last().body
	if !strings.Contains(body, `"x":100`) || !strings.Contains(body, `"y":300`) {
		t.Fatalf("swipe start wrong (scale 3 → points): %s", body)
	}
	if !strings.Contains(body, `"y":100`) {
		t.Fatalf("swipe end wrong: %s", body)
	}
}

func TestScrollPostsActionsInPoints(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.Scroll(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.path != "/session/sess-1/actions" {
		t.Fatalf("scroll path = %q", r.path)
	}
	// centre = (196, 426); up → +213 → (196, 639), in points (no double scaling).
	if !strings.Contains(r.body, `"x":196`) || !strings.Contains(r.body, `"y":639`) {
		t.Fatalf("scroll end wrong (must be points, not double-scaled): %s", r.body)
	}
}

func TestSourceSessionScopedAndScaled(t *testing.T) {
	_, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	res, err := c.Source(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Windows) != 1 {
		t.Fatalf("windows = %d", len(res.Windows))
	}
	// Button at (10,20) 30x40 points, scale 3 → pixels (30,60)-(120,180).
	btn := res.Windows[0].Tree.Children[0].Children[0]
	want := [4]int{30, 60, 120, 180}
	got := [4]int{btn.Bounds.Left, btn.Bounds.Top, btn.Bounds.Right, btn.Bounds.Bottom}
	if got != want {
		t.Fatalf("button bounds = %v, want %v", got, want)
	}
}

func TestScreenshotDecodesBase64(t *testing.T) {
	_, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	png, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(png) != "hello" {
		t.Fatalf("screenshot = %q, want hello", png)
	}
}

func TestSetValueQueriesElementThenPostsValue(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.SetValue(context.Background(), "save_btn", "hello"); err != nil {
		t.Fatal(err)
	}
	queries := f.find("/session/sess-1/element")
	if len(queries) != 1 {
		t.Fatalf("element queries = %d, want 1", len(queries))
	}
	if !strings.Contains(queries[0].body, `"accessibility id"`) {
		t.Fatalf("element query should use accessibility id: %s", queries[0].body)
	}
	vals := f.find("/session/sess-1/element/el-1/value")
	if len(vals) != 1 || !strings.Contains(vals[0].body, "hello") {
		t.Fatalf("value posts = %+v", vals)
	}
}

func TestHomeFallsBackToHomescreen(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.Home(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.find("/session/sess-1/wda/pressButton")); n != 1 {
		t.Fatalf("pressButton calls = %d, want 1", n)
	}
}

func TestCloseDeletesSession(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	c.Close(context.Background())
	if n := len(f.find("/session/sess-1")); n != 1 {
		t.Fatalf("DELETE /session calls = %d, want 1", n)
	}
	if c.Session() != "" {
		t.Fatal("session not cleared after Close")
	}
}

func TestLaunchAppPostsBundleID(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.LaunchApp(context.Background(), "com.example.app"); err != nil {
		t.Fatal(err)
	}
	launches := f.find("/session/sess-1/wda/launchApp")
	if len(launches) != 1 || !strings.Contains(launches[0].body, "com.example.app") {
		t.Fatalf("launchApp calls = %+v", launches)
	}
}

func TestListAppsIsUnsupportedOverHTTP(t *testing.T) {
	_, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if _, err := c.ListApps(context.Background(), false); err == nil {
		t.Fatal("ListApps must be unsupported over HTTP; devicelink serves it")
	}
}
