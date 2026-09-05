package shot

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestEncodeResizesLongestSide(t *testing.T) {
	// 1400x700 → longest side 1400 > 700; resized to 700x350.
	shot, err := Encode(pngBytes(1400, 700), 80, 700)
	if err != nil {
		t.Fatal(err)
	}
	if shot.W != 700 || shot.H != 350 {
		t.Errorf("dims = %dx%d, want 700x350", shot.W, shot.H)
	}
	if shot.Format != "jpeg" {
		t.Errorf("format = %q", shot.Format)
	}
	if !strings.HasPrefix(shot.B64, "/9j/") {
		t.Error("b64 does not look like a JPEG (missing /9j/ magic)")
	}
}

func TestEncodeKeepsSmallImage(t *testing.T) {
	shot, err := Encode(pngBytes(100, 50), 80, 700)
	if err != nil {
		t.Fatal(err)
	}
	// 100x50 is under the cap; returned unchanged.
	if shot.W != 100 || shot.H != 50 {
		t.Errorf("dims = %dx%d, want 100x50", shot.W, shot.H)
	}
}

func TestEncodeNonPNG(t *testing.T) {
	_, err := Encode([]byte("not a png"), 80, 700)
	if err == nil {
		t.Fatal("expected error on non-PNG input")
	}
}

func TestEncodeUnder1_5MiB(t *testing.T) {
	// A large image; after resize longest side is 700 → 700x700, well under cap.
	shot, err := Encode(pngBytes(2000, 2000), 80, 700)
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.B64) > MaxEncodedBytes {
		t.Errorf("encoded %d bytes > cap %d", len(shot.B64), MaxEncodedBytes)
	}
}
