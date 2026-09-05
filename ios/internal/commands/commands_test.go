package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"device-control/ios/internal/errcode"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

func newFake() *wda.Fake {
	return &wda.Fake{
		Scale:  1,
		Screen: screen.ScreenInfo{Width: 375, Height: 812, DensityDpi: 320, Orientation: "portrait"},
	}
}

func dispatch(cmd string, args string, f *wda.Fake) (map[string]any, error) {
	handlers := Build(f, screen.New())
	h, ok := handlers[cmd]
	if !ok {
		return nil, errors.New("unknown command in test: " + cmd)
	}
	data, err := h(context.Background(), json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m, nil
}

func TestTapByCoords(t *testing.T) {
	f := newFake()
	if _, err := dispatch("tap", `{"x":100,"y":200}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Taps) != 1 || f.Taps[0].X != 100 || f.Taps[0].Y != 200 || f.Taps[0].DurationMS != 0 {
		t.Fatalf("tap recorded wrong: %+v", f.Taps)
	}
}

func TestTapMissingArgs(t *testing.T) {
	f := newFake()
	_, err := dispatch("tap", `{}`, f)
	if err == nil || errcode.From(err) != errcode.BadArgs {
		t.Fatalf("want bad_args, got %v", err)
	}
}

func TestLongPressDefaultDuration(t *testing.T) {
	f := newFake()
	if _, err := dispatch("long_press", `{"x":10,"y":20}`, f); err != nil {
		t.Fatal(err)
	}
	if f.Taps[0].DurationMS != 500 {
		t.Errorf("default duration = %d, want 500", f.Taps[0].DurationMS)
	}
}

func TestLongPressCustomDuration(t *testing.T) {
	f := newFake()
	if _, err := dispatch("long_press", `{"x":1,"y":2,"duration_ms":900}`, f); err != nil {
		t.Fatal(err)
	}
	if f.Taps[0].DurationMS != 900 {
		t.Errorf("duration = %d, want 900", f.Taps[0].DurationMS)
	}
}

func TestSwipe(t *testing.T) {
	f := newFake()
	if _, err := dispatch("swipe", `{"x1":0,"y1":0,"x2":100,"y2":200,"duration_ms":250}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Swipes) != 1 || f.Swipes[0].DurationMS != 250 {
		t.Fatalf("swipe wrong: %+v", f.Swipes)
	}
}

func TestScroll(t *testing.T) {
	f := newFake()
	if _, err := dispatch("scroll", `{"direction":"down"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Scrolls) != 1 || f.Scrolls[0] != "down" {
		t.Fatalf("scroll wrong: %+v", f.Scrolls)
	}
}

func TestScrollMissingDirection(t *testing.T) {
	f := newFake()
	_, err := dispatch("scroll", `{}`, f)
	if errcode.From(err) != errcode.BadArgs {
		t.Fatalf("want bad_args, got %v", err)
	}
}

func TestTypeText(t *testing.T) {
	f := newFake()
	if _, err := dispatch("type_text", `{"text":"hello"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.TypedText) != 1 || f.TypedText[0] != "hello" {
		t.Fatalf("typed wrong: %+v", f.TypedText)
	}
}

func TestSetText(t *testing.T) {
	f := newFake()
	if _, err := dispatch("set_text", `{"node_id":"n1","text":"hi"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.SetValues) != 1 || f.SetValues[0].NodeID != "n1" || f.SetValues[0].Text != "hi" {
		t.Fatalf("set_text wrong: %+v", f.SetValues)
	}
}

func TestPressHome(t *testing.T) {
	f := newFake()
	if _, err := dispatch("press_home", `{}`, f); err != nil {
		t.Fatal(err)
	}
	if f.HomeCount != 1 {
		t.Fatalf("home count = %d", f.HomeCount)
	}
}

func TestPressBackIsDeviceError(t *testing.T) {
	f := newFake()
	_, err := dispatch("press_back", `{}`, f)
	if errcode.From(err) != errcode.DeviceError {
		t.Fatalf("want device_error for iOS back, got %v", err)
	}
}

func TestPressKeyHomeRoute(t *testing.T) {
	f := newFake()
	if _, err := dispatch("press_key", `{"key":"home"}`, f); err != nil {
		t.Fatal(err)
	}
	if f.HomeCount != 1 {
		t.Fatalf("home count = %d", f.HomeCount)
	}
}

func TestPressKeyBackRoute(t *testing.T) {
	f := newFake()
	_, err := dispatch("press_key", `{"key":"back"}`, f)
	if errcode.From(err) != errcode.DeviceError {
		t.Fatalf("want device_error, got %v", err)
	}
}

func TestPressKeyEnter(t *testing.T) {
	f := newFake()
	if _, err := dispatch("press_key", `{"key":"enter"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.Keys) != 1 || f.Keys[0] != "enter" {
		t.Fatalf("keys wrong: %+v", f.Keys)
	}
}

func TestDismissKeyboard(t *testing.T) {
	f := newFake()
	if _, err := dispatch("dismiss_keyboard", `{}`, f); err != nil {
		t.Fatal(err)
	}
	if !f.KeyboardDismissed {
		t.Fatal("keyboard not dismissed")
	}
}

func TestListApps(t *testing.T) {
	f := newFake()
	f.Apps = []wda.App{{BundleID: "com.x", Name: "X"}, {BundleID: "com.y", Name: "Y"}}
	m, err := dispatch("list_apps", `{"include_system":true}`, f)
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := m["apps"].([]any)
	if len(apps) != 2 {
		t.Fatalf("want 2 apps, got %v", m)
	}
	if f.ListAppsIncludeSystem[0] != true {
		t.Error("include_system not forwarded")
	}
}

func TestOpenApp(t *testing.T) {
	f := newFake()
	if _, err := dispatch("open_app", `{"package":"com.example.app"}`, f); err != nil {
		t.Fatal(err)
	}
	if len(f.LaunchedApps) != 1 || f.LaunchedApps[0] != "com.example.app" {
		t.Fatalf("launch wrong: %+v", f.LaunchedApps)
	}
}

func TestOpenAppMissingPackage(t *testing.T) {
	f := newFake()
	_, err := dispatch("open_app", `{}`, f)
	if errcode.From(err) != errcode.BadArgs {
		t.Fatalf("want bad_args, got %v", err)
	}
}

// ── get_screen_state ─────────────────────────────────────────────────────

const sampleSource = `<root><App><Window>
<Other name="" label="" value="" enabled="true" visible="true" x="0" y="0" width="375" height="812">
<Button name="Save" label="Save" value="" enabled="true" visible="true" x="100" y="200" width="80" height="44"/>
<TextField name="" label="" value="query" enabled="true" visible="true" x="100" y="300" width="200" height="40"/>
</Other>
</Window></App></root>`

func TestGetScreenStateTree(t *testing.T) {
	f := newFake()
	f.SourceXML = []byte(sampleSource)
	m, err := dispatch("get_screen_state", `{}`, f)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := m["tree"].(string)
	if !strings.Contains(tree, "node_id\tclass\ttext\tdesc\tres_id\tbounds\tflags") {
		t.Errorf("missing header in tree:\n%s", tree)
	}
	// Button "Save" and TextField value "query" should appear as kept rows.
	if !strings.Contains(tree, "Button") || !strings.Contains(tree, "query") {
		t.Errorf("kept rows missing expected nodes:\n%s", tree)
	}
	screen, _ := m["screen"].(map[string]any)
	if screen["w"].(float64) != 375 {
		t.Errorf("screen.w wrong: %v", screen)
	}
}

func TestGetScreenStateWithScreenshot(t *testing.T) {
	f := newFake()
	f.SourceXML = []byte(sampleSource)
	f.PNG = pngBytes(10, 10)
	m, err := dispatch("get_screen_state", `{"include_screenshot":true}`, f)
	if err != nil {
		t.Fatal(err)
	}
	shot, ok := m["screenshot"].(map[string]any)
	if !ok {
		t.Fatalf("missing screenshot: %v", m)
	}
	if shot["format"] != "jpeg" {
		t.Errorf("format = %v", shot["format"])
	}
}

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ── tap by node_id (resolves via source bounds center) ───────────────────

func TestTapByNodeID(t *testing.T) {
	f := newFake()
	f.SourceXML = []byte(sampleSource)
	if _, err := dispatch("tap", `{"node_id":"DUMMY"}`, f); err == nil {
		// node_id DUMMY isn't in the tree; expect stale_node
		t.Fatal("expected stale_node for unknown node_id")
	} else if errcode.From(err) != errcode.StaleNode {
		t.Fatalf("want stale_node, got %v", err)
	}
}
