// Package dispatcher turns one inbound `call` into one `call-response`, with
// the §5/§6 protocol obligations handled in one place so the 16 command
// handlers never see them. Mirrors android/app/.../command/CommandDispatcher.kt.
//
//   - request_id dedup (per connection): the same request_id twice on one
//     connection is a server bug; answer duplicate_request rather than execute
//     a mutating command twice (a replayed tap taps twice).
//   - in-flight cap (≤ MaxInFlight): §5 lets the server pipeline up to 8 calls.
//     Over the cap, answer overloaded — the server retries with backoff.
//   - timeout budget: timeout_ms (default 15s, max 60s, §5) wraps the handler in
//     context.WithTimeout. A call-cancel racing a timeout still gets a response.
//   - error mapping: any thrown error becomes a well-formed ok=false frame.
//
// One instance per connection: seenIDs and inFlight are per-connection state,
// recreated on every reconnect via ResetForNewConnection.
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"device-control/ios/internal/errcode"
	"device-control/ios/internal/wire"
)

// Handler executes one command and returns its `data` (ok=true), or throws an
// error that errcode.From maps to a wire code. It is the Go analogue of the
// Android `suspend (JsonObject) -> JsonElement`.
type Handler func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

// MaxInFlight is §5's per-device cap (also the server's self-limit).
const MaxInFlight = 8

const (
	minTimeoutMS = 1_000
	maxTimeoutMS = 60_000
)

// Dispatcher holds the command table and per-connection protocol state.
type Dispatcher struct {
	handlers map[string]Handler

	mu       sync.Mutex
	seenIDs  map[string]struct{}
	inFlight int
}

// New returns a dispatcher for the given command table. The map is the
// authoritative cmd→handler routing; unknown cmds reply unsupported.
func New(handlers map[string]Handler) *Dispatcher {
	return &Dispatcher{
		handlers: handlers,
		seenIDs:  make(map[string]struct{}),
	}
}

// ResetForNewConnection clears per-connection state. Called by the WS client
// after each reconnect (§7 drops in-flight state on both sides).
func (d *Dispatcher) ResetForNewConnection() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seenIDs = make(map[string]struct{})
	d.inFlight = 0
}

// Dispatch executes one call. It never returns a zero-value response: every
// path produces a well-formed CallResponse, so the WS client can always send
// something back — a dropped response blocks the server's caller until its own
// timeout.
func (d *Dispatcher) Dispatch(ctx context.Context, call wire.Call) wire.CallResponse {
	d.mu.Lock()
	if _, dup := d.seenIDs[call.RequestID]; dup {
		d.mu.Unlock()
		return errorResponse(call.RequestID, errcode.DuplicateRequest,
			"request_id %s already answered on this connection", call.RequestID)
	}
	if d.inFlight >= MaxInFlight {
		d.mu.Unlock()
		return errorResponse(call.RequestID, errcode.Overloaded,
			"device is executing %d calls; retry later", MaxInFlight)
	}
	d.seenIDs[call.RequestID] = struct{}{}
	d.inFlight++
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.inFlight--
		d.mu.Unlock()
		// request_id STAYS in seenIDs for the rest of the connection (§6: a
		// replayed request_id is duplicate_request, not a re-execution).
	}()

	handler, ok := d.handlers[call.Cmd]
	if !ok {
		// The server gates against capabilities at dispatch time, so an unknown
		// cmd here means capability drift or a server bug.
		return errorResponse(call.RequestID, errcode.Unsupported, "unknown command: %s", call.Cmd)
	}

	budget := wire.ClampCallTimeout(call.TimeoutMS)
	if budget < minTimeoutMS {
		budget = minTimeoutMS
	}
	if budget > maxTimeoutMS {
		budget = maxTimeoutMS
	}

	cctx, cancel := context.WithTimeout(ctx, durationFromMS(budget))
	defer cancel()

	data, err := handler(cctx, call.Args)
	if err != nil {
		// context.DeadlineExceeded → timeout; context.Canceled → cancelled;
		// a typed errcode.Code → its own code; everything else → device_error.
		return mapError(call.RequestID, err, cctx)
	}
	if data == nil {
		data = json.RawMessage(`{}`) // data is required when ok=true (§3.1)
	}
	return wire.CallResponse{
		Type:      wire.TypeCallResponse,
		RequestID: call.RequestID,
		OK:        true,
		Data:      data,
	}
}

func mapError(requestID string, err error, cctx context.Context) wire.CallResponse {
	code := errcode.From(err)
	msg := err.Error()
	// For the catch-all device_error, don't leak exception internals; give the
	// server something actionable without stack details.
	if code == errcode.DeviceError {
		msg = "command failed on device"
	}
	_ = cctx
	return wire.CallResponse{
		Type:      wire.TypeCallResponse,
		RequestID: requestID,
		OK:        false,
		Error: &wire.Error{
			Code:      code,
			Message:   msg,
			Retryable: isRetryable(code),
		},
	}
}

// isRetryable returns the §12 retryable flag for a code: a transient failure
// where an identical retry might succeed. The server/§12: timeout and
// overloaded are retryable; everything else is terminal.
func isRetryable(code string) bool {
	return code == errcode.Timeout || code == errcode.Overloaded || code == errcode.NotReady
}

func errorResponse(requestID, code string, format string, args ...any) wire.CallResponse {
	return wire.CallResponse{
		Type:      wire.TypeCallResponse,
		RequestID: requestID,
		OK:        false,
		Error: &wire.Error{
			Code:      code,
			Message:   fmt.Sprintf(format, args...),
			Retryable: isRetryable(code),
		},
	}
}

func durationFromMS(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}
