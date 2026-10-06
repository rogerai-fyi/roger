package main

// acctkeys_bump_retry_test.go: an epoch bump that fails (the shared store is unreachable) is
// owed, re-sent by ONE background retrier, and lands once the store answers again, so peers
// stop serving a key changed during the outage (slice-5 review 2026-10-05).

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyEpochBumpOwedUntilItLands(t *testing.T) {
	vs, mr := testValkeyShared(t)
	b := &broker{shared: vs}
	mr.Close()
	b.bumpKeyEpoch()
	b.bumpKeyEpoch() // a second failure while the retrier runs is owed to the same retrier
	b.ak.mu.Lock()
	require.True(t, b.ak.bumpRetrying)
	require.Equal(t, 2, b.ak.bumpOwed)
	b.ak.mu.Unlock()

	require.NoError(t, mr.Restart())
	require.Eventually(t, func() bool {
		b.ak.mu.Lock()
		defer b.ak.mu.Unlock()
		return !b.ak.bumpRetrying
	}, 5*time.Second, 20*time.Millisecond, "the retrier must finish once the store answers")
	ep, err := b.keyEpoch()
	require.NoError(t, err)
	require.GreaterOrEqual(t, ep, 1.0, "the owed bump must reach the shared epoch")
}
