package onair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTowerRecordsListOnlyLiveServes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	require.Empty(t, Towers())

	release, err := WriteTower(Tower{PID: os.Getpid(), Dir: "/srv/lab", Label: "lab", Addr: "192.168.1.20:8787"})
	require.NoError(t, err)
	got := Towers()
	require.Len(t, got, 1)
	require.Equal(t, "lab", got[0].Label)
	fi, err := os.Stat(TowerFile(os.Getpid()))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "owner-only, like a share record")

	// A record whose process is gone is dropped and removed; a damaged one is skipped.
	_, err = WriteTower(Tower{PID: 999999, Dir: "/srv/gone"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(TowersDir(), "tower-5.json"), []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(TowersDir(), "tower-6.json"), []byte(`{"pid":6}`), 0o600))
	got = Towers()
	require.Len(t, got, 1)
	require.Equal(t, "/srv/lab", got[0].Dir)
	_, err = os.Stat(TowerFile(999999))
	require.True(t, os.IsNotExist(err), "the dead serve's record is removed")

	release()
	require.Empty(t, Towers(), "a stopped serve removes its record")
}
