package wire

// Found with tools/edgesim host mode (2026-10-25): two roger processes on one machine - a simulated Pi
// beside the authority, or a second instance with its own config - never heard each other. Go's
// net.ListenMulticastUDP turns multicast loopback off, so a packet one process sent to the group never
// reached another socket on the same host. The mDNS listener now turns it back on; a process skips its
// own advert by node id (discovery.go), so hearing itself is harmless.

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTwoListenersOnOneMachineHearEachOther(t *testing.T) {
	ifi := multicastInterface(t)
	// A free port, not 5353, so the test neither hears nor talks to a real Edge.
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{})
	require.NoError(t, err)
	port := probe.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, probe.Close())
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: port}

	a, err := listenMDNS(ifi, group)
	require.NoError(t, err)
	defer a.Close()
	b, err := listenMDNS(ifi, group)
	require.NoError(t, err)
	defer b.Close()

	_, err = a.WriteTo([]byte("roger"), group)
	require.NoError(t, err)
	require.NoError(t, b.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 16)
	n, _, err := b.ReadFrom(buf)
	require.NoError(t, err, "a packet sent to the group by one listener must reach another on the same machine")
	require.Equal(t, "roger", string(buf[:n]))
}

// multicastInterface is an up, multicast-capable interface with an IPv4 address, or a skip.
func multicastInterface(t *testing.T) *net.Interface {
	t.Helper()
	ifs, err := net.Interfaces()
	require.NoError(t, err)
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				ifi := ifi
				return &ifi
			}
		}
	}
	t.Skip("no multicast-capable IPv4 interface on this machine")
	return nil
}
