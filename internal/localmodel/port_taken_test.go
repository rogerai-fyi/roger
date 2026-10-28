package localmodel

// A port roger chose is free only until someone binds it. When another program takes it between the
// choice and the runtime binding it, the runtime exits at once; that is not the model failing, so roger
// chooses another port and starts the runtime once more (CI, 2026-10-28: the gate's other packages
// took the port). A port the caller named is never swapped, and a runtime that exits for any other
// reason is not started twice.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/runtime"
)

// takenThenFree makes the first suggested port one this test already holds, then hands out real ones.
func takenThenFree(t *testing.T) (taken int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	taken = ln.Addr().(*net.TCPAddr).Port
	prev := suggestPort
	calls := 0
	suggestPort = func(start int) (int, error) {
		calls++
		if calls == 1 {
			return taken, nil
		}
		return prev(start)
	}
	t.Cleanup(func() { suggestPort = prev })
	return taken
}

func TestAPortTakenBeforeTheRuntimeBindsItIsSwappedForAnother(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	taken := takenThenFree(t)
	srv, s, err := h.Serve(context.Background(), ServeOpts{Model: servable(t, h), Exe: os.Args[0], Tag: "bX",
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second, Env: map[string]string{"FAKE_LLAMA": "1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	require.NotEqual(t, taken, s.Port, "it is serving on another port")
	_, err = os.Stat(h.keyPath(taken))
	require.True(t, os.IsNotExist(err), "the first port's key file is not left behind")
	_, statErr := os.Stat(h.recordPath(taken))
	require.True(t, os.IsNotExist(statErr), "and it has no record")
}

func TestAPortTheCallerNamedIsNeverSwapped(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	taken := ln.Addr().(*net.TCPAddr).Port
	_, _, err = h.Serve(context.Background(), ServeOpts{Model: servable(t, h), Exe: os.Args[0], Tag: "bX", Port: taken,
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second, Env: map[string]string{"FAKE_LLAMA": "1"}})
	require.True(t, errors.Is(err, runtime.ErrExited), "the owner's port is theirs to fix: %v", err)
}

func TestARuntimeThatExitsForAnotherReasonIsNotStartedTwice(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	starts := filepath.Join(t.TempDir(), "starts")
	_, _, err := h.Serve(context.Background(), ServeOpts{Model: servable(t, h), Exe: os.Args[0], Tag: "bX",
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second,
		Env: map[string]string{"FAKE_LLAMA": "1", "FAKE_EXIT_AT_ONCE": starts}})
	require.True(t, errors.Is(err, runtime.ErrExited), "%v", err)
	b, err := os.ReadFile(starts)
	require.NoError(t, err)
	require.Equal(t, "x", string(b), "one start: its port was free, so the exit was the runtime's own")
}
