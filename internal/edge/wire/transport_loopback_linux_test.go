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
	all := func() []net.Interface { ifs, _ := net.Interfaces(); return ifs }
	usable := func(ifi net.Interface) bool {
		return ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && HasLAN(InterfaceAddrs([]net.Interface{ifi}))
	}
	listen := func(ifi *net.Interface) (*net.UDPConn, error) { return listenMDNS(ifi, group) }
	rx, err := newRejoiningTransport(all, listen, usable, group)
	if err != nil {
		t.Skip("no private multicast interface on this machine")
	}
	defer rx.Close()
	// Send on ONE interface whose subnet no other joined interface shares, so no copy can come back
	// through the network onto another of them: whatever rx hears is the loopback alone. Hearing it
	// on every joined socket was the fault.
	one, ok := loneSubnetInterface(all(), usable)
	if !ok || len(rx.joined()) < 2 {
		t.Skip("needs two private interfaces on different subnets to tell once from once per socket")
	}
	tx, err := newRejoiningTransport(func() []net.Interface { return []net.Interface{one} }, listen, usable, group)
	require.NoError(t, err)
	defer tx.Close()

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
	require.Equal(t, 1, heard, "sent on %s, heard by the socket on that interface only (joined %v)", one.Name, rx.joined())
}

// loneSubnetInterface is a usable interface whose IPv4 /24 no other usable interface has.
func loneSubnetInterface(ifs []net.Interface, usable func(net.Interface) bool) (net.Interface, bool) {
	nets := map[string][]net.Interface{}
	for _, ifi := range ifs {
		if !usable(ifi) {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				key := ipn.IP.Mask(net.CIDRMask(24, 32)).String()
				nets[key] = append(nets[key], ifi)
				break
			}
		}
	}
	for _, same := range nets {
		if len(same) == 1 {
			return same[0], true
		}
	}
	return net.Interface{}, false
}
