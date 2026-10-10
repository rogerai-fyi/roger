//go:build !unix

package toweradmin

import "net"

// The owner-only named pipe for Windows is not built yet (features/join/admin_socket.feature): on
// this platform an administration command asks the owner to stop serve, as before.
func listen(string) (net.Listener, error) { return nil, ErrUnsupported }

func dial(string) (net.Conn, error) { return nil, ErrUnsupported }
