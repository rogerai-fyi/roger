//go:build unix

package toweradmin

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func listen(path string) (net.Listener, error) {
	// The directory's mode closes the moment between creating the socket and making it 0600, and
	// is the only guard where the peer's user is not read from the socket (off Linux).
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the Tower's data directory %s can be entered by other users (mode %o); run: chmod 700 %s", dir, fi.Mode().Perm(), dir)
	}
	if _, err := os.Lstat(path); err == nil {
		if c, derr := net.DialTimeout("unix", path, time.Second); derr == nil {
			_ = c.Close()
			return nil, fmt.Errorf("a Tower already answers on %s", path)
		}
		// Left by a serve that crashed: nothing is listening, so replace it.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The data directory is already owner-only; the socket is too.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func dial(path string) (net.Conn, error) { return net.DialTimeout("unix", path, 2*time.Second) }
