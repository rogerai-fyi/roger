package wire

import (
	"net"

	"golang.org/x/sys/unix"
)

// onlyJoinedInterface makes c hear the group only on the interface it joined. Linux otherwise delivers
// the group to every socket on the port from every interface any socket joined (IP_MULTICAST_ALL), so
// with loopback on, one packet sent on K interfaces reaches each socket K*K times.
func onlyJoinedInterface(c *net.UDPConn) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
	}); err != nil {
		return err
	}
	return serr
}
