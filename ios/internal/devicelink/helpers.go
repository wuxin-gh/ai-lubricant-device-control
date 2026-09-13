package devicelink

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// parseWdaSource delegates to the wda source parser.
func parseWdaSource(raw []byte, scale int) (*screen.MultiWindowResult, error) {
	return wda.ParseSource(raw, scale)
}

// parseDeviceKitJSONSource adapts a JSON device.dump.ui payload to the
// screen.Node tree. The upstream schema is not documented, so this accepts the
// common field spellings (root/tree/windows/elements/children for the root;
// type/class for the kind; text/value/label/name; identifier/id; x/y/width/
// height or frame/bounds; enabled/visible/...) and returns a structured error
// for anything unrecognizable. A real-device fixture recorded during
// acceptance tightens the schema (drop the alternates that never appear).
func parseDeviceKitJSONSource(raw []byte, scale int) (*screen.MultiWindowResult, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("devicekit dump: empty payload")
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return nil, fmt.Errorf("devicekit dump: neither XML nor JSON (body tail: %s)", tailBytes(trimmed, 200))
	}

	// Find the element root: direct array, or one of the common wrapper keys.
	var root any
	switch t := v.(type) {
	case []any:
		root = t
	case map[string]any:
		for _, key := range []string{"root", "tree", "windows", "elements", "children", "nodes"} {
			if child, ok := t[key]; ok && child != nil {
				root = child
				break
			}
		}
		if root == nil {
			return nil, fmt.Errorf("devicekit dump: json object without a known root key (keys: %v, body tail: %s)",
				jsonKeys(t), tailBytes(trimmed, 200))
		}
	default:
		return nil, fmt.Errorf("devicekit dump: unexpected json shape %T", v)
	}

	nodes := adaptDKNodes(root, scale)
	if len(nodes) == 0 {
		// An empty tree is a valid (if degraded) result: some screens expose
		// nothing. Mirror ParseSource's degraded shape.
		return &screen.MultiWindowResult{Windows: nil, Degraded: true}, nil
	}
	return &screen.MultiWindowResult{
		Windows:  []*screen.WindowData{{WindowID: 0, WindowType: "APPLICATION", Tree: nodes[0], Focused: true}},
		Degraded: false,
	}, nil
}

// jsonKeys lists an object's keys for diagnostics (sorted, bounded).
func jsonKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// sort not critical for an error path; bound the count.
	if len(keys) > 12 {
		keys = keys[:12]
	}
	return keys
}

// adaptDKNodes converts a JSON element (object or array) into screen nodes.
func adaptDKNodes(v any, scale int) []*screen.Node {
	switch t := v.(type) {
	case []any:
		out := make([]*screen.Node, 0, len(t))
		for _, item := range t {
			if node := adaptDKOne(item, scale); node != nil {
				out = append(out, node)
			}
		}
		return out
	case map[string]any:
		if node := adaptDKOne(t, scale); node != nil {
			return []*screen.Node{node}
		}
	}
	return nil
}

// adaptDKOne converts one JSON element object into a screen.Node (children
// recurse). Returns nil for non-object entries so malformed siblings are
// skipped, not fatal.
func adaptDKOne(v any, scale int) *screen.Node {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	node := &screen.Node{}
	node.ClassName = dkStr(obj, "type", "class", "typeName", "elementType", "XCUIElementType")
	node.Text = dkStr(obj, "text", "value", "label", "name")
	node.ContentDescription = dkStr(obj, "label", "name", "description")
	node.ResourceID = dkStr(obj, "identifier", "accessibilityIdentifier", "id", "resourceId")

	x, y, w, h := dkRect(obj)
	if w > 0 && h > 0 {
		// DeviceKit geometry is POINTS; the wire wants pixels (same scaling the
		// WDA parser applies to its rects).
		node.Bounds = screen.Bounds{
			Left: x * scale, Top: y * scale,
			Right: (x + w) * scale, Bottom: (y + h) * scale,
		}
	}

	node.Enabled = dkBool(obj, "enabled", "isEnabled")
	node.Visible = dkBool(obj, "visible", "isVisible")
	node.Clickable = dkBool(obj, "clickable", "isClickable", "accessible")
	node.Focusable = dkBool(obj, "focusable", "isFocusable", "focused", "isFocused")
	node.Editable = dkBool(obj, "editable", "isEditable")
	node.Scrollable = dkBool(obj, "scrollable", "isScrollable")

	for _, key := range []string{"children", "elements", "nodes", "subviews", "childElements"} {
		if kids, ok := obj[key].([]any); ok && len(kids) > 0 {
			for _, kid := range kids {
				if child := adaptDKOne(kid, scale); child != nil {
					node.Children = append(node.Children, child)
				}
			}
			break
		}
	}
	return node
}

// dkStr returns the first non-empty string among the given keys.
func dkStr(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			// Some dumps spell the type as {"type": {"name": "Button"}}.
			if m, ok := v.(map[string]any); ok {
				if s, ok := m["name"].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// dkBool returns the first true-ish bool among the given keys.
func dkBool(obj map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	return false
}

// dkRect pulls element geometry from either flat x/y/width/height or a
// frame/bounds object (possibly "x,y,w,h" string form).
func dkRect(obj map[string]any) (x, y, w, h int) {
	for _, k := range []string{"frame", "bounds", "rect"} {
		if v, ok := obj[k]; ok {
			switch t := v.(type) {
			case map[string]any:
				return dkFlatRect(t)
			case []any:
				if len(t) >= 4 {
					return dkNum(t[0]), dkNum(t[1]), dkNum(t[2]), dkNum(t[3])
				}
			case string:
				parts := strings.Split(strings.Trim(t, "[]() "), ",")
				if len(parts) >= 4 {
					return dkNumStr(parts[0]), dkNumStr(parts[1]), dkNumStr(parts[2]), dkNumStr(parts[3])
				}
			}
		}
	}
	return dkFlatRect(obj)
}

func dkFlatRect(obj map[string]any) (x, y, w, h int) {
	x = dkNum(obj["x"], obj["X"])
	y = dkNum(obj["y"], obj["Y"])
	w = dkNum(obj["width"], obj["w"])
	h = dkNum(obj["height"], obj["h"])
	return
}

func dkNum(vals ...any) int {
	for _, v := range vals {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case string:
			if n := dkNumStr(t); n != 0 {
				return n
			}
		}
	}
	return 0
}

func dkNumStr(s string) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil {
		return n
	}
	return 0
}

// decodeBase64PNG decodes a WDA screenshot reply (a base64 string in
// {"value":"<b64>"}). The HTTP layer already unwrapped value; here it is a
// raw base64 string.
func decodeBase64PNG(b64 string) ([]byte, error) {
	if b64 == "" {
		return nil, errors.New("empty screenshot")
	}
	return base64.StdEncoding.DecodeString(b64)
}

// connectAndLaunchWDA was the pre-refactor seam to the go-ios handshake. Start
// now calls startSession (indirected for tests) directly; this stub remains
// only to avoid churn in external callers and is unused in production.
//
// Deprecated: use Start.
func (l *Link) connectAndLaunchWDA(ctx context.Context) error {
	_ = ctx
	return errors.New("connectAndLaunchWDA is deprecated; use Link.Start")
}
