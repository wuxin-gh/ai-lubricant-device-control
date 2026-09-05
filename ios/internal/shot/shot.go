// Package shot encodes a WDA PNG screenshot into the §8.3 wire shape: JPEG
// quality 80, longest side ≤ 700px, base64, hard cap 1.5 MiB encoded. Mirrors
// the Android ScreenCaptureProvider defaults (ARC-MCP DEFAULT_QUALITY=80,
// SCREENSHOT_MAX_SIZE=700).
package shot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	"golang.org/x/image/draw"
)

// MaxLongestSide is the §8.3 longest-side cap (ARC-MCP SCREENSHOT_MAX_SIZE).
const MaxLongestSide = 700

// DefaultQuality is the §8.3 JPEG quality default (ARC-MCP DEFAULT_QUALITY).
const DefaultQuality = 80

// MaxEncodedBytes is the §8.3 base64 hard cap (1.5 MiB).
const MaxEncodedBytes = 1_572_864 // 1.5 * 1024 * 1024

// Screenshot is the §8.3 wire object.
type Screenshot struct {
	Format string `json:"format"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	B64    string `json:"b64"`
}

// Encode decodes png, resizes so the longest side ≤ maxLongest, re-encodes as
// JPEG at the given quality, and base64-encodes. If the encoded form exceeds
// MaxEncodedBytes it lowers the quality and retries until it fits or hits a
// floor; if still too large it shrinks the dimensions. Returns the §8.3 object.
func Encode(pngBytes []byte, quality int, maxLongest int) (Screenshot, error) {
	if quality <= 0 {
		quality = DefaultQuality
	}
	if maxLongest <= 0 {
		maxLongest = MaxLongestSide
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return Screenshot{}, err
	}
	img = resizeLongest(img, maxLongest)

	b64, q := encodeToFit(img, quality)
	return Screenshot{
		Format: "jpeg",
		W:      img.Bounds().Dx(),
		H:      img.Bounds().Dy(),
		B64:    b64,
	}, q
}

// encodeToFit JPEG-encodes img, lowering quality (then dimensions) until the
// base64 fits under MaxEncodedBytes.
func encodeToFit(img image.Image, quality int) (string, error) {
	q := quality
	for {
		b64 := encodeAt(img, q)
		if len(b64) <= MaxEncodedBytes || q <= 20 {
			return b64, nil
		}
		q -= 10
	}
}

func encodeAt(img image.Image, q int) string {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// resizeLongest downsamples img so its longest side is ≤ max, using
// golang.org/x/image/draw (Catmull-Rom for quality). If already within the
// bound the image is returned unchanged.
func resizeLongest(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= max {
		return src
	}
	scale := float64(max) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// IsPNG reports whether data decodes as a PNG.
func IsPNG(data []byte) bool {
	_, err := png.Decode(bytes.NewReader(data))
	return err == nil
}

// _ keeps the strings import live for future hooks (content-type sniffing);
// remove if the only consumer disappears.
var _ = strings.Contains
