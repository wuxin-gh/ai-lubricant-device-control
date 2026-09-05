package commands

import (
	"context"
	"time"

	"device-control/ios/internal/errcode"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// after returns a channel that closes after ms, like Kotlin's delay(). Used to
// settle between scroll_to_node attempts (§8.5: 300ms).
func after(ms int) <-chan time.Time {
	return time.After(time.Duration(ms) * time.Millisecond)
}

// resolveNodeCenter re-reads the source and finds the node's bounds center.
// iOS WDA has no accessibility ACTION_CLICK equivalent, so node_id targeting
// always resolves to a coordinate tap at the bounds center (unlike Android,
// which prefers performAction). Returns stale_node when the id is gone.
func resolveNodeCenter(ctx context.Context, c wda.Client, nodeID string) (int, int, error) {
	res, err := c.Source(ctx)
	if err != nil {
		return 0, 0, err
	}
	n := findNodeByID(res, nodeID)
	if n == nil {
		return 0, 0, errcode.New(errcode.StaleNode, "node_id not found: %s", nodeID)
	}
	cx := (n.Bounds.Left + n.Bounds.Right) / 2
	cy := (n.Bounds.Top + n.Bounds.Bottom) / 2
	return cx, cy, nil
}

// nodeVisible reports whether a node_id is on-screen in the current source.
func nodeVisible(ctx context.Context, c wda.Client, nodeID string) (bool, error) {
	res, err := c.Source(ctx)
	if err != nil {
		return false, err
	}
	n := findNodeByID(res, nodeID)
	if n == nil {
		return false, errcode.New(errcode.StaleNode, "node_id not found: %s", nodeID)
	}
	return n.Visible, nil
}

// findNodeByID walks every window's tree for a node with the given id.
func findNodeByID(res *screen.MultiWindowResult, id string) *screen.Node {
	for _, w := range res.Windows {
		if n := walkTreeForID(w.Tree, id); n != nil {
			return n
		}
	}
	return nil
}

func walkTreeForID(n *screen.Node, id string) *screen.Node {
	if n.ID == id {
		return n
	}
	for _, c := range n.Children {
		if r := walkTreeForID(c, id); r != nil {
			return r
		}
	}
	return nil
}
