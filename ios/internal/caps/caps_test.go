package caps

import "testing"

// Ported from CapabilitiesTest.kt. v0 vocabulary is the complete 16, once.
func TestAllIsCompleteVocabulary(t *testing.T) {
	want := []string{
		"get_screen_state", "tap", "long_press", "double_tap", "swipe", "scroll",
		"scroll_to_node", "type_text", "set_text", "press_key", "dismiss_keyboard",
		"press_back", "press_home", "press_recents", "open_app", "list_apps",
	}
	if len(All) != len(want) {
		t.Fatalf("All has %d commands, want %d", len(All), len(want))
	}
	seen := map[string]int{}
	for _, c := range All {
		seen[c]++
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Errorf("command %q appears %d times, want 1", w, seen[w])
		}
	}
}

func TestForDeviceReturnsAll(t *testing.T) {
	got := ForDevice()
	if len(got) != len(All) {
		t.Fatalf("ForDevice returned %d, want %d", len(got), len(All))
	}
	// ForDevice returns a copy; mutating it must not affect All.
	got[0] = "MUTATED"
	if All[0] == "MUTATED" {
		t.Error("ForDevice did not return a copy")
	}
}
