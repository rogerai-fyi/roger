//go:build !unix

package main

import (
	"os"
	"path/filepath"
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
