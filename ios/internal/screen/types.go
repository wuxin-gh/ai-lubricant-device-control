// Package screen renders the iOS accessibility tree (from WebDriverAgent's
// source XML) as the compact TSV of protocol v0 §8.2.
//
// It is a faithful Go port of android/core/.../accessibility/CompactTreeFormatter.kt.
// The wire format this produces IS normative for §8.2: the column set, the flag
// tokens, the `-` null value, the 100-char truncation, and the `hierarchy:`
// block were all pinned to the Android formatter, and this port reproduces them
// byte-for-byte so the server cannot tell an iOS tree from an Android one.
//
// Changing the output here is a protocol change, not a refactor.
package screen

// Bounds is the screen bounds of a node, in physical pixels (spec §1: the wire
// never carries dp). Mirrors android BoundsData.
type Bounds struct {
	Left, Top, Right, Bottom int
}

// ScreenInfo is the screen dimensions/density/orientation header (spec §8.1).
// Density is an integer DPI (Android densityDpi); iOS has no densityDpi so the
// driver reports a scale-derived equivalent (best-effort, see devicelink).
type ScreenInfo struct {
	Width       int
	Height      int
	DensityDpi  int
	Orientation string
}

// Node is a parsed accessibility node — the Go analogue of
// android AccessibilityNodeData. It is filled from WDA's XCUIElement tree.
type Node struct {
	ID                 string
	ClassName          string
	Text               string
	ContentDescription string
	ResourceID         string
	Bounds             Bounds
	Clickable          bool
	LongClickable      bool
	Focusable          bool
	Scrollable         bool
	Editable           bool
	IsPassword         bool
	Enabled            bool
	Visible            bool
	// WebRole mirrors the Android webRole field: a Chromium DOM role populated
	// only on web content. iOS WDA does not expose this, so it stays empty and
	// the web-node no-truncation path is effectively unused (native nodes always
	// truncate). The field exists so the formatter logic is a faithful port.
	WebRole  string
	Children []*Node
}

// WindowData is a single window's parsed tree plus window metadata (spec §8.2
// multi-window header). Mirrors android WindowData.
type WindowData struct {
	WindowID     int
	WindowType   string
	PackageName  string
	Title        string
	ActivityName string // empty = omitted from the header
	Layer        int
	Focused      bool
	Tree         *Node
}

// MultiWindowResult is the set of on-screen windows (spec §8.2).
type MultiWindowResult struct {
	Windows  []*WindowData
	Degraded bool
}
