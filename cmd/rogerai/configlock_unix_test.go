//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConfigLockIgnoresAFileLeftByACrash: on Unix the config lock is an OS lock (flock), so
// a lock file a crashed writer left behind, however fresh, never blocks the next writer; the
// OS released the lock when the process died.
func TestConfigLockIgnoresAFileLeftByACrash(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	require.NoError(t, os.WriteFile(lock, []byte("crashed-a-moment-ago"), 0o600))
	start := time.Now()
	release, err := lockConfig(lock)
	require.NoError(t, err)
	release()
	require.Less(t, time.Since(start), time.Second, "a crashed writer's file is not waited on")
}
