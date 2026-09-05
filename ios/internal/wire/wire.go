// Package wire is the device-control v0 wire protocol, transcribed faithfully
// from spec/protocol-v0.md. It mirrors the server's internal/protocol package
// and the Android app's Frames.kt; the three are kept in lockstep because each
// is a faithful transcription of the same spec, not because one imports another
// (Go forbids importing internal/ across modules, and Android is Kotlin).
//
// Any change here that is not also a spec change is a bug.
package wire

import "encoding/json"

// Version is protocol_version (spec §14).
const Version = 0

// Message types (spec §3). Unknown types MUST be ignored (§3 forward-compat hinge).
const (
	TypeRegister     = "register"
	TypeRegistered   = "registered"
	TypeHeartbeat    = "heartbeat"
	TypeCall         = "call"
	TypeCallCancel   = "call-cancel"
	TypeCallResponse = "call-response"
	TypeEvent        = "event"
)

// WebSocket close codes, application range (spec §13).
const (
	CloseFrameTooLarge      = 4002
	CloseAuthFailed         = 4003
	CloseVersionUnsupported = 4004
	CloseDuplicateRegister  = 4007
	CloseRegisterTimeout    = 4008
	CloseReplaced           = 4009
	CloseStale              = 4010
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

// Register is the device → server register frame (spec §3.1), the first frame
// after WS open.
type Register struct {
	Type            string          `json:"type"`
	ProtocolVersion int             `json:"protocol_version"`
	DeviceID        string          `json:"device_id"`
	Auth            Auth            `json:"auth"`
	Capabilities    []string        `json:"capabilities"`
	DeviceInfo      json.RawMessage `json:"device_info,omitempty"`
}

// Registered is the server's ack (spec §3.2).
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

// Heartbeat is the keepalive frame (spec §10); not acked.
type Heartbeat struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
	Seq      int64  `json:"seq"`
}

// Call is a server → device command request (spec §3.2).
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

// CallResponse is the device → server reply (spec §3.1). Exactly one per Call.
type CallResponse struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Event is an unsolicited device → server push (spec §9). v0 defines two kinds.
type Event struct {
	Type string          `json:"type"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Event kinds (spec §9).
const (
	EventControlRevoked      = "control-revoked"
	EventCapabilitiesChanged = "capabilities-changed"
)

// Protocol constants and defaults (spec §2, §4, §5, §7).
const (
	MaxFrameBytes        = 4 << 20 // §2
	RegisterTimeoutSec   = 10      // §4.1
	HeartbeatIntervalSec = 15      // §7 default
	HeartbeatTimeoutSec  = 60      // §7
	MaxInFlightPerDevice = 8       // §5
	DefaultCallTimeoutMS = 15000   // §5
	MaxCallTimeoutMS     = 60000   // §5
)

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

// PairReply is the server's response to POST /pair (spec §11).
type PairReply struct {
	DeviceID        string `json:"device_id"`
	Token           string `json:"token"`
	ProtocolVersion int    `json:"protocol_version"`
}
