package errcode

import (
	"context"
	"errors"
	"testing"
)

// Ported from ErrorCodeMappingTest.kt. Pins every branch of From.
func TestFromMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"typed Code carries its own", New(StaleNode, "x"), StaleNode},
		{"typed NotFound", New(NotFound, "x"), NotFound},
		{"sentinel ErrStaleNode", ErrStaleNode, StaleNode},
		{"sentinel ErrNotFound", ErrNotFound, NotFound},
		{"sentinel ErrBadArgs", ErrBadArgs, BadArgs},
		{"sentinel ErrPermissionDenied", ErrPermissionDenied, PermissionDenied},
		{"sentinel ErrTimeout", ErrTimeout, Timeout},
		{"sentinel ErrNotReady", ErrNotReady, NotReady},
		{"sentinel ErrCancelled", ErrCancelled, Cancelled},
		{"context.DeadlineExceeded", context.DeadlineExceeded, Timeout},
		{"context.Canceled", context.Canceled, Cancelled},
		{"availability hint 'not available'", errors.New("Accessibility service is not available"), NotReady},
		{"availability hint 'not enabled'", errors.New("service not enabled"), NotReady},
		{"availability hint 'not ready'", errors.New("not ready yet"), NotReady},
		{"no availability hint", errors.New("ACTION_CLICK failed"), DeviceError},
		{"generic error", errors.New("anything"), DeviceError},
		{"nil", nil, DeviceError},
	}
	for _, c := range cases {
		if got := From(c.err); got != c.want {
			t.Errorf("%s: From(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

func TestNewFormatsMessage(t *testing.T) {
	c := New(StaleNode, "node_id not found: %s", "node_42")
	if c.Code != StaleNode || c.Message != "node_id not found: node_42" {
		t.Fatalf("unexpected Code: %+v", c)
	}
}

func TestIsMatchesOnCode(t *testing.T) {
	if !errors.Is(New(StaleNode, "x"), ErrStaleNode) {
		t.Error("errors.Is should match a thrown StaleNode Code against ErrStaleNode")
	}
	if errors.Is(New(NotFound, "x"), ErrStaleNode) {
		t.Error("NotFound should not match ErrStaleNode")
	}
}
