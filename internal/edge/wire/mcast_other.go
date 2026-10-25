//go:build !linux

package wire

import "net"

// onlyJoinedInterface is Linux's concern: other systems deliver the group per joined interface.
func onlyJoinedInterface(*net.UDPConn) error { return nil }
