// Package caps is the command set this iOS device declares at registration.
//
// It mirrors android/app/.../protocol/Capabilities.kt and matches spec §8. The
// server gates every inbound call against the declared list, so under-declaring
// is safe (the call never reaches the wire) and over-declaring is the bug (the
// caller discovers the gap only after a failed command).
//
// iOS notes: unlike Android there is no accessibility-IME gating (no API-33
// concept), so the full set is always declared. press_back is declared for
// parity even though iOS has no system-wide back; its handler returns a clear
// device_error so the caller learns the gap immediately rather than via an
// undeclared-command rejection.
package caps

// All is the complete v0 command vocabulary this driver implements, in spec §8
// order. Kept as one list, not scattered strings, so registration and the
// command dispatch table cannot drift.
var All = []string{
	"get_screen_state",
	"tap",
	"long_press",
	"double_tap",
	"swipe",
	"scroll",
	"scroll_to_node",
	"type_text",
	"set_text",
	"press_key",
	"dismiss_keyboard",
	"press_back",
	"press_home",
	"press_recents",
	"open_app",
	"list_apps",
}

// ForDevice returns the capability set for this iOS device. There is no
// platform-version gating on iOS (unlike Android's API-level IME gate), so
// this is the full list. The function exists for symmetry with the Android
// capability API and as the single place to add future gating.
func ForDevice() []string {
	out := make([]string, len(All))
	copy(out, All)
	return out
}
