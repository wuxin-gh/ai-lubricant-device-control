// Package protocol defines the device-control v0 wire protocol.
//
// This is a direct transcription of spec/protocol-v0.md. Any change here that
// is not also a spec change is a bug.
package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Version is the protocol_version this implementation speaks (spec §14).
const Version = 0

// Message types, device → server (spec §3.1).
const (
	TypeRegister     = "register"
	TypeHeartbeat    = "heartbeat"
	TypeCallResponse = "call-response"
	TypeEvent        = "event"
)

// Message types, server → device (spec §3.2).
const (
	TypeRegistered = "registered"
	TypeCall       = "call"
	TypeCallCancel = "call-cancel"
)

// Application-range WebSocket close codes (spec §13).
const (
	CloseFrameTooLarge      = 4002
	CloseAuthFailed         = 4003
	CloseVersionUnsupported = 4004
	CloseDuplicateRegister  = 4007
	CloseRegisterTimeout    = 4008
	CloseReplaced           = 4009
	CloseStale              = 4010
)

// Device-reported error codes (spec §12). The server only *consumes* these;
// it never invents one on the device's behalf.
const (
	ErrUnsupported      = "unsupported"
	ErrBadArgs          = "bad_args"
	ErrStaleNode        = "stale_node"
	ErrNotFound         = "not_found"
	ErrTimeout          = "timeout"
	ErrCancelled        = "cancelled"
	ErrDuplicateRequest = "duplicate_request"
	ErrOverloaded       = "overloaded"
	ErrPermissionDenied = "permission_denied"
	ErrNotReady         = "not_ready"
	ErrDeviceError      = "device_error"
)

// Protocol constants and defaults (spec §2, §4, §5, §7).
const (
	MaxFrameBytes        = 4 << 20 // §2
	RegisterTimeoutSec   = 10      // §4.1
	HeartbeatIntervalSec = 15      // §7
	HeartbeatTimeoutSec  = 60      // §7
	MaxInFlightPerDevice = 8       // §5
	DefaultCallTimeoutMS = 15000   // §5
	MaxCallTimeoutMS     = 60000   // §5
	ServerGraceMS        = 5000    // §5: server timeout = timeout_ms + 5s
)

// Auth is register.auth (spec §11). v0 defines only scheme "token".
type Auth struct {
	Scheme string `json:"scheme"`
	Token  string `json:"token"`
}

// SchemeToken is the only auth scheme defined in v0.
const SchemeToken = "token"

// Error is the structured error object (spec §12).
type Error struct {
	Code      string          `json:"code"`
	Message   string          `json:"message,omitempty"`
	Retryable bool            `json:"retryable,omitempty"`
	Details   json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Inbound is any device → server frame. Fields are the union of all device
// message types; only those relevant to Type are populated. Unknown fields in
// the JSON are ignored, per the forward-compat rule in spec §3.
type Inbound struct {
	Type string `json:"type"`

	// register
	ProtocolVersion *int            `json:"protocol_version"`
	DeviceID        string          `json:"device_id"`
	Auth            *Auth           `json:"auth"`
	Capabilities    []string        `json:"capabilities"`
	DeviceInfo      json.RawMessage `json:"device_info"`

	// heartbeat
	Seq int64 `json:"seq"`

	// call-response
	RequestID string          `json:"request_id"`
	OK        *bool           `json:"ok"`
	Data      json.RawMessage `json:"data"`
	Error     *Error          `json:"error"`

	// event
	Kind string `json:"kind"`
}

// Event kinds defined in v0 (spec §9).
const (
	EventControlRevoked      = "control-revoked"
	EventCapabilitiesChanged = "capabilities-changed"
)

// Registered is the server's ack of register (spec §3.2).
type Registered struct {
	Type                 string   `json:"type"`
	ProtocolVersion      int      `json:"protocol_version"`
	DeviceID             string   `json:"device_id"`
	ServerTime           string   `json:"server_time"`
	SessionID            string   `json:"session_id"`
	HeartbeatIntervalS   int      `json:"heartbeat_interval_s"`
	HeartbeatTimeoutS    int      `json:"heartbeat_timeout_s"`
	AcceptedCapabilities []string `json:"accepted_capabilities"`
}

// Call asks the device to execute one command (spec §3.2).
type Call struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Cmd       string          `json:"cmd"`
	Args      json.RawMessage `json:"args,omitempty"`
	TimeoutMS int             `json:"timeout_ms,omitempty"`
}

// CallCancel is the advisory cancellation frame (spec §5).
type CallCancel struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// NewID returns prefix + 128 bits of base64url randomness, for device_id /
// request_id / session_id (spec §3.3 recommends opaque ids ≤ 64 bytes).
func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// ClampCallTimeout applies the §5 default and cap.
func ClampCallTimeout(ms int) int {
	if ms <= 0 {
		return DefaultCallTimeoutMS
	}
	if ms > MaxCallTimeoutMS {
		return MaxCallTimeoutMS
	}
	return ms
}
