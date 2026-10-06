//go:build !unix

package main

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConfigLockHonorsTheDeadlineDuringATakeover: while another waiter holds the takeover of
// a stale lock, a waiter still gives up at its deadline (it never spins past it).
func TestConfigLockHonorsTheDeadlineDuringATakeover(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	require.NoError(t, os.WriteFile(lock, []byte("crashed"), 0o600))
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(lock, old, old))
	require.NoError(t, os.WriteFile(lock+".takeover", nil, 0o600)) // a live takeover in progress
	done := make(chan error, 1)
	go func() {
		release, err := lockConfig(lock)
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "locked by another roger command")
	case <-time.After(10 * time.Second):
		t.Fatal("lockConfig spun past its 5 s deadline")
	}
}

// TestConfigLockReleasesOnlyItsOwn: a holder whose stale lock was taken over never removes
// the new holder's lock when it finally releases.
func TestConfigLockReleasesOnlyItsOwn(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	release, err := lockConfig(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lock, []byte("someone-else"), 0o600)) // taken over meanwhile
	release()
	_, err = os.Stat(lock)
	require.NoError(t, err, "release removed a lock it no longer held")
}

// TestConfigLockStaleTakeoverIsExclusive: many waiters on one stale lock never hold it at
// the same time.
func TestConfigLockStaleTakeoverIsExclusive(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	for round := 0; round < 20; round++ {
		require.NoError(t, os.WriteFile(lock, []byte("crashed"), 0o600))
		old := time.Now().Add(-time.Minute)
		require.NoError(t, os.Chtimes(lock, old, old))
		var active, overlap atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := lockConfig(lock)
				if err != nil {
					return
				}
				if active.Add(1) > 1 {
					overlap.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
				active.Add(-1)
				release()
			}()
		}
		wg.Wait()
		require.Zero(t, overlap.Load(), "two waiters held the lock at once (round %d)", round)
	}
}
