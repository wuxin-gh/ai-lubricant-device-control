// WDA HTTP routes and W3C action payload builders.
//
// REAL-DEVICE GATE: the route strings below are the contract with a specific
// WebDriverAgent build (the Appium fork). They have not been verified against
// a live iPhone yet. The central table exists so a fork/route divergence found
// during device verification is a one-line fix here, not a hunt through the
// client. Touch synthesis deliberately uses only W3C Actions payloads
// (/session/{id}/actions) — every WDA build implements those — instead of the
// fork-specific convenience routes (/tap, /touchAndHold, ...) the pre-alpha
// client used to call.
package wda

import "strings"

// WDA device-side port. The runner is launched with USE_PORT=8100 so the
// device-side endpoint is deterministic; the host-side forwarded port is what
// the HTTP client dials and is allocated dynamically by devicelink.
const DeviceWDAPort = 8100

// Session-less routes.
const (
	routeStatus     = "/status"  // server readiness + capabilities
	routeNewSession = "/session" // POST create session
)

// sessionRoute builds /session/{id}/{parts...}. Session-less routes are the
// constants above; DELETE /session/{id} is routeDeleteSession.
func sessionRoute(session string, parts ...string) string {
	var b strings.Builder
	b.WriteString("/session/")
	b.WriteString(session)
	for _, p := range parts {
		b.WriteString("/")
		b.WriteString(p)
	}
	return b.String()
}

// Session-scoped route builders (called with the live session id).
func routeSource(s string) string            { return sessionRoute(s, "source") }
func routeScreenshot(s string) string        { return sessionRoute(s, "screenshot") }
func routeWindowSize(s string) string        { return sessionRoute(s, "window", "size") }
func routeScreen(s string) string            { return sessionRoute(s, "wda", "screen") }
func routeElement(s string) string           { return sessionRoute(s, "element") }
func routeElementValue(s, eid string) string { return sessionRoute(s, "element", eid, "value") }
func routeActions(s string) string           { return sessionRoute(s, "actions") }
func routeLaunchApp(s string) string         { return sessionRoute(s, "wda", "launchApp") }
func routeDismissKeyboard(s string) string   { return sessionRoute(s, "wda", "keyboard", "dismiss") }
func routePressButton(s string) string       { return sessionRoute(s, "wda", "pressButton") }
func routeKeys(s string) string              { return sessionRoute(s, "keys") }
func routeDeleteSession(s string) string     { return "/session/" + s }

// ── W3C Actions payloads ─────────────────────────────────────────────────
//
// WDA rect coordinates are POINTS; the command layer passes physical PIXELS.
// Callers must divide by scale before building a payload — the builders here
// take points and are the single place that knows the wire shape.

// actionPointer builds the pointerParameters + actionSequence skeleton shared
// by every touch payload: one "touch" pointer with the given action items.
func actionPointer(items ...map[string]any) map[string]any {
	return map[string]any{
		"type":       "pointer",
		"id":         "touch1",
		"parameters": map[string]any{"pointerType": "touch"},
		"actions":    items,
	}
}

// emptyActionSource is the W3C actions envelope: one pointer sequence.
func emptyActionSource(seq map[string]any) map[string]any {
	return map[string]any{"actions": []map[string]any{seq}}
}

// ptrPause is a W3C pause action item.
func ptrPause(ms int) map[string]any {
	return map[string]any{"type": "pause", "duration": ms}
}

// ptrMove is a W3C pointerMove to absolute viewport coordinates (points).
func ptrMove(x, y int) map[string]any {
	return map[string]any{"type": "pointerMove", "x": x, "y": y}
}

// ptrDown/ptrUp are the contact lifecycle items.
func ptrDown() map[string]any { return map[string]any{"type": "pointerDown", "button": 0} }
func ptrUp() map[string]any   { return map[string]any{"type": "pointerUp", "button": 0} }

// TapPayload is a single tap at (x,y) points.
func TapPayload(x, y int) map[string]any {
	return emptyActionSource(actionPointer(ptrMove(x, y), ptrDown(), ptrPause(50), ptrUp()))
}

// LongPressPayload holds contact at (x,y) for durationMS.
func LongPressPayload(x, y, durationMS int) map[string]any {
	d := durationMS
	if d <= 0 {
		d = 500
	}
	return emptyActionSource(actionPointer(ptrMove(x, y), ptrDown(), ptrPause(d), ptrUp()))
}

// DoubleTapPayload is two taps with a 150ms gap (iOS double-tap window).
func DoubleTapPayload(x, y int) map[string]any {
	return emptyActionSource(actionPointer(
		ptrMove(x, y), ptrDown(), ptrPause(50), ptrUp(),
		ptrPause(150),
		ptrMove(x, y), ptrDown(), ptrPause(50), ptrUp(),
	))
}

// SwipePayload drags from (x1,y1) to (x2,y2) over durationMS.
func SwipePayload(x1, y1, x2, y2, durationMS int) map[string]any {
	d := durationMS
	if d <= 0 {
		d = 300
	}
	return emptyActionSource(actionPointer(
		ptrMove(x1, y1), ptrDown(), ptrPause(d/3),
		ptrMove(x2, y2), ptrPause(d/3),
		ptrUp(),
	))
}

// AppSwitcherPayload approximates the iOS app-switcher gesture: a short swipe
// up from the very bottom of the screen. WDA has no direct endpoint; this is
// the standard approximation. Requires screen size in points.
func AppSwitcherPayload(w, h int) map[string]any {
	return SwipePayload(w/2, h-4, w/2, h/2, 250)
}

// ElementQueryPayload matches the node_id the command layer carries. WDA
// element ids come from a prior tree snapshot; the §8.2 formatter emits them
// via ResourceID, so we query by accessibility id and fall back to nothing —
// a node_id that is not an accessibility id yields "no element" and the
// command layer surfaces that as an invalid_node error.
func ElementQueryPayload(nodeID string) map[string]any {
	return map[string]any{
		"using": "accessibility id",
		"value": nodeID,
	}
}

// SetValuePayload sets text on a resolved element.
func SetValuePayload(text string) map[string]any {
	return map[string]any{"value": []string{text}}
}

// LaunchAppPayload launches a bundle id (optionally stopping it first).
func LaunchAppPayload(bundleID string, stopIfRunning bool) map[string]any {
	return map[string]any{"bundleId": bundleID, "shouldWaitForQuiescence": true, "stopIfRunning": stopIfRunning}
}

// NewSessionPayload is the W3C new-session request against WDA. It targets
// the app-under-test as "com.apple.springboard" so WDA does not launch/kill a
// specific app on session create — the driver manages apps itself via
// launchApp.
func NewSessionPayload() map[string]any {
	return map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"bundleId":           "com.apple.springboard",
				"shouldUseCompactResponses": false,
				"maximumIdpCookieLength":    0,
			},
			"firstMatch": []map[string]any{{}},
		},
	}
}

// PressHomePayload targets /session/{id}/wda/pressButton.
func PressHomePayload() map[string]any {
	return map[string]any{"name": "home"}
}

// KeyPayload synthesizes key input as an Appium WDA /keys sequence.
func KeyPayload(key string) map[string]any {
	return map[string]any{"value": []string{key}}
}
