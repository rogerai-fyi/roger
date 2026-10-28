//go:build unix

package toweradmin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The admin socket only opens in a data directory no one else can enter: the directory's mode is
// what closes the moment between creating the socket and making it 0600, and the only guard off
// Linux, where the peer's user is not read from the socket.
func TestTheAdminSocketRefusesADirectoryOthersCanEnter(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o711} {
		dir := filepath.Join(t.TempDir(), "tower")
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.Chmod(dir, mode))
		ln, err := Listen(dir)
		if ln != nil {
			_ = ln.Close()
		}
		require.Error(t, err, "mode %o", mode)
		require.Contains(t, err.Error(), "chmod 700")
		_, serr := os.Lstat(Path(dir))
		require.True(t, os.IsNotExist(serr), "no socket is left behind for mode %o", mode)
	}
	dir := filepath.Join(t.TempDir(), "tower")
	require.NoError(t, os.Mkdir(dir, 0o700))
	ln, err := Listen(dir)
	require.NoError(t, err)
	_ = ln.Close()
}
