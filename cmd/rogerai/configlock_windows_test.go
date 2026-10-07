//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConfigLockIgnoresAFileLeftByACrash: on Windows the config lock is an OS lock (LockFileEx), so
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

// TestConfigLockExcludesAnotherProcess: the OS lock holds across processes. A child roger
// process cannot take the lock while this one holds it, and takes it once it is released.
func TestConfigLockExcludesAnotherProcess(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	release, err := lockConfig(lock)
	require.NoError(t, err)
	child := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigLockChildHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), "ROGER_LOCK_CHILD="+lock)
		return cmd.Run()
	}
	require.Error(t, child(), "a second process took the lock while this one held it")
	release()
	require.NoError(t, child(), "the lock is free once released")
}

// TestConfigLockChildHelper is the child half of TestConfigLockExcludesAnotherProcess: it
// exits non-zero when the lock cannot be taken. It does nothing in a normal run.
func TestConfigLockChildHelper(t *testing.T) {
	path := os.Getenv("ROGER_LOCK_CHILD")
	if path == "" {
		return
	}
	release, err := lockConfig(path)
	if err != nil {
		os.Exit(3)
	}
	release()
}
