package wire

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// The two socket options that let two rogers on one machine hear each other once, read back off a
// real listener. Unlike the end-to-end test, this needs only one multicast interface, so it runs in CI.
func TestTheMDNSListenerHearsItsOwnInterfaceAndLoopsBack(t *testing.T) {
	ifi := multicastInterface(t)
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{})
	require.NoError(t, err)
	port := probe.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, probe.Close())
	c, err := listenMDNS(ifi, &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: port})
	require.NoError(t, err)
	defer c.Close()
	rc, err := c.SyscallConn()
	require.NoError(t, err)
	var all, loop int
	var e1, e2 error
	require.NoError(t, rc.Control(func(fd uintptr) {
		all, e1 = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL)
		loop, e2 = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_LOOP)
	}))
	require.NoError(t, e1)
	require.NoError(t, e2)
	require.Zero(t, all, "the socket hears only the interface it joined")
	require.Equal(t, 1, loop, "and hears this machine's own packets")
}
