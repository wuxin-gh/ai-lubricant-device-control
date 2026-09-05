// Package pairclient redeems a pairing code for long-lived device credentials.
// Mirrors android/app/.../net/PairingClient.kt and the server's
// internal/store.NormalizeCode (store.go): uppercase, strip everything outside
// [A-Z0-9].
//
// The server returns 403 (deliberately indistinguishable) for not-found /
// expired / already-used / replayed: codes are single-use, 10 min TTL, wiped on
// restart. 404/405 means the path is wrong and we try the /mcp/device-control
// fallback so a user who typed only the host still pairs.
package pairclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"device-control/ios/internal/wire"
)

// Result is the persisted outcome of a successful pairing.
type Result struct {
	ServerURL string
	DeviceID  string
	Token     string
}

// InvalidCodeError means the server returned 403 — the code is invalid, expired,
// or already used. Single-use: do not retry the same code.
type InvalidCodeError struct{ msg string }

func (e *InvalidCodeError) Error() string { return e.msg }

// ProtocolMismatchError means the server speaks a protocol version we don't.
type ProtocolMismatchError struct{ Expected, Actual int }

func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf("protocol version mismatch: expected %d, got %d", e.Expected, e.Actual)
}

// Client pairs against a server.
type Client struct {
	HTTP *http.Client
}

// New returns a client with a 30s timeout.
func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

// Pair POSTs the code to {base}/pair, trying the user's base as-is first and
// the /mcp/device-control prefix as a fallback (so a bare host still works).
// The code is normalized locally to match the server's NormalizeCode.
func (c *Client) Pair(base, rawCode string) (Result, error) {
	code := NormalizeCode(rawCode)
	if code == "" {
		return Result{}, errors.New("empty pairing code")
	}

	baseClean := strings.TrimRight(base, "/")
	candidates := []string{
		baseClean + "/pair",
		baseClean + "/mcp/device-control/pair",
	}

	var lastErr error
	for idx, url := range candidates {
		res, err := c.post(url, code)
		if err != nil {
			// 403 is terminal: the code is single-use, and continuing would
			// burn a consumed code against the next path. Surface immediately.
			var invalid *InvalidCodeError
			if errors.As(err, &invalid) {
				return Result{}, err
			}
			var mismatch *ProtocolMismatchError
			if errors.As(err, &mismatch) {
				return Result{}, err
			}
			// Connection / 404 / 405: try the next candidate.
			lastErr = err
			continue
		}
		// Probe-effective base: idx 0 succeeded with the user's base, idx 1
		// succeeded only after adding the prefix.
		effective := baseClean
		if idx == 1 {
			effective = baseClean + "/mcp/device-control"
		}
		return Result{
			ServerURL: effective,
			DeviceID:  res.DeviceID,
			Token:     res.Token,
		}, nil
	}
	return Result{}, fmt.Errorf("未能在该地址找到设备控制服务端: %w", lastErr)
}

func (c *Client) post(url, code string) (*wire.PairReply, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, &InvalidCodeError{"pairing code invalid, expired, or already used"}
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, fmt.Errorf("HTTP %d at %s", resp.StatusCode, url)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("pairing failed: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var reply wire.PairReply
	// json.Unmarshal ignores unknown fields by default, matching the Android
	// client's ignoreUnknownKeys=true tolerance for forward-compat additions.
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("parse pair reply: %w", err)
	}
	if reply.ProtocolVersion != wire.Version {
		return nil, &ProtocolMismatchError{Expected: wire.Version, Actual: reply.ProtocolVersion}
	}
	return &reply, nil
}

// NormalizeCode makes codes forgiving to type: case and separators are ignored.
// Mirrors server store.NormalizeCode — uppercase, keep only [A-Z0-9]. The
// alphabet excludes I/O/0/1, but we don't enforce that here: the server rejects
// codes containing those as 403, and we surface the same unified message.
func NormalizeCode(raw string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
