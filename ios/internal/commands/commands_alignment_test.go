package commands

import (
	"context"
	"strings"
	"testing"

	"device-control/ios/internal/errcode"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// ── get_foreground_app ───────────────────────────────────────────────────

func TestGetForegroundAppReturnsPackage(t *testing.T) {
	f := newFake()
	bundle := "com.apple.mobilesafari"
	f.Foreground = &bundle
	out, err := dispatch("get_foreground_app", `{}`, f)
	if err != nil {
		t.Fatal(err)
	}
	if out["package"] != "com.apple.mobilesafari" {
		t.Fatalf("package = %v", out["package"])
	}
}

func TestGetForegroundAppUnsupportedIsHonest(t *testing.T) {
	f := newFake() // Foreground == nil → the backend cannot observe it
	_, err := dispatch("get_foreground_app", `{}`, f)
	if err == nil || errcode.From(err) != errcode.Unsupported {
		t.Fatalf("want unsupported when the backend cannot observe the foreground, got %v", err)
	}
}

// ── open_url ─────────────────────────────────────────────────────────────

func TestOpenURLPassesToBackend(t *testing.T) {
	f := newFake()
	if _, err := dispatch("open_url", `{"url":"https://example.com/a"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.OpenedURLs) != 1 || f.OpenedURLs[0] != "https://example.com/a" {
		t.Fatalf("opened urls = %v", f.OpenedURLs)
	}
}

func TestOpenURLRejectsNonHTTPSchemes(t *testing.T) {
	// Non-http(s) schemes are default-denied (same policy as mobile-mcp): a
	// custom scheme handed over by an LLM-driven agent can trigger privileged
	// app actions, so the gate must sit ahead of the backend call.
	f := newFake()
	for _, u := range []string{
		`{"url":"intent://evil/#Intent;end"}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"myapp://action"}`,
	} {
		if _, err := dispatch("open_url", u, f); err == nil || errcode.From(err) != errcode.BadArgs {
			t.Fatalf("want bad_args for %s, got %v", u, err)
		}
	}
	if len(f.OpenedURLs) != 0 {
		t.Fatal("a rejected scheme must not reach the backend")
	}
}

// ── open_app: verify foreground, classify errors ─────────────────────────

func TestOpenAppVerifiesForeground(t *testing.T) {
	f := newFake()
	other := "com.apple.otherapp"
	f.Foreground = &other
	_, err := dispatch("open_app", `{"package":"com.apple.mobilesafari"}`, f)
	if err == nil {
		t.Fatal("a launch that left another app foregrounded must not report success")
	}
	if errcode.From(err) != errcode.DeviceError {
		t.Fatalf("want device_error, got %v", err)
	}
}

func TestOpenAppSucceedsWhenForegroundMatches(t *testing.T) {
	f := newFake()
	safari := "com.apple.mobilesafari"
	f.Foreground = &safari
	if _, err := dispatch("open_app", `{"package":"com.apple.mobilesafari"}`, f); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAppKeepsWorkingWhenForegroundUnobservable(t *testing.T) {
	// Backends that cannot observe the foreground keep the old best-effort ok
	// rather than inventing a failure.
	f := newFake()
	if _, err := dispatch("open_app", `{"package":"com.x"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.LaunchedApps) != 1 {
		t.Fatalf("launch not forwarded: %v", f.LaunchedApps)
	}
}

func TestOpenAppClassifiesUnsupported(t *testing.T) {
	f := newFake()
	f.Err = wda.ErrUnsupported
	_, err := dispatch("open_app", `{"package":"com.x"}`, f)
	if err == nil || errcode.From(err) != errcode.Unsupported {
		t.Fatalf("want unsupported, got %v", err)
	}
}

// ── scroll with node_id scrolls the target, not the screen centre ────────

func TestScrollWithNodeIDSwipesAtNodeCenter(t *testing.T) {
	f := newFake()
	f.SourceXML = []byte(sampleSource)
	res, err := wda.ParseSource(f.SourceXML, 1)
	if err != nil {
		t.Fatal(err)
	}
	var target string
	var walk func(n *screen.Node)
	walk = func(n *screen.Node) {
		if n == nil || target != "" {
			return
		}
		if n.ClassName == "Button" {
			target = n.ID
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, w := range res.Windows {
		walk(w.Tree)
	}
	if target == "" {
		t.Fatal("sample source has no Button; the fixture drifted")
	}

	if _, err := dispatch("scroll", `{"direction":"down","node_id":"`+target+`"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Swipes) != 1 {
		t.Fatalf("expected a swipe through the node, got %+v", f.Swipes)
	}
	// The swipe must start at the node's bounds center, not the screen centre
	// (newFake is 375x812 → 187,406).
	if s := f.Swipes[0]; s.X1 == 187 && s.Y1 == 406 {
		t.Fatalf("scroll ignored node_id and swiped the screen centre: %+v", s)
	}
}

func TestScrollWithoutNodeIDUsesCenterSwipe(t *testing.T) {
	f := newFake()
	if _, err := dispatch("scroll", `{"direction":"down"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Scrolls) != 1 {
		t.Fatalf("expected the backend scroll call, got %v", f.Scrolls)
	}
}

// ── dismiss_keyboard honors the per-call context ─────────────────────────

func TestDismissKeyboardUsesCallContext(t *testing.T) {
	// With context.Background() the call was uncancellable and could outlive the
	// dispatcher's budget; the handler must pass ctx through.
	f := newFake()
	f.Err = context.Canceled
	_, err := dispatch("dismiss_keyboard", `{}`, f)
	if err == nil {
		t.Fatal("dismiss_keyboard ignored a cancelled context")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("want the cancellation to surface, got %v", err)
	}
}
