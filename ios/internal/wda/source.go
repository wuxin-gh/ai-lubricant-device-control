package wda

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"device-control/ios/internal/screen"
)

// ParseSource turns WDA's /source XML into a MultiWindowResult. WDA emits one
// tree (the active app); the result is a single APPLICATION window. The XML
// element tag is the XCUIElementType simple name (Button, Other, Window, App,
// Cell, TextField, ...); attributes carry name/label/value/enabled/visible
// and a rect (x,y,width,height in points).
//
// The fields map onto screen.Node:
//   - ClassName = the element tag (already the simple type name)
//   - Text = value (or name when label is empty — see below)
//   - ContentDescription = label
//   - ResourceID = the accessibility identifier, when present
//   - Bounds = rect * scale (WDA rect is points; the wire wants pixels)
//   - flags = best-effort from enabled/visible/type (see buildFlags)
//
// This is best-effort: iOS accessibility attributes are not 1:1 with Android's,
// and the spec §8.2 explicitly lets the device pick reasonable flag values.
func ParseSource(xmlData []byte, scale int) (*screen.MultiWindowResult, error) {
	if scale <= 0 {
		scale = 2 // safe default; the driver supplies the real device scale
	}
	top, err := decodeTree(xmlData)
	if err != nil {
		return nil, err
	}
	if top == nil {
		return &screen.MultiWindowResult{Windows: nil, Degraded: true}, nil
	}
	root := convertElement(*top, scale)
	return &screen.MultiWindowResult{
		Windows:  []*screen.WindowData{{WindowID: 0, WindowType: "APPLICATION", Tree: root, Focused: true}},
		Degraded: false,
	}, nil
}

// wdaElement is the XML shape of a WDA source node. attr order is not
// guaranteed; we read by attribute name in the start element.
type wdaElement struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	// Children are nested elements, captured in document order.
	Children []wdaElement `xml:",any"`
}

// decodeTree decodes the first XML element subtree of a WDA /source reply. It
// returns the raw wdaElement (nil for empty input); conversion to screen.Node
// happens in ParseSource, which threads the per-device scale through.
func decodeTree(data []byte) (*wdaElement, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var top *wdaElement
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			// Decode the first start element and its full subtree.
			var e wdaElement
			if err := dec.DecodeElement(&e, &se); err != nil {
				return nil, err
			}
			top = &e
			break
		}
	}
	return top, nil
}

// convertElement maps one WDA XML element onto screen.Node. scale is threaded
// explicitly: WDA rects are in points, the wire wants physical pixels, and each
// device has its own scale (2×/3×) — a package-level default would silently mix
// devices up (and made concurrent ParseSource calls clobber each other).
func convertElement(e wdaElement, scale int) *screen.Node {
	attrs := map[string]string{}
	for _, a := range e.Attrs {
		attrs[strings.ToLower(a.Name.Local)] = a.Value
	}
	n := &screen.Node{
		ClassName:          e.XMLName.Local,
		Text:               attrs["value"],
		ContentDescription: attrs["label"],
		ResourceID:         attrs["identifier"], // WDA accessibility identifier
		Enabled:            attrs["enabled"] == "true" || attrs["enabled"] == "TRUE",
		Visible:            attrs["visible"] == "true" || attrs["visible"] == "TRUE",
		WebRole:            attrs["dom_type"],
		IsPassword:         attrs["secure"] == "true",
	}
	// WDA exposes no clickable flag; infer from type and enabled.
	inferFlags(n)
	// Bounds: rect (x,y,w,h) in points → pixels.
	n.Bounds = rectToPixelBounds(attrs, scale)
	for _, c := range e.Children {
		n.Children = append(n.Children, convertElement(c, scale))
	}
	return n
}

func rectToPixelBounds(attrs map[string]string, s int) screen.Bounds {
	x := atoiSafe(attrs["x"])
	y := atoiSafe(attrs["y"])
	w := atoiSafe(attrs["width"])
	h := atoiSafe(attrs["height"])
	return screen.Bounds{
		Left:   x * s,
		Top:    y * s,
		Right:  (x + w) * s,
		Bottom: (y + h) * s,
	}
}

func atoiSafe(s string) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return 0
}

// inferFlags maps the XCUIElementType (the tag) and enabled/visible onto the
// Android-style flag set. This is necessarily approximate; the spec allows the
// device to pick reasonable flags (§8.2). The mapping:
//   - Button/Cell/MenuItem/Link/Tab/Icon are clickable when enabled
//   - TextField/TextView are editable + focusable when enabled
//   - ScrollView/Table/Collection are scrollable
//   - everything enabled is focusable (conservative; keeps it addressable)
func inferFlags(n *screen.Node) {
	cls := n.ClassName
	switch {
	case containsAny(cls, "Button", "Cell", "MenuItem", "Link", "Tab", "Icon", "Switch", "Slider"):
		n.Clickable = n.Enabled
	case containsAny(cls, "TextField", "TextView", "SearchField"):
		n.Editable = n.Enabled
		n.Focusable = n.Enabled
	case containsAny(cls, "ScrollView", "Table", "Collection", "Picker"):
		n.Scrollable = true
	}
	if n.Enabled {
		n.Focusable = true
	}
	n.LongClickable = n.Clickable
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
