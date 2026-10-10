//go:build unix

package toweradmin

import (
	"fmt"
	"net"
	"os"
	"time"
)

func listen(path string) (net.Listener, error) {
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
