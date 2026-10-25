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

	"device-control/ios/internal/screen"
)

// errorWDA answers every mutating route with HTTP 200 + a WDA error envelope
// ({"value":{"error":...}}). This is the shape the fork-dependent failure takes
// and the exact hole the original do() had: it only inspected the HTTP status,
// so a 2xx-with-error was reported to the caller as success and the command's
// ok=true lied about a device that did nothing.
type errorWDA struct {
	mu       sync.Mutex
	requests []recordedRequest
	session  string
}

func newErrorWDA(t *testing.T) (*errorWDA, *httptest.Server) {
	t.Helper()
	f := &errorWDA{session: "sess-1"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *errorWDA) handle(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{r.Method, r.URL.Path, body})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": f.session}})
		return
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/window/size"),
		r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/wda/screen"):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"width": 400, "height": 800, "pixelRatio": 2}})
		return
	default:
		// Every other call: 200 + an error body.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": map[string]any{"error": "invalid element state", "message": "cannot perform action"},
		})
	}
}

func newErrorClient(t *testing.T) *HTTPClient {
	t.Helper()
	_, srv := newErrorWDA(t)
	c := NewHTTPClient(srv.URL, 5*time.Second)
	if err := c.CreateSession(context.Background()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return c
}

// TestTapSurfacesErrorEnvelopeOnHTTP200 is the regression test for the silent
// no-op class: a mutating call whose reply is 2xx-but-error MUST throw.
func TestTapSurfacesErrorEnvelopeOnHTTP200(t *testing.T) {
	c := newErrorClient(t)
	if err := c.Tap(context.Background(), 100, 200, 0); err == nil {
		t.Fatal("Tap returned nil on a 2xx error envelope; the client is still trusting the status code")
	} else if !strings.Contains(err.Error(), "invalid element state") {
		t.Fatalf("error should carry the WDA error, got: %v", err)
	}
}

// TestMutatingCommandsAllCheckTheEnvelope covers the rest of the mutating
// surface in one pass — each of these passed out=nil and previously returned ok.
func TestMutatingCommandsAllCheckTheEnvelope(t *testing.T) {
	c := newErrorClient(t)
	calls := map[string]func() error{
		"Swipe":           func() error { return c.Swipe(context.Background(), 1, 1, 2, 2, 100) },
		"DoubleTap":       func() error { return c.DoubleTap(context.Background(), 1, 1) },
		"TypeText":        func() error { return c.TypeText(context.Background(), "hi") },
		"Key":             func() error { return c.Key(context.Background(), "enter") },
		"DismissKeyboard": func() error { return c.DismissKeyboard(context.Background()) },
		"Home":            func() error { return c.Home(context.Background()) },
		"LaunchApp":       func() error { return c.LaunchApp(context.Background(), "com.x") },
		"OpenURL":         func() error { return c.OpenURL(context.Background(), "https://example.com") },
	}
	for name, fn := range calls {
		if err := fn(); err == nil {
			t.Errorf("%s returned nil on a 2xx error envelope", name)
		}
	}
}

func TestForegroundAppReadsActiveAppInfo(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/wda/activeAppInfo"):
			got = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{
				"bundleId": "com.apple.mobilesafari", "name": "Safari",
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": "sess-1"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"width": 400, "height": 800, "pixelRatio": 2}})
		}
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.URL, 5*time.Second)
	if err := c.CreateSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	bundle, err := c.ForegroundApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bundle != "com.apple.mobilesafari" {
		t.Fatalf("bundle = %q", bundle)
	}
	if !strings.HasSuffix(got, "/wda/activeAppInfo") {
		t.Fatalf("wrong route: %s", got)
	}
}

func TestOpenURLPostsToSessionURLRoute(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.OpenURL(context.Background(), "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	var hit *recordedRequest
	for i := range f.requests {
		if strings.HasSuffix(f.requests[i].path, "/url") && f.requests[i].method == http.MethodPost {
			hit = &f.requests[i]
		}
	}
	if hit == nil {
		t.Fatal("no POST to /session/{id}/url")
	}
	if !strings.Contains(hit.body, "https://example.com/a") {
		t.Fatalf("url payload missing the address: %s", hit.body)
	}
}

// TestLaunchAppDoesNotStopRunningApp pins the parity fix: Android's open_app
// resumes an existing task, so iOS must not force-restart it.
func TestLaunchAppDoesNotStopRunningApp(t *testing.T) {
	f, srv := newFakeWDA(t)
	c := newTestClient(t, srv)
	if err := c.LaunchApp(context.Background(), "com.example.app"); err != nil {
		t.Fatal(err)
	}
	launches := f.find("/session/sess-1/wda/launchApp")
	if len(launches) != 1 {
		t.Fatalf("launchApp calls = %d, want 1", len(launches))
	}
	if !strings.Contains(launches[0].body, `"stopIfRunning":false`) {
		t.Fatalf("launch must not stop a running app (body: %s)", launches[0].body)
	}
}

// TestParseSourceAssignsStableNodeIDs is the regression test for the hole where
// no production path ever set screen.Node.ID — the tree's node_id column was
// empty and every node_id-targeted command returned stale_node.
func TestParseSourceAssignsStableNodeIDs(t *testing.T) {
	xml := []byte(`<App>` +
		`<Window>` +
		`<Button name="OK" enabled="true" visible="true" x="10" y="20" width="30" height="40"/>` +
		`<TextField identifier="search" enabled="true" visible="true" x="10" y="100" width="300" height="40"/>` +
		`</Window>` +
		`</App>`)
	res, err := ParseSource(xml, 3)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var walk func(n *screen.Node)
	walk = func(n *screen.Node) {
		if n == nil {
			return
		}
		ids = append(ids, n.ID)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, w := range res.Windows {
		walk(w.Tree)
	}
	if len(ids) != 4 {
		t.Fatalf("nodes = %d, want 4", len(ids))
	}
	for i, id := range ids {
		if id == "" {
			t.Fatalf("node %d has an empty node_id", i)
		}
		if !strings.HasPrefix(id, "node_") {
			t.Fatalf("node %d id %q is not node_-prefixed", i, id)
		}
	}
	// Distinct: three sibling-ish nodes must not collide.
	seen := map[string]bool{}
	for _, id := range ids[1:] { // skip the root
		if seen[id] {
			t.Fatalf("duplicate node_id %q", id)
		}
		seen[id] = true
	}

	// Stable: re-parsing identical XML yields identical ids (spec §8.4 lets a
	// caller reuse ids while the UI is unchanged).
	res2, err := ParseSource(xml, 3)
	if err != nil {
		t.Fatal(err)
	}
	var ids2 []string
	var walk2 func(n *screen.Node)
	walk2 = func(n *screen.Node) {
		if n == nil {
			return
		}
		ids2 = append(ids2, n.ID)
		for _, c := range n.Children {
			walk2(c)
		}
	}
	for _, w := range res2.Windows {
		walk2(w.Tree)
	}
	for i := range ids {
		if ids[i] != ids2[i] {
			t.Fatalf("node_id unstable across re-parse: %q vs %q", ids[i], ids2[i])
		}
	}
}

// TestScreenInfoIsPhysicalPixels pins the units fix: window/size comes back in
// points, and the wire header must be pixels so it matches the pixel bounds the
// tree carries (a points header made derived coordinates land in the wrong spot).
func TestScreenInfoIsPhysicalPixels(t *testing.T) {
	c := newTestClient(t, func() *httptest.Server {
		_, srv := newFakeWDA(t)
		return srv
	}())
	info, scale, err := c.ScreenInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scale != 3 {
		t.Fatalf("scale = %d, want 3", scale)
	}
	if info.Width != 393*3 || info.Height != 852*3 {
		t.Fatalf("screen = %dx%d, want %dx%d (points × scale)",
			info.Width, info.Height, 393*3, 852*3)
	}
}
