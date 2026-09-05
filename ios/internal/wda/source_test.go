package wda

import (
	"testing"

	"device-control/ios/internal/screen"
)

// Sample WDA /source XML: a Window with one Other container holding a Button
// and a TextField. Coordinates are points (scale=2 below to assert px mapping).
const sampleSource = `<root><App><Window>
<Other name="" label="" value="" enabled="true" visible="true" x="0" y="0" width="187" height="406">
<Button name="Save" label="Save" value="" identifier="save_btn" enabled="true" visible="true" x="50" y="100" width="40" height="22"/>
<TextField name="" label="" value="query" enabled="true" visible="true" x="50" y="150" width="100" height="20"/>
</Other>
</Window></App></root>`

func TestParseSourceNodes(t *testing.T) {
	res, err := ParseSource([]byte(sampleSource), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Windows) != 1 {
		t.Fatalf("want 1 window, got %d", len(res.Windows))
	}
	if res.Windows[0].WindowType != "APPLICATION" {
		t.Errorf("window type = %q", res.Windows[0].WindowType)
	}
	root := res.Windows[0].Tree
	// root is the <root> element; its child App → Window → Other (container)
	other := findNode(res, "Other")
	if other == nil {
		t.Fatalf("Other container not found in:\n%s", dumpNode(root))
	}
	btn := findByClass(other, "Button")
	if btn == nil {
		t.Fatal("Button not found under Other")
	}
	if btn.ContentDescription != "Save" {
		t.Errorf("button label = %q", btn.ContentDescription)
	}
	if btn.ResourceID != "save_btn" {
		t.Errorf("button identifier = %q", btn.ResourceID)
	}
	// bounds: x=50,y=100,w=40,h=22 in points, scale=2 → left=100,top=200,right=180,bottom=244
	want := screen.Bounds{Left: 100, Top: 200, Right: 180, Bottom: 244}
	if btn.Bounds != want {
		t.Errorf("bounds = %+v, want %+v", btn.Bounds, want)
	}
	if !btn.Clickable || !btn.Enabled {
		t.Errorf("button should be clickable+enabled: %+v", btn)
	}
}

func TestParseSourceTextFieldEditable(t *testing.T) {
	res, _ := ParseSource([]byte(sampleSource), 1)
	tf := findNode(res, "TextField")
	if tf == nil {
		t.Fatal("TextField not found")
	}
	if !tf.Editable {
		t.Error("TextField should be editable")
	}
	if tf.Text != "query" {
		t.Errorf("text = %q", tf.Text)
	}
}

func TestParseSourceEmpty(t *testing.T) {
	res, err := ParseSource([]byte(""), 2)
	if err != nil {
		t.Fatalf("empty source should not error: %v", err)
	}
	if res == nil || len(res.Windows) != 0 {
		t.Errorf("expected no windows, got %+v", res)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func findNode(res *screen.MultiWindowResult, class string) *screen.Node {
	for _, w := range res.Windows {
		if n := findByClass(w.Tree, class); n != nil {
			return n
		}
	}
	return nil
}

func findByClass(n *screen.Node, class string) *screen.Node {
	if n.ClassName == class {
		return n
	}
	for _, c := range n.Children {
		if r := findByClass(c, class); r != nil {
			return r
		}
	}
	return nil
}

func dumpNode(n *screen.Node) string {
	if n == nil {
		return "<nil>"
	}
	s := n.ClassName + " "
	for _, c := range n.Children {
		s += dumpNode(c) + " "
	}
	return s
}
