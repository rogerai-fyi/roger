package onair

// After a restart, pids are reused: the pid a lock or a share record names can belong to a new,
// unrelated process (a container's restart hands out the same pids in the same order). A holder
// that started after its lock was written is not that broadcaster (found 2026-10-10: a Pi that
// rebooted refused `roger --lan share` because the share's own shell had the old share's pid).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestALockWhosePidWasReusedIsTakenOver(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	path := LockPath("node-x")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	// a live pid (the test runner's parent), but the lock was written long before it started
	b, _ := json.Marshal(Info{PID: os.Getppid(), Station: "old", Model: "m", Started: time.Now().Add(-24 * 365 * time.Hour).Unix()})
	require.NoError(t, os.WriteFile(path, b, 0o600))
	release, err := Acquire("node-x", "new", "m")
	require.NoError(t, err, "a pid reused since the lock was written is not the broadcaster")
	release()
}

func TestALiveHolderStillHoldsItsLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	path := LockPath("node-y")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	b, _ := json.Marshal(Info{PID: os.Getppid(), Station: "live", Model: "m", Started: time.Now().Unix()})
	require.NoError(t, os.WriteFile(path, b, 0o600))
	_, err := Acquire("node-y", "new", "m")
	require.ErrorIs(t, err, ErrHeld)
}

func TestAShareRecordWhosePidWasReusedIsStale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	release, err := WriteShare(Share{PID: os.Getppid(), Band: "m", ChatURL: "http://127.0.0.1:1/v1/chat/completions"})
	require.NoError(t, err)
	defer release()
	require.Len(t, Shares(), 1, "written now by a live process")
	old := time.Now().Add(-24 * 365 * time.Hour)
	require.NoError(t, os.Chtimes(ShareFile(os.Getppid()), old, old))
	require.Empty(t, Shares(), "written before its pid's process started: a reused pid")
}

func TestStillTheProcessBranches(t *testing.T) {
	prev := processStart
	t.Cleanup(func() { processStart = prev })
	me, now := os.Getpid(), time.Now()

	processStart = func(int) (time.Time, bool, bool) { return now, true, true }
	require.False(t, StillTheProcess(me, now), "a zombie runs nothing")

	processStart = func(int) (time.Time, bool, bool) { return time.Time{}, false, false }
	require.True(t, StillTheProcess(me, now.Add(-time.Hour)), "where start times can't be read, a live pid is believed")

	processStart = func(int) (time.Time, bool, bool) { return now, false, true }
	require.True(t, StillTheProcess(me, time.Time{}), "a record with no time is judged by liveness alone")
	require.False(t, StillTheProcess(1<<30, now), "a pid that is gone")

	require.True(t, unixOrZero(0).IsZero())
	require.Equal(t, int64(42), unixOrZero(42).Unix())
}
