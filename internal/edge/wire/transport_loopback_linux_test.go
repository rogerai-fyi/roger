//go:build linux

package wire

// Once per interface is what IP_MULTICAST_ALL=0 gives on Linux (mcast_linux.go). Other systems
// have no such switch, so this is pinned on Linux only (mcast_other.go says what they do).

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Measured on a machine with three private interfaces (2026-10-25): with loopback on, one packet sent
// came back nine times, to the sender and to every other roger there - Linux delivers the group to a
// socket on every interface any socket joined (IP_MULTICAST_ALL). A responder answering nine copies
// on three interfaces, each answer heard nine times, overflows a browser's queue. Each socket now hears
// only the interface it joined: one packet sent on K interfaces is heard K times, once per interface.
func TestOnePacketIsHeardOncePerInterface(t *testing.T) {
	multicastInterface(t)
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{})
	require.NoError(t, err)
	port := probe.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, probe.Close())
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: port}
	list := func() []net.Interface { ifs, _ := net.Interfaces(); return ifs }
	usable := func(ifi net.Interface) bool {
		return ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && HasLAN(InterfaceAddrs([]net.Interface{ifi}))
	}
	listen := func(ifi *net.Interface) (*net.UDPConn, error) { return listenMDNS(ifi, group) }
	tx, err := newRejoiningTransport(list, listen, usable, group)
	if err != nil {
		t.Skip("no private multicast interface on this machine")
	}
	defer tx.Close()
	rx, err := newRejoiningTransport(list, listen, usable, group)
	require.NoError(t, err)
	defer rx.Close()

	require.NoError(t, tx.Send([]byte("roger")))
	heard := 0
	buf := make([]byte, 16)
	for {
		require.NoError(t, rx.SetReadDeadline(time.Now().Add(300*time.Millisecond)))
		if _, _, err := rx.Recv(buf); err != nil {
			break
		}
		heard++
	}
	require.Equal(t, len(tx.joined()), heard, "joined %v", tx.joined())
}
