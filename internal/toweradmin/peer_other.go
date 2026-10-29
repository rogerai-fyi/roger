//go:build !linux

package toweradmin

import "net"

// Off Linux the socket file's 0600 mode is the check.
func checkPeer(net.Conn) error { return nil }
