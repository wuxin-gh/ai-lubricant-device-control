package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"device-control/ios/internal/errcode"
	"device-control/ios/internal/wire"
)

// Protocol-level obligations: request_id dedup, the 8-call in-flight cap,
// timeout enforcement, and the exception→error mapping. Pure logic — no WDA,
// no device. Ported from CommandDispatcherTest.kt.

func call(requestID, cmd string, timeoutMS int) wire.Call {
	return wire.Call{
		Type:      wire.TypeCall,
		RequestID: requestID,
		Cmd:       cmd,
		Args:      jsonRaw(`{}`),
		TimeoutMS: timeoutMS,
	}
}

func okHandler(context.Context, json.RawMessage) (json.RawMessage, error) {
	time.Sleep(10 * time.Millisecond)
	return jsonRaw(`{}`), nil
}

func slowHandler(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	// Respects ctx so a deadline cancels it — mirrors how real HTTP-backed
	// handlers behave under the dispatcher's per-call timeout. time.Sleep would
	// ignore cancellation and hide the timeout path.
	select {
	case <-time.After(5 * time.Second):
		return jsonRaw(`{}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func throwingHandler(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("bad x") // maps to device_error (no availability hint)
}

func throwingBadArgs(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errcode.New(errcode.BadArgs, "bad x")
}

func TestSuccessfulCallReturnsOKWithData(t *testing.T) {
	d := New(map[string]Handler{"tap": okHandler})
	r := d.Dispatch(context.Background(), call("req_1", "tap", 15_000))
	if !r.OK || r.Error != nil {
		t.Fatalf("want ok=true nil error, got %+v", r)
	}
}

func TestDuplicateRequestIDRejected(t *testing.T) {
	d := New(map[string]Handler{"tap": okHandler})
	first := d.Dispatch(context.Background(), call("req_1", "tap", 15_000))
	if !first.OK {
		t.Fatal("first call should succeed")
	}
	second := d.Dispatch(context.Background(), call("req_1", "tap", 15_000))
	if second.OK || second.Error.Code != errcode.DuplicateRequest {
		t.Fatalf("duplicate must be rejected with duplicate_request, got %+v", second)
	}
}

func TestResetClearsDedupSet(t *testing.T) {
	d := New(map[string]Handler{"tap": okHandler})
	if r := d.Dispatch(context.Background(), call("req_1", "tap", 15_000)); !r.OK {
		t.Fatal("first call should succeed")
	}
	if r := d.Dispatch(context.Background(), call("req_1", "tap", 15_000)); r.OK {
		t.Fatal("duplicate should be rejected")
	}
	d.ResetForNewConnection()
	// After reset (new connection per §4.2), the same request_id is acceptable.
	if r := d.Dispatch(context.Background(), call("req_1", "tap", 15_000)); !r.OK {
		t.Fatal("after reset, the request_id should be acceptable again")
	}
}

func TestUnknownCommandReturnsUnsupported(t *testing.T) {
	d := New(map[string]Handler{})
	r := d.Dispatch(context.Background(), call("req_1", "not_a_command", 15_000))
	if r.OK || r.Error.Code != errcode.Unsupported {
		t.Fatalf("unknown command must return unsupported, got %+v", r)
	}
}

func TestTimeoutMapsToTimeoutError(t *testing.T) {
	d := New(map[string]Handler{"slow": slowHandler})
	r := d.Dispatch(context.Background(), call("req_1", "slow", 100))
	if r.OK || r.Error.Code != errcode.Timeout {
		t.Fatalf("timeout must map to timeout code, got %+v", r)
	}
	if !r.Error.Retryable {
		t.Error("timeout should be retryable")
	}
}

func TestTimeoutMSClampedToMinimum(t *testing.T) {
	// A timeout_ms of 0 clamps to 1s, so a fast handler still succeeds.
	d := New(map[string]Handler{"tap": okHandler})
	r := d.Dispatch(context.Background(), call("req_1", "tap", 0))
	if !r.OK {
		t.Fatalf("clamped timeout should let a fast handler succeed, got %+v", r)
	}
}

func TestHandlerExceptionMapsToErrorCode(t *testing.T) {
	d := New(map[string]Handler{"throw": throwingBadArgs})
	r := d.Dispatch(context.Background(), call("req_1", "throw", 15_000))
	if r.OK || r.Error.Code != errcode.BadArgs {
		t.Fatalf("typed BadArgs should map to bad_args, got %+v", r)
	}
}

func TestNinthInFlightReturnsOverloaded(t *testing.T) {
	// Launch 8 concurrent slow calls to fill the cap, then a 9th must be rejected.
	d := New(map[string]Handler{"slow": slowHandler})
	var wg sync.WaitGroup
	started := make(chan struct{})
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(rid string) {
			defer wg.Done()
			_ = d.Dispatch(context.Background(), call(rid, "slow", 60_000))
		}(string(rune('0' + i))) // "1".."8"
	}
	// Give the slow handlers time to enter the in-flight section.
	time.Sleep(50 * time.Millisecond)
	ninth := d.Dispatch(context.Background(), call("rid_9", "slow", 15_000))
	if ninth.OK || ninth.Error.Code != errcode.Overloaded {
		t.Fatalf("9th in-flight must be overloaded, got %+v", ninth)
	}
	if !ninth.Error.Retryable {
		t.Error("overloaded should be retryable")
	}
	close(started)
	// Drain the slow handlers (cancel them via timeout rather than waiting 5s).
	wg.Wait()
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
