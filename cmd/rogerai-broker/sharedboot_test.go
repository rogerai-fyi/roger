package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSharedRetryBackoffSchedule pins the boot retry's schedule in virtual time
// (features/ops/shared_store_readiness.feature R2): the first retry within 1 s, then doubling,
// capped at 30 s, so two minutes of outage see more than 4 attempts and no gap over 30 s, and
// an hour keeps retrying at the cap.
func TestSharedRetryBackoffSchedule(t *testing.T) {
	first := sharedRetryBackoff(0)
	require.Greater(t, first, time.Duration(0))
	require.LessOrEqual(t, first, time.Second)

	tests := []struct {
		name   string
		window time.Duration
		minN   int
	}{
		{"two minutes", 2 * time.Minute, 5},
		{"one hour", time.Hour, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var elapsed, d time.Duration
			n := 0
			for {
				d = sharedRetryBackoff(d)
				require.LessOrEqual(t, d, 30*time.Second)
				if elapsed+d > tc.window {
					break
				}
				elapsed += d
				n++
			}
			require.GreaterOrEqual(t, n, tc.minN)
			require.Equal(t, 30*time.Second, d, "a long outage retries at the cap")
		})
	}
	require.Equal(t, 2*first, sharedRetryBackoff(first), "doubling")
}
