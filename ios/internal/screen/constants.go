package screen

import "strconv"

// Wire-format constants. Mirrors CompactTreeFormatter.kt's companion object.
// These are the §8.2 normative values — changing one is a protocol change.
const (
	nullValue        = "-"
	maxTextLength    = 100
	truncationSuffix = "...truncated"

	noteLine               = "note:structural-only nodes are omitted from the tree"
	noteLineCustomElements = "note:certain elements are custom and will not be properly reported, " +
		"if needed or if tools are not working as expected set " +
		"include_screenshot=true to see the screen and take what you see into account"
	noteLineFlagsLegend = "note:flags: on=onscreen off=offscreen clk=clickable lclk=longClickable " +
		"foc=focusable scr=scrollable edt=editable ena=enabled"
	noteLineOffscreenHint = "note:offscreen items require scroll_to_node before interaction"

	flagOnscreen      = "on"
	flagOffscreen     = "off"
	flagClickable     = "clk"
	flagLongClickable = "lclk"
	flagFocusable     = "foc"
	flagScrollable    = "scr"
	flagEditable      = "edt"
	flagEnabled       = "ena"
	flagSeparator     = ","

	hierarchyHeader = "hierarchy:"
	hierarchyIndent = "  "

	header = "node_id" + sep + "class" + sep + "text" + sep +
		"desc" + sep + "res_id" + sep + "bounds" + sep + "flags"

	degradationNote = "note:DEGRADED — multi-window unavailable, only active window reported"

	// PageSize is §8.7's 200 kept nodes per cursor page (ARC-MCP PAGE_SIZE).
	PageSize = 200
)

const sep = "\t"

// strconvItoa wraps strconv.Itoa so the formatter callsites read cleanly.
func strconvItoa(i int) string {
	return strconv.Itoa(i)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
