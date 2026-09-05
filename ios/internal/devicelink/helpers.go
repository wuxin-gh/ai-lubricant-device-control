package devicelink

import (
	"context"
	"encoding/base64"
	"errors"

	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// parseWdaSource delegates to the wda source parser.
func parseWdaSource(raw []byte, scale int) (*screen.MultiWindowResult, error) {
	return wda.ParseSource(raw, scale)
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
