package commands

import (
	"device-control/ios/internal/shot"
)

// encodeScreenshot wraps shot.Encode so the get_screen_state handler can pass
// the §8.3 defaults (quality 80, longest side 700) without importing shot
// at the call site.
func encodeScreenshot(png []byte, quality, maxLongest int) (shot.Screenshot, error) {
	return shot.Encode(png, quality, maxLongest)
}
