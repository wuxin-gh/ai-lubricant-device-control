// Package errcode is the closed set of wire error.code values (spec §12) and
// the mapping from Go errors thrown by WDA/commands onto that set.
//
// It mirrors android/app/.../ErrorCode.kt. iOS has no CoreException type
// hierarchy (the Android core's typed failures), so the mapping keys off
// concrete Go error types and sentinel values defined in this package. The
// mapping is deliberately conservative: anything not clearly bad_args/stale/
// permission/timeout is device_error, never internal — internal is reserved for
// genuine invariant violations the server should surface to an operator, and
// over-reporting it would drown real signals.
package errcode

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The closed set (spec §12).
const (
	Unsupported      = "unsupported"
	BadArgs          = "bad_args"
	StaleNode        = "stale_node"
	NotFound         = "not_found"
	Timeout          = "timeout"
	Cancelled        = "cancelled"
	DuplicateRequest = "duplicate_request"
	Overloaded       = "overloaded"
	PermissionDenied = "permission_denied"
	NotReady         = "not_ready"
	DeviceError      = "device_error"
	Internal         = "internal"
)

// Code is a typed wire error code carrying its own message, so a handler can
// throw a typed failure the way the Android handlers throw CommandException.
//
//	errcode.New(errcode.StaleNode, "node_id not found: %s", id)
type Code struct {
	Code    string
	Message string
	// Retryable is optional; when false (zero value) the wire field is omitted.
	Retryable    bool
	HasRetryable bool
}

// New returns a Code with retryable left unspecified (omitted on the wire).
func New(code, format string, args ...any) Code {
	return Code{Code: code, Message: fmt.Sprintf(format, args...)}
}

// NewRetryable returns a Code with an explicit retryable flag.
func NewRetryable(code string, retryable bool, format string, args ...any) Code {
	return Code{Code: code, Message: fmt.Sprintf(format, args...), Retryable: retryable, HasRetryable: true}
}

// Error implements error.
func (c Code) Error() string {
	if c.Message == "" {
		return c.Code
	}
	return c.Code + ": " + c.Message
}

// Is lets errors.Is(err, someCode) match on the code value, so callers can
// branch on a thrown Code without string matching.
func (c Code) Is(target error) bool {
	tc, ok := target.(Code)
	if !ok {
		return false
	}
	return c.Code == tc.Code
}

// From maps a thrown error onto a wire code. Order matters: the most specific
// first, same as the Android ErrorCode.from.
//
//   - A thrown Code carries its own explicit code (handler knew the answer).
//   - The sentinel errors defined in this package (ErrStaleNode etc.) map 1:1.
//   - Sentinel NotFound → not_found, BadArgs → bad_args, etc.
//   - context.DeadlineExceeded → timeout (the dispatcher's per-call budget).
//   - context.Canceled → cancelled (call-cancel / connection drop).
//   - An error whose message contains an availability hint → not_ready.
//   - Everything else → device_error.
func From(err error) string {
	if err == nil {
		return DeviceError
	}
	var c Code
	if errors.As(err, &c) {
		return c.Code
	}
	switch {
	case errors.Is(err, ErrStaleNode):
		return StaleNode
	case errors.Is(err, ErrNotFound):
		return NotFound
	case errors.Is(err, ErrBadArgs):
		return BadArgs
	case errors.Is(err, ErrPermissionDenied):
		return PermissionDenied
	case errors.Is(err, ErrTimeout):
		return Timeout
	case errors.Is(err, ErrNotReady):
		return NotReady
	case errors.Is(err, ErrCancelled):
		return Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout
	case errors.Is(err, context.Canceled):
		return Cancelled
	}
	if msg := err.Error(); containsAvailabilityHint(msg) {
		return NotReady
	}
	return DeviceError
}

// containsAvailabilityHint mirrors ErrorCode.kt's IllegalStateException sniff:
// a message mentioning "not available"/"not enabled"/"not ready" signals an
// uninitialized service/permission rather than a hard device failure.
func containsAvailabilityHint(msg string) bool {
	for _, hint := range []string{"not available", "not enabled", "not ready"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// Sentinel errors for handlers that want to throw a typed failure without
// formatting a message. errors.Is(thrown, ErrStaleNode) is true for these.
var (
	ErrStaleNode        = Code{Code: StaleNode, Message: "stale node"}
	ErrNotFound         = Code{Code: NotFound, Message: "not found"}
	ErrBadArgs          = Code{Code: BadArgs, Message: "bad args"}
	ErrPermissionDenied = Code{Code: PermissionDenied, Message: "permission denied"}
	ErrTimeout          = Code{Code: Timeout, Message: "timeout"}
	ErrNotReady         = Code{Code: NotReady, Message: "not ready"}
	ErrCancelled        = Code{Code: Cancelled, Message: "cancelled"}
)
