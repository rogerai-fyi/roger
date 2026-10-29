package tower

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// LoadConfig is the one loader every tower command and the standalone serve use: it requires a
// path, reads and validates the file, and names the data directory's state file when that is
// what it was given.
func TestLoadConfig(t *testing.T) {
	_, err := LoadConfig("")
	require.ErrorContains(t, err, "--config is required")

	_, err = LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)

	dir := t.TempDir()
	state := filepath.Join(dir, "tower.json")
	require.NoError(t, os.WriteFile(state, []byte(`{"tower_id":"abc123"}`), 0o600))
	_, err = LoadConfig(state)
	require.ErrorContains(t, err, "is a Tower DATA DIRECTORY state file, not a configuration file")

	// JSON that is not a state file is judged by the real parser, not by this guard.
	notState := filepath.Join(dir, "other.json")
	require.NoError(t, os.WriteFile(notState, []byte(`{"x":1}`), 0o600))
	_, err = LoadConfig(notState)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "DATA DIRECTORY")

	good := filepath.Join(dir, "tower.yaml")
	require.NoError(t, os.WriteFile(good, []byte("apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: standalone\nstandalone:\n  bind: 127.0.0.1:9000\n  label: shed\n  advertise: true\n"), 0o600))
	c, err := LoadConfig(good)
	require.NoError(t, err)
	require.Equal(t, ModeStandalone, c.Mode)
	require.Equal(t, LocalConfig{Bind: "127.0.0.1:9000", Label: "shed", Advertise: true}, *c.Standalone)
}
