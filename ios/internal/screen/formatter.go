package screen

import (
	"strings"
)

// CompactTreeFormatter renders a Node tree as the §8.2 compact TSV. Faithful
// port of android CompactTreeFormatter.kt.
type CompactTreeFormatter struct{}

// New returns a formatter.
func New() *CompactTreeFormatter { return &CompactTreeFormatter{} }

// Format renders a single-window tree with an app/activity header (the Android
// `format` entry, kept for parity; the multi-window path is FormatMultiWindow).
func (f *CompactTreeFormatter) Format(tree *Node, packageName, activityName string, screen ScreenInfo) string {
	var sb, hier strings.Builder
	sb.WriteString(noteLine + "\n")
	sb.WriteString(noteLineCustomElements + "\n")
	sb.WriteString(noteLineFlagsLegend + "\n")
	sb.WriteString(noteLineOffscreenHint + "\n")
	sb.WriteString("app:" + packageName + " activity:" + activityName + "\n")
	sb.WriteString("screen:" + itoa(screen.Width) + "x" + itoa(screen.Height) +
		" density:" + itoa(screen.DensityDpi) + " orientation:" + screen.Orientation + "\n")
	sb.WriteString(header + "\n")
	walk(tree, 0, []func(*Node, int){
		func(n *Node, _ int) { f.appendElementRow(&sb, n) },
		func(n *Node, depth int) {
			for i := 0; i < depth; i++ {
				hier.WriteString(hierarchyIndent)
			}
			hier.WriteString(n.ID)
			hier.WriteByte('\n')
		},
	})
	sb.WriteString(hierarchyHeader + "\n")
	out := sb.String() + hier.String()
	return strings.TrimRight(out, "\n")
}

// FormatMultiWindow renders every window with its own header + TSV + hierarchy
// section. Mirrors formatMultiWindow.
func (f *CompactTreeFormatter) FormatMultiWindow(result *MultiWindowResult, screen ScreenInfo) string {
	var sb, hier strings.Builder
	if result.Degraded {
		sb.WriteString(degradationNote + "\n")
	}
	sb.WriteString(noteLine + "\n")
	sb.WriteString(noteLineCustomElements + "\n")
	sb.WriteString(noteLineFlagsLegend + "\n")
	sb.WriteString(noteLineOffscreenHint + "\n")
	sb.WriteString("screen:" + itoa(screen.Width) + "x" + itoa(screen.Height) +
		" density:" + itoa(screen.DensityDpi) + " orientation:" + screen.Orientation + "\n")
	for _, w := range result.Windows {
		sb.WriteString(f.BuildWindowHeader(w) + "\n")
		sb.WriteString(header + "\n")
		hier.Reset()
		walk(w.Tree, 0, []func(*Node, int){
			func(n *Node, _ int) { f.appendElementRow(&sb, n) },
			func(n *Node, depth int) {
				for i := 0; i < depth; i++ {
					hier.WriteString(hierarchyIndent)
				}
				hier.WriteString(n.ID)
				hier.WriteByte('\n')
			},
		})
		sb.WriteString(hierarchyHeader + "\n")
		sb.WriteString(hier.String())
	}
	return strings.TrimRight(sb.String(), "\n")
}

// BuildWindowHeader mirrors buildWindowHeader. Activity is omitted when empty.
func (f *CompactTreeFormatter) BuildWindowHeader(w *WindowData) string {
	var b strings.Builder
	b.WriteString("--- ")
	b.WriteString("window:" + itoa(w.WindowID) + " ")
	b.WriteString("type:" + w.WindowType + " ")
	pkg := w.PackageName
	if pkg == "" {
		pkg = "unknown"
	}
	b.WriteString("pkg:" + pkg + " ")
	title := w.Title
	if title == "" {
		title = "unknown"
	}
	b.WriteString("title:" + title + " ")
	if w.ActivityName != "" {
		b.WriteString("activity:" + w.ActivityName + " ")
	}
	b.WriteString("layer:" + itoa(w.Layer) + " ")
	b.WriteString("focused:" + boolStr(w.Focused))
	b.WriteString(" ---")
	return b.String()
}

// CountKeptNodes returns the number of kept nodes across all windows (equals
// the number of TSV rows emitted). Mirrors countKeptNodes.
func (f *CompactTreeFormatter) CountKeptNodes(result *MultiWindowResult) int {
	var count func(*Node) int
	count = func(n *Node) int {
		c := 0
		if f.ShouldKeepNode(n) {
			c = 1
		}
		for _, ch := range n.Children {
			c += count(ch)
		}
		return c
	}
	total := 0
	for _, w := range result.Windows {
		total += count(w.Tree)
	}
	return total
}

// ShouldKeepNode mirrors shouldKeepNode: kept iff any of text/desc/res_id
// non-empty, or clickable/longClickable/scrollable/editable.
func (f *CompactTreeFormatter) ShouldKeepNode(n *Node) bool {
	return n.Text != "" ||
		n.ContentDescription != "" ||
		n.ResourceID != "" ||
		n.Clickable ||
		n.LongClickable ||
		n.Scrollable ||
		n.Editable
}

// AppendElementRow mirrors appendElementRow.
func (f *CompactTreeFormatter) AppendElementRow(sb *strings.Builder, n *Node) {
	className := f.SimplifyClassName(n.ClassName)
	// Merged WebView nodes carry collapsed content existing only in this output;
	// truncating would lose it, so web nodes are never truncated.
	truncate := n.WebRole == ""
	text := f.SanitizeText(n.Text, truncate)
	desc := f.SanitizeText(n.ContentDescription, truncate)
	resID := f.SanitizeResourceID(n.ResourceID)
	bounds := itoa(n.Bounds.Left) + "," + itoa(n.Bounds.Top) + "," +
		itoa(n.Bounds.Right) + "," + itoa(n.Bounds.Bottom)
	flags := f.BuildFlags(n)
	sb.WriteString(n.ID + sep + className + sep + text + sep + desc + sep + resID + sep + bounds + sep + flags + "\n")
}

func (f *CompactTreeFormatter) appendElementRow(sb *strings.Builder, n *Node) {
	f.AppendElementRow(sb, n)
}

// SimplifyClassName strips the package prefix. Mirrors simplifyClassName.
func (f *CompactTreeFormatter) SimplifyClassName(className string) string {
	if className == "" {
		return nullValue
	}
	if i := strings.LastIndex(className, "."); i >= 0 {
		return className[i+1:]
	}
	return className
}

// SanitizeText mirrors sanitizeText. Replaces tab/newline/cr with space,
// trims, returns "-" for empty, truncates to MaxTextLength + suffix unless
// truncate is false (merged WebView content).
func (f *CompactTreeFormatter) SanitizeText(text string, truncate bool) string {
	s := text
	if s != "" {
		s = strings.ReplaceAll(s, "\t", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return nullValue
	}
	if truncate && len(s) > maxTextLength {
		return s[:maxTextLength] + truncationSuffix
	}
	return s
}

// SanitizeResourceID mirrors sanitizeResourceId.
func (f *CompactTreeFormatter) SanitizeResourceID(resourceID string) string {
	if resourceID == "" {
		return nullValue
	}
	s := resourceID
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return nullValue
	}
	return s
}

// BuildFlags mirrors buildFlags: first token always on/off, then clk,lclk,foc,
// scr,edt,ena when true.
func (f *CompactTreeFormatter) BuildFlags(n *Node) string {
	var b strings.Builder
	if n.Visible {
		b.WriteString(flagOnscreen)
	} else {
		b.WriteString(flagOffscreen)
	}
	if n.Clickable {
		b.WriteString(flagSeparator + flagClickable)
	}
	if n.LongClickable {
		b.WriteString(flagSeparator + flagLongClickable)
	}
	if n.Focusable {
		b.WriteString(flagSeparator + flagFocusable)
	}
	if n.Scrollable {
		b.WriteString(flagSeparator + flagScrollable)
	}
	if n.Editable {
		b.WriteString(flagSeparator + flagEditable)
	}
	if n.Enabled {
		b.WriteString(flagSeparator + flagEnabled)
	}
	return b.String()
}

// walk mirrors walkTree: visits kept nodes with the visitors, advancing depth
// only across kept nodes (filtered nodes' children stay at the parent's depth).
func walk(n *Node, depth int, visitors []func(*Node, int)) {
	kept := (&CompactTreeFormatter{}).ShouldKeepNode(n)
	if kept {
		for _, v := range visitors {
			v(n, depth)
		}
	}
	childDepth := depth
	if kept {
		childDepth = depth + 1
	}
	for _, c := range n.Children {
		walk(c, childDepth, visitors)
	}
}

// itoa is strconv.Itoa without importing strconv into the formatter package
// (keeps the port self-contained; there are many int→string calls).
func itoa(i int) string {
	// strconv is fine; use it.
	return strconvItoa(i)
}
