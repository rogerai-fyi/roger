package localmodel

// After a restart, pids are reused: the supervisor pid a record names can belong to a different
// process (or a zombie). Such a record is stale, not a serve to switch away from (found 2026-10-10:
// a Pi that rebooted kept a dead serve "on the air", and the next serve died retiring it).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeRecord(t *testing.T, h Home, s Serving) {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	require.NoError(t, os.WriteFile(h.recordPath(s.Port), b, 0o600))
}

func TestARecordWhosePidWasReusedIsStale(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	written := time.Unix(1_700_000_000, 0)
	writeRecord(t, h, Serving{Supervisor: 37, Port: 41000, Band: "m", Started: written})
	prevAlive, prevStart := alive, procStart
	t.Cleanup(func() { alive, procStart = prevAlive, prevStart })
	alive = func(int) bool { return true }

	procStart = func(int) (time.Time, bool, bool) { return written.Add(-time.Second), false, true }
	require.Len(t, h.LiveAll(), 1, "the process that wrote the record is still running")

	procStart = func(int) (time.Time, bool, bool) { return written.Add(time.Hour), false, true }
	require.Empty(t, h.LiveAll(), "a process that started after the record was written is not its supervisor")

	procStart = func(int) (time.Time, bool, bool) { return written.Add(-time.Second), true, true }
	require.Empty(t, h.LiveAll(), "a zombie serves nothing")

	procStart = func(int) (time.Time, bool, bool) { return time.Time{}, false, false }
	require.Len(t, h.LiveAll(), 1, "where start times can't be read, a live pid is still believed")
	_ = filepath.Join
}
