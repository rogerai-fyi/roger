//go:build unix

package filelock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

// TestTryLockRefusesWhileHeld: a second holder is told no without waiting, and gets the lock
// once the first releases it.
func TestTryLockRefusesWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, b := open(t, path), open(t, path)
	ok, err := TryLock(a)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = TryLock(b)
	require.NoError(t, err)
	require.False(t, ok, "a second holder took the lock while the first held it")
	require.NoError(t, Unlock(a))
	ok, err = TryLock(b)
	require.NoError(t, err)
	require.True(t, ok, "the lock is free once released")
	require.NoError(t, Unlock(b))
}

// TestLockWaitsForTheHolder: Lock blocks while another holder has the lock and returns once it
// is released.
func TestLockWaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, b := open(t, path), open(t, path)
	require.NoError(t, Lock(a))
	got := make(chan error, 1)
	go func() { got <- Lock(b) }()
	select {
	case <-got:
		t.Fatal("Lock returned while another holder had the lock")
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, Unlock(a))
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Lock never returned after the holder released")
	}
	require.NoError(t, Unlock(b))
}

// TestTryLockOnAClosedFileIsAnError: a failure that is not contention is reported as an error,
// not as "held by someone else".
func TestTryLockOnAClosedFileIsAnError(t *testing.T) {
	f := open(t, filepath.Join(t.TempDir(), "x.lock"))
	f.Close()
	ok, err := TryLock(f)
	require.Error(t, err)
	require.False(t, ok)
}

// TestTheLockHoldsAcrossProcesses: a child process cannot take the lock while this one holds it,
// and the OS frees it when the holder lets go.
func TestTheLockHoldsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a := open(t, path)
	require.NoError(t, Lock(a))
	child := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFileLockChildHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), "FILELOCK_CHILD="+path)
		return cmd.Run()
	}
	require.Error(t, child(), "a second process took the lock while this one held it")
	require.NoError(t, Unlock(a))
	require.NoError(t, child(), "the lock is free once released")
}

// TestFileLockChildHelper is the child half of TestTheLockHoldsAcrossProcesses: it exits
// non-zero when the lock is held elsewhere. It does nothing in a normal run.
func TestFileLockChildHelper(t *testing.T) {
	path := os.Getenv("FILELOCK_CHILD")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	ok, err := TryLock(f)
	if err != nil || !ok {
		os.Exit(3)
	}
	_ = Unlock(f)
}
