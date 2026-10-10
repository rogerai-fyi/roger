//go:build !unix && !windows

package toweradmin

import "net"

// A platform with neither unix sockets nor named pipes (features/join/admin_socket.feature): on
// this platform an administration command asks the owner to stop serve, as before.
func listen(string) (net.Listener, error) { return nil, ErrUnsupported }

func dial(string) (net.Conn, error) { return nil, ErrUnsupported }
