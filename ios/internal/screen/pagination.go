package screen

import (
	"strings"
)

// Snapshot is a retained multi-window read, paginated on demand (§8.7). Mirrors
// android ScreenStateSnapshot.
type Snapshot struct {
	ID             string
	Result         *MultiWindowResult
	ScreenInfo     ScreenInfo
	TotalKeptNodes int
	TotalPages     int
}

// PageCount returns the number of pages for totalKept kept nodes (min 1).
func PageCount(totalKept int) int {
	if totalKept <= 0 {
		return 1
	}
	return (totalKept + PageSize - 1) / PageSize
}

// FormatMultiWindowPage renders a single (1-based) page of snapshot. The
// header/notes/window-header/TSV-row/hierarchy formats are identical to
// FormatMultiWindow; the only additions are the page: line and the trailing
// cursor note. Mirrors formatMultiWindowPage.
//
// A window section is emitted only when it contributes at least one row to this
// page. Each window's hierarchy block lists the page's rows PLUS their kept
// ancestors (kept-ancestor closure), preserving the kept-depth indentation.
func (f *CompactTreeFormatter) FormatMultiWindowPage(snapshot *Snapshot, page int) string {
	result := snapshot.Result
	screen := snapshot.ScreenInfo
	startIndex := (page - 1) * PageSize
	endIndexExclusive := page * PageSize
	if endIndexExclusive > snapshot.TotalKeptNodes {
		endIndexExclusive = snapshot.TotalKeptNodes
	}
	perWindowKept := make([][]*Node, len(result.Windows))
	for i, w := range result.Windows {
		perWindowKept[i] = collectKeptNodes(w.Tree)
	}

	var sb strings.Builder
	if result.Degraded {
		sb.WriteString(degradationNote + "\n")
	}
	sb.WriteString(noteLine + "\n")
	sb.WriteString(noteLineCustomElements + "\n")
	sb.WriteString(noteLineFlagsLegend + "\n")
	sb.WriteString(noteLineOffscreenHint + "\n")
	sb.WriteString("screen:" + itoa(screen.Width) + "x" + itoa(screen.Height) +
		" density:" + itoa(screen.DensityDpi) + " orientation:" + screen.Orientation + "\n")
	sb.WriteString("page:" + itoa(page) + "/" + itoa(snapshot.TotalPages) +
		" snapshot:" + snapshot.ID + " nodes:" + itoa(startIndex+1) + "-" +
		itoa(endIndexExclusive) + "/" + itoa(snapshot.TotalKeptNodes) + "\n")

	windowGlobalStart := 0
	for wIdx, w := range result.Windows {
		kept := perWindowKept[wIdx]
		windowEnd := windowGlobalStart + len(kept)
		overlapStart := startIndex
		if windowGlobalStart > overlapStart {
			overlapStart = windowGlobalStart
		}
		overlapEnd := endIndexExclusive
		if windowEnd < overlapEnd {
			overlapEnd = windowEnd
		}
		if overlapStart < overlapEnd {
			pageNodes := kept[overlapStart-windowGlobalStart : overlapEnd-windowGlobalStart]
			appendWindowPageSection(&sb, w, pageNodes)
		}
		windowGlobalStart = windowEnd
	}

	sb.WriteString(buildPaginationNote(snapshot, page) + "\n")
	return strings.TrimRight(sb.String(), "\n")
}

func buildPaginationNote(snapshot *Snapshot, page int) string {
	if page < snapshot.TotalPages {
		return "note:more nodes available — call get_screen_state with cursor \"" +
			snapshot.ID + "." + itoa(page+1) +
			"\" to continue. You do NOT need to fetch every page; " +
			"stop once you have found what you need. This cursor is tied to this screen " +
			"snapshot; if the screen changed, call without a cursor for a fresh one."
	}
	return "note:end of snapshot (page " + itoa(page) + "/" + itoa(snapshot.TotalPages) +
		"). You do NOT need to have fetched every page; stop once you have found what you need."
}

// collectKeptNodes returns the pre-order list of kept nodes (same order/filter
// as walk's TSV emission).
func collectKeptNodes(root *Node) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(n *Node) {
		if (&CompactTreeFormatter{}).ShouldKeepNode(n) {
			out = append(out, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return out
}

// appendWindowPageSection emits a window section for pageNodes: window header,
// TSV rows, and a hierarchy block holding those rows PLUS their kept-ancestors
// (kept-ancestor closure), at the same kept-depth indentation as
// FormatMultiWindow.
func appendWindowPageSection(sb *strings.Builder, w *WindowData, pageNodes []*Node) {
	f := &CompactTreeFormatter{}
	pageNodeIDs := make(map[string]struct{}, len(pageNodes))
	for _, n := range pageNodes {
		pageNodeIDs[n.ID] = struct{}{}
	}
	closure := make(map[string]struct{})
	var covers func(*Node) bool
	covers = func(n *Node) bool {
		subtreeCovers := false
		if _, ok := pageNodeIDs[n.ID]; ok {
			subtreeCovers = true
		}
		for _, c := range n.Children {
			if covers(c) {
				subtreeCovers = true
			}
		}
		if subtreeCovers && f.ShouldKeepNode(n) {
			closure[n.ID] = struct{}{}
		}
		return subtreeCovers
	}
	covers(w.Tree)

	sb.WriteString(f.BuildWindowHeader(w) + "\n")
	sb.WriteString(header + "\n")
	for _, n := range pageNodes {
		f.AppendElementRow(sb, n)
	}
	sb.WriteString(hierarchyHeader + "\n")

	var appendHierarchy func(*Node, int)
	appendHierarchy = func(n *Node, depth int) {
		kept := f.ShouldKeepNode(n)
		if kept {
			if _, ok := closure[n.ID]; ok {
				for i := 0; i < depth; i++ {
					sb.WriteString(hierarchyIndent)
				}
				sb.WriteString(n.ID)
				sb.WriteByte('\n')
			}
		}
		childDepth := depth
		if kept {
			childDepth = depth + 1
		}
		for _, c := range n.Children {
			appendHierarchy(c, childDepth)
		}
	}
	appendHierarchy(w.Tree, 0)
}
