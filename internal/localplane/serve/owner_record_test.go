package serve

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/onair"
)

// TestAServingTowerIsListedForItsOwnersScreens: while serve runs, this user's TUI and console can
// find it (its data directory, name and address); when it stops, the record goes with it.
func TestAServingTowerIsListedForItsOwnersScreens(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := standaloneDir(t)
	p, err := Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0", "--label", "lab"}, io.Discard)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- p.Serve() }()

	require.Eventually(t, func() bool { return len(onair.Towers()) == 1 }, 3*time.Second, 20*time.Millisecond)
	got := onair.Towers()[0]
	require.Equal(t, dir, got.Dir)
	require.Equal(t, "lab", got.Label)
	require.Equal(t, p.Addr(), got.Addr)
	require.Equal(t, os.Getpid(), got.PID)

	require.NoError(t, p.Close())
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop")
	}
	require.Empty(t, onair.Towers(), "a stopped serve is no longer listed")
}
