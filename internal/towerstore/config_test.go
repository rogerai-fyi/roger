package towerstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/tower"
)

func standaloneState(t *testing.T) *tower.State {
	t.Helper()
	st, err := tower.Init(filepath.Join(t.TempDir(), "tw"), tower.ModeStandalone)
	require.NoError(t, err)
	return st
}

// ForConfig: no storage keeps the data directory; a configured database is used and fails
// closed (an unreadable URL file or a non-local database is an error, never a quiet fallback).
func TestForConfigChoosesTheStoreAndFailsClosed(t *testing.T) {
	st := standaloneState(t)

	for _, c := range []*tower.Config{nil, {}, {Storage: &tower.StorageConfig{}}} {
		got, closeFn, err := ForConfig(c, st)
		require.NoError(t, err)
		require.Same(t, st, got, "no storage configured: the data directory is the store")
		require.NoError(t, closeFn())
	}

	_, closeFn, err := ForConfig(&tower.Config{Storage: &tower.StorageConfig{URLFile: "/nonexistent/db-url"}}, st)
	require.ErrorContains(t, err, "cannot read the database URL file")
	require.NoError(t, closeFn())

	public := filepath.Join(t.TempDir(), "db-url")
	require.NoError(t, os.WriteFile(public, []byte("postgres://u:p@db.example.com:5432/tower\n"), 0o600))
	_, _, err = ForConfig(&tower.Config{Storage: &tower.StorageConfig{URLFile: public}}, st)
	require.ErrorContains(t, err, "allowlist")
}

func TestForConfigUsesALiveDatabase(t *testing.T) {
	dsn := realDSN(t)
	f := filepath.Join(t.TempDir(), "db-url")
	require.NoError(t, os.WriteFile(f, []byte(dsn+"\n"), 0o600))
	st := standaloneState(t)
	got, closeFn, err := ForConfig(&tower.Config{Storage: &tower.StorageConfig{URLFile: f}}, st)
	require.NoError(t, err)
	require.NotSame(t, st, got, "a configured database backs the state")
	require.NoError(t, closeFn())
}
