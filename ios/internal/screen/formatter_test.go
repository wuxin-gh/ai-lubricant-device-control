package screen

import (
	"strings"
	"testing"
)

// Faithful port of android CompactTreeFormatterTest.kt. Pins the §8.2 wire
// format byte-for-byte against the Android formatter, so the server cannot
// tell an iOS tree from an Android one.

var defaultScreen = ScreenInfo{Width: 1080, Height: 2400, DensityDpi: 420, Orientation: "portrait"}

func makeNode(opts ...func(*Node)) *Node {
	n := &Node{ID: "node_test", Bounds: Bounds{0, 0, 100, 100}}
	for _, o := range opts {
		o(n)
	}
	return n
}

func withID(s string) func(*Node)         { return func(n *Node) { n.ID = s } }
func withClass(s string) func(*Node)      { return func(n *Node) { n.ClassName = s } }
func withText(s string) func(*Node)       { return func(n *Node) { n.Text = s } }
func withDesc(s string) func(*Node)       { return func(n *Node) { n.ContentDescription = s } }
func withRes(s string) func(*Node)        { return func(n *Node) { n.ResourceID = s } }
func withBounds(b Bounds) func(*Node)     { return func(n *Node) { n.Bounds = b } }
func withClick(b bool) func(*Node)        { return func(n *Node) { n.Clickable = b } }
func withLong(b bool) func(*Node)         { return func(n *Node) { n.LongClickable = b } }
func withFocus(b bool) func(*Node)        { return func(n *Node) { n.Focusable = b } }
func withScroll(b bool) func(*Node)       { return func(n *Node) { n.Scrollable = b } }
func withEdit(b bool) func(*Node)         { return func(n *Node) { n.Editable = b } }
func withEnabled(b bool) func(*Node)      { return func(n *Node) { n.Enabled = b } }
func withVisible(b bool) func(*Node)      { return func(n *Node) { n.Visible = b } }
func withChildren(c ...*Node) func(*Node) { return func(n *Node) { n.Children = c } }

// ── format ───────────────────────────────────────────────────────────────

func TestFormatNoteLines(t *testing.T) {
	f := New()
	out := f.Format(makeNode(withText("hello")), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	if lines[0] != noteLine {
		t.Errorf("line0: got %q", lines[0])
	}
	if lines[1] != noteLineCustomElements {
		t.Errorf("line1: got %q", lines[1])
	}
	if lines[2] != noteLineFlagsLegend {
		t.Errorf("line2: got %q", lines[2])
	}
	if lines[3] != noteLineOffscreenHint {
		t.Errorf("line3: got %q", lines[3])
	}
}

func TestFormatMetadataLines(t *testing.T) {
	f := New()
	out := f.Format(makeNode(withText("hello")), "com.example.app", ".MainActivity", defaultScreen)
	lines := strings.Split(out, "\n")
	if lines[4] != "app:com.example.app activity:.MainActivity" {
		t.Errorf("app line: got %q", lines[4])
	}
	if lines[5] != "screen:1080x2400 density:420 orientation:portrait" {
		t.Errorf("screen line: got %q", lines[5])
	}
}

func TestFormatHeaderLine(t *testing.T) {
	f := New()
	out := f.Format(makeNode(withText("hello")), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	if lines[6] != "node_id\tclass\ttext\tdesc\tres_id\tbounds\tflags" {
		t.Errorf("header: got %q", lines[6])
	}
}

func TestFormatKeptNodeRow(t *testing.T) {
	f := New()
	out := f.Format(makeNode(
		withID("node_btn"), withClass("android.widget.Button"), withText("OK"),
		withRes("btn_ok"), withBounds(Bounds{100, 200, 300, 260}),
		withClick(true), withVisible(true), withEnabled(true),
	), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	want := "node_btn\tButton\tOK\t-\tbtn_ok\t100,200,300,260\ton,clk,ena"
	if lines[7] != want {
		t.Errorf("row:\n got: %q\nwant: %q", lines[7], want)
	}
}

func TestFormatFiltersNoiseNodes(t *testing.T) {
	f := New()
	out := f.Format(makeNode(
		withID("node_frame"), withClass("android.widget.FrameLayout"),
		withBounds(Bounds{0, 0, 1080, 2400}),
	), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	// notes + metadata + header = 7 lines + hierarchy: label = 8 lines, no data rows
	if len(lines) != 8 {
		t.Fatalf("want 8 lines, got %d (%q)", len(lines), out)
	}
}

func TestFormatIncludesChildrenOfFiltered(t *testing.T) {
	f := New()
	child := makeNode(
		withID("node_child"), withClass("android.widget.Button"), withText("Click me"),
		withClick(true), withVisible(true), withEnabled(true),
	)
	parent := makeNode(
		withID("node_parent"), withClass("android.widget.FrameLayout"),
		withChildren(child),
	)
	out := f.Format(parent, "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("want 10 lines, got %d (%q)", len(lines), out)
	}
	if !strings.HasPrefix(lines[7], "node_child\t") {
		t.Errorf("line7 should start with node_child: %q", lines[7])
	}
}

func TestFormatIncludesNonVisibleKeptNodes(t *testing.T) {
	f := New()
	out := f.Format(makeNode(
		withID("node_hidden"), withClass("android.widget.Button"), withText("Hidden"),
		withVisible(false), withEnabled(true),
	), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("want 10 lines, got %d", len(lines))
	}
	flags := strings.Split(lines[7], "\t")
	if !strings.HasPrefix(flags[len(flags)-1], "off") {
		t.Errorf("flags should start with off: %q", flags[len(flags)-1])
	}
}

func TestFormatAllFiltered(t *testing.T) {
	f := New()
	out := f.Format(makeNode(
		withID("node_root"), withClass("android.widget.FrameLayout"),
		withChildren(makeNode(withID("node_inner"), withClass("android.widget.LinearLayout"))),
	), "com.example", ".Main", defaultScreen)
	lines := strings.Split(out, "\n")
	if len(lines) != 8 {
		t.Fatalf("want 8 lines, got %d (%q)", len(lines), out)
	}
}

// ── hierarchy ────────────────────────────────────────────────────────────

func extractHierarchy(out string) []string {
	lines := strings.Split(out, "\n")
	idx := -1
	for i, l := range lines {
		if l == hierarchyHeader {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	return lines[idx+1:]
}

func TestHierarchySingleKeptNode(t *testing.T) {
	f := New()
	out := f.Format(makeNode(withID("node_a"), withText("Hello"), withVisible(true)), "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	if len(h) != 1 || h[0] != "node_a" {
		t.Fatalf("want [node_a], got %v", h)
	}
}

func TestHierarchyParentChildIndent(t *testing.T) {
	f := New()
	child := makeNode(withID("node_child"), withText("Child"), withVisible(true))
	parent := makeNode(withID("node_parent"), withText("Parent"), withVisible(true), withChildren(child))
	out := f.Format(parent, "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	want := []string{"node_parent", "  node_child"}
	if len(h) != 2 || h[0] != want[0] || h[1] != want[1] {
		t.Fatalf("want %v, got %v", want, h)
	}
}

func TestHierarchyFilteredParentPromotesChildren(t *testing.T) {
	f := New()
	child := makeNode(withID("node_child"), withText("Child"), withVisible(true))
	parent := makeNode(withID("node_structural"), withClass("android.widget.FrameLayout"), withChildren(child))
	out := f.Format(parent, "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	if len(h) != 1 || h[0] != "node_child" {
		t.Fatalf("want [node_child], got %v", h)
	}
}

func TestHierarchyDeepNesting(t *testing.T) {
	f := New()
	gc := makeNode(withID("node_gc"), withText("GC"), withVisible(true))
	c := makeNode(withID("node_c"), withText("C"), withVisible(true), withChildren(gc))
	r := makeNode(withID("node_r"), withText("R"), withVisible(true), withChildren(c))
	out := f.Format(r, "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	want := []string{"node_r", "  node_c", "    node_gc"}
	if len(h) != 3 || h[0] != want[0] || h[1] != want[1] || h[2] != want[2] {
		t.Fatalf("want %v, got %v", want, h)
	}
}

func TestHierarchyMultipleChildrenSameLevel(t *testing.T) {
	f := New()
	c1 := makeNode(withID("node_c1"), withText("C1"), withVisible(true))
	c2 := makeNode(withID("node_c2"), withText("C2"), withVisible(true))
	p := makeNode(withID("node_p"), withText("P"), withVisible(true), withChildren(c1, c2))
	out := f.Format(p, "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	want := []string{"node_p", "  node_c1", "  node_c2"}
	if len(h) != 3 || h[0] != want[0] || h[1] != want[1] || h[2] != want[2] {
		t.Fatalf("want %v, got %v", want, h)
	}
}

func TestHierarchyMixedKeptFilteredDeep(t *testing.T) {
	f := New()
	leaf := makeNode(withID("node_leaf"), withText("Leaf"), withVisible(true))
	mid := makeNode(withID("node_mid"), withClass("android.widget.FrameLayout"), withChildren(leaf))
	root := makeNode(withID("node_root"), withText("Root"), withVisible(true), withChildren(mid))
	out := f.Format(root, "com.example", ".Main", defaultScreen)
	h := extractHierarchy(out)
	want := []string{"node_root", "  node_leaf"}
	if len(h) != 2 || h[0] != want[0] || h[1] != want[1] {
		t.Fatalf("want %v, got %v", want, h)
	}
}

// ── shouldKeepNode ───────────────────────────────────────────────────────

func TestShouldKeepNode(t *testing.T) {
	f := New()
	checks := []struct {
		name string
		node *Node
		want bool
	}{
		{"text", makeNode(withText("hello")), true},
		{"desc", makeNode(withDesc("desc")), true},
		{"resid", makeNode(withRes("com.example:id/btn")), true},
		{"click", makeNode(withClick(true)), true},
		{"long", makeNode(withLong(true)), true},
		{"scroll", makeNode(withScroll(true)), true},
		{"edit", makeNode(withEdit(true)), true},
		{"empty", makeNode(), false},
	}
	for _, c := range checks {
		if got := f.ShouldKeepNode(c.node); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// ── simplifyClassName ───────────────────────────────────────────────────

func TestSimplifyClassName(t *testing.T) {
	f := New()
	if got := f.SimplifyClassName("android.widget.Button"); got != "Button" {
		t.Errorf("strip: %q", got)
	}
	if got := f.SimplifyClassName("Button"); got != "Button" {
		t.Errorf("nodash: %q", got)
	}
	if got := f.SimplifyClassName(""); got != "-" {
		t.Errorf("empty: %q", got)
	}
}

// ── sanitizeText ─────────────────────────────────────────────────────────

func TestSanitizeText(t *testing.T) {
	f := New()
	if got := f.SanitizeText("hello\tworld", true); got != "hello world" {
		t.Errorf("tab: %q", got)
	}
	if got := f.SanitizeText("hello\nworld", true); got != "hello world" {
		t.Errorf("nl: %q", got)
	}
	if got := f.SanitizeText("hello\rworld", true); got != "hello world" {
		t.Errorf("cr: %q", got)
	}
	if got := f.SanitizeText("", true); got != "-" {
		t.Errorf("empty: %q", got)
	}
	if got := f.SanitizeText("\t\n", true); got != "-" {
		t.Errorf("ws: %q", got)
	}
	long := strings.Repeat("a", 150)
	if got := f.SanitizeText(long, true); got != strings.Repeat("a", 100)+"...truncated" {
		t.Errorf("trunc: got len %d", len(got))
	}
	exact := strings.Repeat("a", 100)
	if got := f.SanitizeText(exact, true); got != exact {
		t.Errorf("exact: got len %d", len(got))
	}
	over := strings.Repeat("a", 101)
	if got := f.SanitizeText(over, true); got != strings.Repeat("a", 100)+"...truncated" {
		t.Errorf("over: got len %d", len(got))
	}
	// truncate=false preserves merged WebView content
	if got := f.SanitizeText(long, false); got != long {
		t.Errorf("notrunc: got len %d", len(got))
	}
}

func TestAppendElementRowWebVsNative(t *testing.T) {
	f := New()
	long := strings.Repeat("b", 150)
	web := &Node{ID: "w", Bounds: Bounds{0, 0, 1, 1}, Text: long, WebRole: "article"}
	native := &Node{ID: "n", Bounds: Bounds{0, 0, 1, 1}, Text: long}
	var wb, nb strings.Builder
	f.AppendElementRow(&wb, web)
	f.AppendElementRow(&nb, native)
	if !strings.Contains(wb.String(), long) {
		t.Error("web node text must not be truncated")
	}
	if !strings.Contains(nb.String(), "...truncated") {
		t.Error("native node text must still truncate")
	}
}

// ── sanitizeResourceId ───────────────────────────────────────────────────

func TestSanitizeResourceID(t *testing.T) {
	f := New()
	if got := f.SanitizeResourceID(""); got != "-" {
		t.Errorf("empty: %q", got)
	}
	long := "com.example.very.long.package:id/" + strings.Repeat("a", 200)
	if got := f.SanitizeResourceID(long); got != long {
		t.Errorf("long id should not truncate")
	}
}

// ── buildFlags ───────────────────────────────────────────────────────────

func TestBuildFlags(t *testing.T) {
	f := New()
	if got := f.BuildFlags(makeNode(
		withVisible(true), withClick(true), withLong(true), withFocus(true),
		withScroll(true), withEdit(true), withEnabled(true),
	)); got != "on,clk,lclk,foc,scr,edt,ena" {
		t.Errorf("all: %q", got)
	}
	if got := f.BuildFlags(makeNode(withVisible(true), withClick(true), withEnabled(true))); got != "on,clk,ena" {
		t.Errorf("subset: %q", got)
	}
	if got := f.BuildFlags(makeNode()); got != "off" {
		t.Errorf("none: %q", got)
	}
	if got := f.BuildFlags(makeNode(withVisible(false), withClick(true), withEnabled(true))); got != "off,clk,ena" {
		t.Errorf("off+flags: %q", got)
	}
	if got := f.BuildFlags(makeNode(withVisible(true))); got != "on" {
		t.Errorf("only on: %q", got)
	}
	if got := f.BuildFlags(makeNode(
		withVisible(false), withClick(true), withLong(true), withFocus(true),
		withScroll(true), withEdit(true), withEnabled(true),
	)); got != "off,clk,lclk,foc,scr,edt,ena" {
		t.Errorf("off+all: %q", got)
	}
}

// ── formatMultiWindow ───────────────────────────────────────────────────

func win(id string, wtype, pkg, title string, focused bool, tree *Node) *WindowData {
	return &WindowData{WindowID: 0, WindowType: wtype, PackageName: pkg, Title: title, Layer: 0, Focused: focused, Tree: tree}
}

func TestFormatMultiWindowSingle(t *testing.T) {
	f := New()
	tree := makeNode(
		withID("node_btn"), withClass("android.widget.Button"), withText("OK"),
		withBounds(Bounds{0, 0, 100, 48}), withClick(true), withVisible(true), withEnabled(true),
	)
	res := &MultiWindowResult{Windows: []*WindowData{
		{WindowID: 0, WindowType: "APPLICATION", PackageName: "com.example.app", Title: "MainActivity",
			ActivityName: "com.example.app.MainActivity", Layer: 0, Focused: true, Tree: tree},
	}}
	out := f.FormatMultiWindow(res, defaultScreen)
	if !strings.Contains(out, "screen:1080x2400") {
		t.Error("missing screen line")
	}
	wantHeader := "--- window:0 type:APPLICATION pkg:com.example.app title:MainActivity activity:com.example.app.MainActivity layer:0 focused:true ---"
	if !strings.Contains(out, wantHeader) {
		t.Errorf("missing window header; got:\n%s", out)
	}
	if !strings.Contains(out, "node_btn") {
		t.Error("missing node_btn")
	}
	if strings.Contains(out, degradationNote) {
		t.Error("degradation note should be absent")
	}
}

func TestFormatMultiWindowTwoWindows(t *testing.T) {
	f := New()
	app := makeNode(withID("node_app"), withText("App"), withClick(true), withVisible(true))
	dialog := makeNode(withID("node_allow"), withText("Allow"), withClick(true), withVisible(true))
	res := &MultiWindowResult{Windows: []*WindowData{
		{WindowID: 0, WindowType: "APPLICATION", PackageName: "com.example", Title: "Main", Layer: 0, Focused: false, Tree: app},
		{WindowID: 1, WindowType: "SYSTEM", PackageName: "com.android.permissioncontroller", Title: "Permission", Layer: 1, Focused: true, Tree: dialog},
	}}
	out := f.FormatMultiWindow(res, defaultScreen)
	for _, want := range []string{"--- window:0 type:APPLICATION", "--- window:1 type:SYSTEM", "node_app", "node_allow"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestBuildWindowHeaderActivityOmitted(t *testing.T) {
	f := New()
	h := f.BuildWindowHeader(&WindowData{
		WindowID: 0, WindowType: "SYSTEM", PackageName: "com.android.systemui",
		Title: "StatusBar", ActivityName: "", Layer: 0, Focused: false, Tree: makeNode(withID("x")),
	})
	if strings.Contains(h, "activity:") {
		t.Errorf("activity should be omitted: %q", h)
	}
}

func TestBuildWindowHeaderAllFields(t *testing.T) {
	f := New()
	h := f.BuildWindowHeader(&WindowData{
		WindowID: 2, WindowType: "INPUT_METHOD", PackageName: "com.google.android.inputmethod.latin",
		Title: "Gboard", ActivityName: "", Layer: 5, Focused: false, Tree: makeNode(withID("x")),
	})
	want := "--- window:2 type:INPUT_METHOD pkg:com.google.android.inputmethod.latin title:Gboard layer:5 focused:false ---"
	if h != want {
		t.Errorf("got %q want %q", h, want)
	}
}

func TestDegradedModeAddsNote(t *testing.T) {
	f := New()
	tree := makeNode(withID("node_x"), withText("X"), withClick(true), withVisible(true))
	res := &MultiWindowResult{Windows: []*WindowData{
		{WindowID: 0, WindowType: "APPLICATION", Tree: tree, Focused: true},
	}, Degraded: true}
	out := f.FormatMultiWindow(res, defaultScreen)
	if !strings.HasPrefix(out, degradationNote) {
		t.Error("degraded output should start with the degradation note")
	}
}

func TestNullPackageAndTitleRenderAsUnknown(t *testing.T) {
	f := New()
	tree := makeNode(withID("node_x"), withText("X"), withClick(true), withVisible(true))
	res := &MultiWindowResult{Windows: []*WindowData{
		{WindowID: 0, WindowType: "APPLICATION", Title: "", PackageName: "", Tree: tree, Focused: true},
	}}
	out := f.FormatMultiWindow(res, defaultScreen)
	if !strings.Contains(out, "pkg:unknown") || !strings.Contains(out, "title:unknown") {
		t.Errorf("missing unknown markers:\n%s", out)
	}
}

func TestEachWindowHasHierarchySection(t *testing.T) {
	f := New()
	app := makeNode(withID("node_app"), withText("App"), withClick(true), withVisible(true))
	dialog := makeNode(withID("node_dialog"), withText("Dialog"), withClick(true), withVisible(true))
	res := &MultiWindowResult{Windows: []*WindowData{
		{WindowID: 0, WindowType: "APPLICATION", PackageName: "com.example", Title: "Main", Layer: 0, Focused: false, Tree: app},
		{WindowID: 1, WindowType: "SYSTEM", PackageName: "com.android.system", Title: "Dialog", Layer: 1, Focused: true, Tree: dialog},
	}}
	lines := strings.Split(f.FormatMultiWindow(res, defaultScreen), "\n")
	hIdx := []int{}
	for i, l := range lines {
		if l == hierarchyHeader {
			hIdx = append(hIdx, i)
		}
	}
	if len(hIdx) != 2 {
		t.Fatalf("want 2 hierarchy sections, got %d", len(hIdx))
	}
	if lines[hIdx[0]+1] != "node_app" {
		t.Errorf("first hierarchy node: %q", lines[hIdx[0]+1])
	}
	if lines[hIdx[1]+1] != "node_dialog" {
		t.Errorf("second hierarchy node: %q", lines[hIdx[1]+1])
	}
}
