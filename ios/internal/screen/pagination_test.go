package screen

import (
	"strings"
	"testing"
)

// Ported from PaginationTestTrees.kt — deterministic trees with a controllable
// kept-node count and a known kept-ancestor chain, so pagination tests can
// assert page boundaries and kept-ancestor closure in the hierarchy.

func keptNodeWindow(keptLeafCount, ancestorDepth, windowID int, packageName string) *WindowData {
	if ancestorDepth < 1 {
		panic("ancestorDepth must be >= 1")
	}
	leaves := make([]*Node, 0, keptLeafCount)
	for i := 0; i < keptLeafCount; i++ {
		leaves = append(leaves, &Node{
			ID: "node_leaf_" + itoa(i), ClassName: "android.widget.TextView",
			Text: "leaf" + itoa(i), Bounds: Bounds{0, i, 100, i + 1},
			Enabled: true, Visible: true,
		})
	}
	node := container(ancestorDepth-1, leaves)
	for depth := ancestorDepth - 2; depth >= 0; depth-- {
		node = container(depth, []*Node{node})
	}
	return &WindowData{
		WindowID: windowID, WindowType: "APPLICATION", PackageName: packageName,
		Title: "Test", Focused: true, Tree: node,
	}
}

func container(index int, children []*Node) *Node {
	return &Node{
		ID: "node_anc_" + itoa(index), ClassName: "android.widget.FrameLayout",
		ResourceID: "com.example.app:id/anc_" + itoa(index),
		Bounds:     Bounds{0, 0, 100, 100}, Enabled: true, Visible: true,
		Children: children,
	}
}

func resultOf(window *WindowData, degraded bool) *MultiWindowResult {
	return &MultiWindowResult{Windows: []*WindowData{window}, Degraded: degraded}
}

func snapshotOf(result *MultiWindowResult, id string) *Snapshot {
	f := New()
	total := f.CountKeptNodes(result)
	return &Snapshot{ID: id, Result: result, ScreenInfo: defaultScreen, TotalKeptNodes: total, TotalPages: PageCount(total)}
}

func tsvRows(pageText string) []string {
	var out []string
	for _, l := range splitLines(pageText) {
		if strings.Contains(l, "\t") && !strings.HasPrefix(l, "node_id\t") {
			out = append(out, l)
		}
	}
	return out
}

func hierarchyLines(pageText string) []string {
	lines := splitLines(pageText)
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

func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

func TestCountKeptNodesMatchesRowCount(t *testing.T) {
	f := New()
	res := resultOf(keptNodeWindow(50, 2, 1, "com.example.app"), false)
	rows := tsvRows(f.FormatMultiWindow(res, defaultScreen))
	if len(rows) != f.CountKeptNodes(res) {
		t.Errorf("count=%d rows=%d", f.CountKeptNodes(res), len(rows))
	}
}

func TestPage1ContainsFirst200Rows(t *testing.T) {
	// ancestorDepth=2 + 448 leaves = 450 kept -> 3 pages (200/200/50)
	f := New()
	res := resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), false)
	page := f.FormatMultiWindowPage(snapshotOf(res, "snap1"), 1)
	if !contains(page, "page:1/3 snapshot:snap1 nodes:1-200/450") {
		t.Errorf("page header wrong:\n%s", page)
	}
	if len(tsvRows(page)) != 200 {
		t.Errorf("want 200 rows, got %d", len(tsvRows(page)))
	}
	for _, want := range []string{"node_anc_0\t", "node_leaf_0\t", "node_leaf_197\t"} {
		if !contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	if contains(page, "node_leaf_198\t") {
		t.Error("page 1 must not contain node_leaf_198")
	}
}

func TestPage2ContainsRows201To400(t *testing.T) {
	f := New()
	res := resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), false)
	page := f.FormatMultiWindowPage(snapshotOf(res, "snap1"), 2)
	if !contains(page, "page:2/3 snapshot:snap1 nodes:201-400/450") {
		t.Errorf("page header wrong:\n%s", page)
	}
	if len(tsvRows(page)) != 200 {
		t.Errorf("want 200 rows, got %d", len(tsvRows(page)))
	}
	for _, want := range []string{"node_leaf_198\t", "node_leaf_397\t"} {
		if !contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, skip := range []string{"node_leaf_197\t", "node_leaf_398\t"} {
		if contains(page, skip) {
			t.Errorf("page 2 must not contain %q", skip)
		}
	}
}

func TestLastPageHasEndNote(t *testing.T) {
	f := New()
	snap := snapshotOf(resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), false), "snap1")
	if !contains(f.FormatMultiWindowPage(snap, 1), "cursor \"snap1.2\"") {
		t.Error("page 1 should reference next cursor snap1.2")
	}
	if !contains(f.FormatMultiWindowPage(snap, 2), "cursor \"snap1.3\"") {
		t.Error("page 2 should reference next cursor snap1.3")
	}
	last := f.FormatMultiWindowPage(snap, 3)
	if !contains(last, "note:end of snapshot (page 3/3)") {
		t.Error("last page should have end note")
	}
	if contains(last, "cursor \"snap1.4\"") {
		t.Error("last page must not reference a next cursor")
	}
}

func TestPageHierarchyIncludesKeptAncestors(t *testing.T) {
	f := New()
	res := resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), false)
	page2 := f.FormatMultiWindowPage(snapshotOf(res, "snap1"), 2)
	hier := hierarchyLines(page2)
	if !containsStr(hier, "node_anc_0") || !containsStr(hier, "  node_anc_1") {
		t.Errorf("page hierarchy should include kept ancestors: %v", hier)
	}
	// ...but NOT as TSV rows on page 2 (their rows are on page 1)
	if contains(page2, "node_anc_0\t") || contains(page2, "node_anc_1\t") {
		t.Error("ancestors must not be TSV rows on page 2")
	}
}

func TestPageHierarchyIndentationMatchesFull(t *testing.T) {
	f := New()
	res := resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), false)
	full := f.FormatMultiWindow(res, defaultScreen)
	page2 := f.FormatMultiWindowPage(snapshotOf(res, "snap1"), 2)
	// leaf_198 sits at depth 2 (anc_0 -> anc_1 -> leaf) => 4-space indent, in BOTH
	if !contains(full, "    node_leaf_198") || !contains(page2, "    node_leaf_198") {
		t.Error("leaf_198 should be 4-space-indented in both outputs")
	}
}

func TestWindowHeaderRepeatsAcrossPages(t *testing.T) {
	f := New()
	w := keptNodeWindow(448, 2, 7, "com.span")
	snap := snapshotOf(resultOf(w, false), "snap1")
	header := f.BuildWindowHeader(w)
	if !contains(f.FormatMultiWindowPage(snap, 1), header) || !contains(f.FormatMultiWindowPage(snap, 2), header) {
		t.Error("window header should repeat on both pages")
	}
}

func TestMultiWindowPageBoundary(t *testing.T) {
	f := New()
	w1 := keptNodeWindow(148, 2, 1, "com.app1")
	w2 := keptNodeWindow(98, 2, 2, "com.app2")
	// w1=150 kept, w2=100 kept, total=250 -> 2 pages; page 2 (rows 201-250) is window 2 only
	snap := snapshotOf(&MultiWindowResult{Windows: []*WindowData{w1, w2}}, "snap1")
	page2 := f.FormatMultiWindowPage(snap, 2)
	if !contains(page2, "page:2/2 snapshot:snap1 nodes:201-250/250") {
		t.Errorf("header wrong:\n%s", page2)
	}
	if !contains(page2, "window:2 ") || !contains(page2, "pkg:com.app2 ") {
		t.Error("page 2 should be window 2 only")
	}
	if contains(page2, "window:1 ") || contains(page2, "pkg:com.app1 ") {
		t.Error("page 2 must not contain window 1")
	}
}

func TestSinglePageNoPaginationMetadata(t *testing.T) {
	f := New()
	res := resultOf(keptNodeWindow(10, 2, 1, "com.example.app"), false)
	out := f.FormatMultiWindow(res, defaultScreen)
	if contains(out, "page:") || contains(out, "cursor") {
		t.Error("single page must not carry pagination metadata")
	}
}

func TestDegradedPaginatesWithNoteOnEachPage(t *testing.T) {
	f := New()
	snap := snapshotOf(resultOf(keptNodeWindow(448, 2, 1, "com.example.app"), true), "snap1")
	for p := 1; p <= 3; p++ {
		first := splitLines(f.FormatMultiWindowPage(snap, p))[0]
		if first != degradationNote {
			t.Errorf("page %d first line: %q", p, first)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
func containsStr(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
