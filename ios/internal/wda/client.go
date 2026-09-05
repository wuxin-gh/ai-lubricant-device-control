// Package wda is the WebDriverAgent HTTP client. It speaks the WebDriver
// (W3C) protocol against the WDA HTTP server running on the iPhone, which the
// host reaches through go-ios port forwarding (see internal/devicelink).
//
// The interface here is the integration boundary the command handlers depend
// on. A fake implementation backs the command tests, so the protocol layer is
// fully exercised without a real iPhone.
//
// WDA's source endpoint returns an XCUIElement tree as XML; internal/screen
// parses it into screen.Node for the §8.2 formatter. Touch synthesis,
// keyboard, screenshot, and app launch all map onto WDA's REST endpoints.
package wda

import (
	"context"
	"encoding/json"

	"device-control/ios/internal/screen"
)

// Client is theWebDriverAgent surface the command handlers use.
//
// Methods return raw JSON values (the call-response `data`) or throw errors
// that errcode.From maps onto wire codes. ctx carries the per-call timeout
// budget from the dispatcher; an HTTP request that respects ctx will be
// cancelled when the budget expires.
type Client interface {
	// Source returns the live XCUIElement tree, parsed into screen.Node roots
	// (one per window). Multi-window support is best-effort; WDA's primary
	// source is the active app.
	Source(ctx context.Context) (*screen.MultiWindowResult, error)

	// Screenshot returns a PNG (raw bytes) of the current screen.
	Screenshot(ctx context.Context) ([]byte, error)

	// ScreenInfo returns the current screen dimensions/orientation/scale.
	ScreenInfo(ctx context.Context) (screen.ScreenInfo, int, error)

	// Tap performs a coordinate tap (duration 0 = single tap). (x,y) are
	// physical pixels; the impl scales to WDA points before sending.
	Tap(ctx context.Context, x, y int, durationMS int) error

	// DoubleTap performs two taps at the coordinate.
	DoubleTap(ctx context.Context, x, y int) error

	// Swipe performs a drag from (x1,y1) to (x2,y2) over durationMS.
	Swipe(ctx context.Context, x1, y1, x2, y2, durationMS int) error

	// Scroll swipes the focused/target scrollable in a direction.
	// direction ∈ up/down/left/right.
	Scroll(ctx context.Context, direction string) error

	// TypeText types text into the focused editable.
	TypeText(ctx context.Context, text string) error

	// SetValue sets text on the node resolved by nodeID.
	SetValue(ctx context.Context, nodeID, text string) error

	// Key presses a §8.6 key (enter/tab/delete/escape/space/dpad_*).
	Key(ctx context.Context, key string) error

	// DismissKeyboard hides the on-screen keyboard if present.
	DismissKeyboard(ctx context.Context) error

	// Home presses the device home button.
	Home(ctx context.Context) error

	// Recents opens the app switcher.
	Recents(ctx context.Context) error

	// ListApps returns installed apps: {bundleID, name}.
	ListApps(ctx context.Context, includeSystem bool) ([]App, error)

	// LaunchApp launches the app with the given bundle ID.
	LaunchApp(ctx context.Context, bundleID string) error
}

// App is an installed iOS app: bundle ID (the iOS analogue of Android package)
// and its display name.
type App struct {
	BundleID string `json:"package"`
	Name     string `json:"name"`
}

// RawJSON is a convenience to make a call-response `data` from a Go value.
func RawJSON(v any) (json.RawMessage, error) {
	return json.Marshal(v)
}

// EmptyData is the ok=true empty payload for commands that return nothing.
var EmptyData = json.RawMessage(`{}`)
