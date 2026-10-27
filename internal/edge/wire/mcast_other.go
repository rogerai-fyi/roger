//go:build !linux

package wire

import "net"

// onlyJoinedInterface sets nothing off Linux: there is no portable equivalent of IP_MULTICAST_ALL.
// On a multi-homed BSD or macOS machine a socket may also hear the group from an interface it did
// not join, so a packet can arrive more than once per interface; the once-per-interface test is
// Linux only (transport_loopback_linux_test.go).
func onlyJoinedInterface(*net.UDPConn) error { return nil }
