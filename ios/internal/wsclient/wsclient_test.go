package wsclient

import (
	"testing"
	"time"
)

// Full-jitter backoff (§7): delay = random(0, min(cap=30s, base=1s * 2^attempt)).
// Range is uniform; attempt caps at 30s. Ported from WsClientBackoffTest.kt.
func TestBackoffRange(t *testing.T) {
	cases := []struct {
		attempt    int
		upperBound time.Duration
	}{
		{0, 1_000 * time.Millisecond},
		{1, 2_000 * time.Millisecond},
		{2, 4_000 * time.Millisecond},
		{4, 16_000 * time.Millisecond},
		{5, 30_000 * time.Millisecond},
		{100, 30_000 * time.Millisecond},
	}
	for _, tc := range cases {
		var max time.Duration
		for i := 0; i < 5_000; i++ {
			d := ComputeBackoffDelay(tc.attempt)
			if d < 0 || d > tc.upperBound {
				t.Errorf("attempt %d delay %v outside [0,%v]", tc.attempt, d, tc.upperBound)
			}
			if d > max {
				max = d
			}
		}
		// Statistical sanity: with 5000 samples across a uniform range we
		// should approach the upper bound. Guards against off-by-one (e.g.
		// returning 0..upperBound-1 only).
		if max < tc.upperBound*99/100 {
			t.Errorf("attempt %d max %v too far from upper %v", tc.attempt, max, tc.upperBound)
		}
	}
}

func TestBackoffNeverNegative(t *testing.T) {
	for attempt := 0; attempt < 1000; attempt++ {
		if d := ComputeBackoffDelay(attempt); d < 0 {
			t.Fatalf("negative delay at attempt %d: %v", attempt, d)
		}
	}
}

func TestBackoffIsJittered(t *testing.T) {
	// With a 30s range and 500 samples, a constant-zero or constant-max
	// implementation would be overwhelmingly unlikely to produce many distinct
	// values. Guards against "return 0" or "return upper" regressions.
	seen := map[time.Duration]struct{}{}
	for i := 0; i < 500; i++ {
		seen[ComputeBackoffDelay(5)] = struct{}{}
	}
	if len(seen) <= 100 {
		t.Fatalf("backoff not jittered: only %d distinct values", len(seen))
	}
}
