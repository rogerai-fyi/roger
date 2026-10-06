package main

// acctkeys_lock_backoff_test.go: a key's check-and-hold lock in the shared store is waited for
// with a bounded backoff, not a tight poll (state audit 2026-10-05). A waiter blocked for 300ms
// behind another holder must reach the shared store a bounded number of times, and must still
// take the lock promptly once it is free.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyLockWaitsWithBackoff(t *testing.T) {
	vs, mr := testValkeyShared(t)
	b := &broker{shared: vs}
	const held = 300 * time.Millisecond
	require.NoError(t, mr.Set(counterKeyPrefix+"acctkeylock:k", "other"))
	go func() {
		time.Sleep(held)
		mr.Del(counterKeyPrefix + "acctkeylock:k")
	}()
	before := mr.CommandCount()
	start := time.Now()
	unlock, err := b.keyLock("k")
	waited := time.Since(start)
	require.NoError(t, err)
	unlock()
	attempts := mr.CommandCount() - before
	require.LessOrEqual(t, attempts, 40, "a waiter polled the shared store %d times in %s", attempts, waited)
	require.Less(t, waited, held+150*time.Millisecond, "the lock must be taken soon after it frees")
}

// TestKeyLocalLocksStayBounded: the local per-key locks (an instance with no shared store) are
// a fixed set, so locking ever more distinct keys never grows the instance's memory.
func TestKeyLocalLocksStayBounded(t *testing.T) {
	b := &broker{}
	for i := 0; i < 1000; i++ {
		unlock, err := b.keyLock(fmt.Sprintf("key_%d", i))
		require.NoError(t, err)
		unlock()
	}
	require.LessOrEqual(t, len(b.ak.locks), 64, "local key locks grow with every key ever locked")
}
