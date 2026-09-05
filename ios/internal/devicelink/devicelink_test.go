package devicelink

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"net/http/httptest"

	"device-control/ios/internal/wda"
)

// fakeSession is a test deviceSession: it stands in for the go-ios runner and
// forward, pointing the link's HTTP client at a real httptest WDA server.
type fakeSession struct {
	hostPort int
	apps     []wda.App
	log      string
	closed   int32
}

func (f *fakeSession) HostPort() int { return f.hostPort }
func (f *fakeSession) RunnerLog() string {
	if f.log != "" {
		return f.log
	}
	return ""
}
func (f *fakeSession) ListApps(_ context.Context, includeSystem bool) ([]wda.App, error) {
	if includeSystem {
		return append(f.apps, wda.App{BundleID: "com.apple.Preferences", Name: "Settings"}), nil
	}
	return f.apps, nil
}
func (f *fakeSession) Close() error {
	atomic.AddInt32(&f.closed, 1)
	return nil
}

// withFakeStart replaces the go-ios start seam and returns the session the
// link will adopt.
func withFakeStart(t *testing.T, s *fakeSession) func() {
	t.Helper()
	orig := startSession
	startSession = func(ctx context.Context, opts Options) (deviceSession, error) {
		return s, nil
	}
	return func() { startSession = orig }
}

// wdaTestServer spins an httptest server that answers the routes Link.Start
// probes (/status, /session, window size, wda/screen) plus /source.
func wdaTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var session = "s1"
	srv := newSessionStubWDA(t, session)
	return srv
}

func TestLinkStartStartsAndClosesOnce(t *testing.T) {
	srv := wdaTestServer(t)
	sess := &fakeSession{hostPort: portOf(t, srv.URL)}
	defer withFakeStart(t, sess)()

	l := New(Options{WDABundleID: "com.x.WebDriverAgentRunner.xctrunner", XCTestConfig: "WebDriverAgentRunner.xctest", ReadyTimeout: 5 * time.Second})
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if l.client == nil {
		t.Fatal("client not set after Start")
	}
	if l.client.Session() == "" {
		t.Fatal("no WDA session after Start")
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if n := atomic.LoadInt32(&sess.closed); n != 1 {
		t.Fatalf("session Close called %d times, want exactly 1", n)
	}
}

func TestLinkStartRequiresWDAConfig(t *testing.T) {
	l := New(Options{})
	if err := l.Start(context.Background()); err == nil {
		t.Fatal("Start should fail without WDABundleID/XCTestConfig")
	}
}

func TestLinkStartFailureTearsDownSession(t *testing.T) {
	// Session whose host port points at a dead server: readiness never passes.
	sess := &fakeSession{hostPort: 1, log: "WDA: no such runner"}
	defer withFakeStart(t, sess)()

	l := New(Options{
		WDABundleID:  "com.x.WebDriverAgentRunner.xctrunner",
		XCTestConfig: "WebDriverAgentRunner.xctest",
		ReadyTimeout: 1200 * time.Millisecond,
	})
	err := l.Start(context.Background())
	if err == nil {
		t.Fatal("Start should fail when WDA never becomes ready")
	}
	if n := atomic.LoadInt32(&sess.closed); n != 1 {
		t.Fatalf("failed Start must tear the session down, closed=%d", n)
	}
	if l.session != nil || l.client != nil {
		t.Fatal("failed Start must leave no session/client behind")
	}
}

func TestLinkListAppsViaSession(t *testing.T) {
	srv := wdaTestServer(t)
	sess := &fakeSession{
		hostPort: portOf(t, srv.URL),
		apps:     []wda.App{{BundleID: "com.example.app", Name: "Example"}},
	}
	defer withFakeStart(t, sess)()

	l := New(Options{WDABundleID: "com.x.WebDriverAgentRunner.xctrunner", XCTestConfig: "WebDriverAgentRunner.xctest"})
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Close()

	apps, err := l.ListApps(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].BundleID != "com.example.app" {
		t.Fatalf("user apps = %+v", apps)
	}
	all, err := l.ListApps(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("include-system apps = %d, want 2", len(all))
	}
}

func TestLinkDelegatesCommandsToWDA(t *testing.T) {
	srv := wdaTestServer(t)
	sess := &fakeSession{hostPort: portOf(t, srv.URL)}
	defer withFakeStart(t, sess)()

	l := New(Options{WDABundleID: "com.x.WebDriverAgentRunner.xctrunner", XCTestConfig: "WebDriverAgentRunner.xctest"})
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Close()

	// ScreenInfo comes from the session (scale 2 stub).
	info, scale, err := l.ScreenInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scale != 2 || info.Width != 390 {
		t.Fatalf("screen = %dx%d @%dx, want 390 wide @2x", info.Width, info.Height, scale)
	}

	// A real command round-trips through the session HTTP client.
	if err := l.Tap(context.Background(), 100, 200, 0); err != nil {
		t.Fatalf("Tap: %v", err)
	}
	if _, err := l.Source(context.Background()); err != nil {
		t.Fatalf("Source: %v", err)
	}
}

func TestLinkCommandsFailBeforeStart(t *testing.T) {
	l := New(Options{})
	if err := l.Tap(context.Background(), 1, 1, 0); err == nil {
		t.Fatal("Tap before Start must fail")
	}
	if _, err := l.ListApps(context.Background(), false); err == nil {
		t.Fatal("ListApps before Start must fail")
	}
}

func TestFreePortReturnsUsablePort(t *testing.T) {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("freePort = %d, out of range", p)
	}
}

// ── test-only WDA stub (separate from the production handler) ────────────

func newSessionStubWDA(t *testing.T, session string) *httptest.Server {
	t.Helper()
	mux := newStubMux(t, session)
	return httptest.NewServer(mux)
}

func portOf(t *testing.T, url string) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(url, "http://127.0.0.1:%d", &port); err != nil {
		t.Fatalf("parse test server port from %q: %v", url, err)
	}
	return port
}

func newStubMux(t *testing.T, session string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{"build":{"version":"stub"}}}`)
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"value":{"sessionId":%q,"capabilities":{}}}`, session)
	})
	prefix := "/session/" + session
	mux.HandleFunc(prefix+"/window/size", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{"width":390,"height":844}}`)
	})
	mux.HandleFunc(prefix+"/wda/screen", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{"pixelRatio":2}}`)
	})
	mux.HandleFunc(prefix+"/source", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":"<App><Window><Button name='B' label='B' enabled='true' visible='true' x='1' y='2' width='3' height='4'/></Window></App>"}`)
	})
	mux.HandleFunc(prefix+"/actions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{}}`)
	})
	mux.HandleFunc(prefix+"/wda/pressButton", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{}}`)
	})
	mux.HandleFunc(prefix+"/wda/homescreen", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":{}}`)
	})
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		// DELETE /session/{id}
		if r.Method == http.MethodDelete {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"value":{}}`)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

var _ = errors.New // keep errors import used if unused later
