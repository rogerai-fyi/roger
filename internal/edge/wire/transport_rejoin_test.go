package wire

// Found with tools/edgesim (2026-10-04): a member whose network interface went away and came back
// (a container reconnected; on a real board, a USB Ethernet adapter re-enumerating) kept writing to
// the vanished interface - "sendto: no such device" - and never came back on its Edge until roger
// restarted. The transport now rejoins: a vanished interface is dropped and a new one joined.

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheMulticastTransportRejoinsWhenInterfacesChange(t *testing.T) {
	// A receiver standing in for the multicast group.
	rx, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer rx.Close()

	var mu sync.Mutex
	ifaces := []net.Interface{{Index: 2, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast}}
	list := func() []net.Interface { mu.Lock(); defer mu.Unlock(); return append([]net.Interface(nil), ifaces...) }
	listen := func(*net.Interface) (*net.UDPConn, error) {
		return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	}
	tp, err := newRejoiningTransport(list, listen, func(net.Interface) bool { return true }, rx.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer tp.Close()
	require.Equal(t, []string{"eth0#2"}, tp.joined())

	// The interface is re-created (a new index): the next send, once the rescan is due, drops the old
	// socket and joins the new interface, and the packet still goes out.
	mu.Lock()
	ifaces = []net.Interface{{Index: 3, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast}}
	mu.Unlock()
	tp.lastScan = time.Now().Add(-time.Minute)
	require.NoError(t, tp.Send([]byte("hello")))
	require.Equal(t, []string{"eth0#3"}, tp.joined())
	buf := make([]byte, 16)
	_ = rx.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := rx.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "hello", string(buf[:n]))

	// A write that fails (the device vanished between scans) rejoins at once and retries.
	tp.closeAll()
	require.NoError(t, tp.Send([]byte("again")), "rejoined and resent, not left failing")
	n, _, err = rx.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "again", string(buf[:n]))

	// A side that only LISTENS (the responder answering queries) rejoins too: a receive, once the
	// rescan is due, joins an interface that came back while nothing was being sent.
	mu.Lock()
	ifaces = []net.Interface{{Index: 4, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast}}
	mu.Unlock()
	tp.lastScan = time.Now().Add(-time.Minute)
	require.NoError(t, tp.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	_, _, err = tp.Recv(buf)
	require.True(t, isTimeout(err))
	require.Equal(t, []string{"eth0#4"}, tp.joined(), "rejoined while only listening")

	// No usable interface at all is the one ErrNoLAN, as before.
	_, err = newRejoiningTransport(func() []net.Interface { return nil }, listen, func(net.Interface) bool { return true }, rx.LocalAddr().(*net.UDPAddr))
	require.ErrorIs(t, err, ErrNoLAN)
}

// After the network comes back a member may hold a new address; its answers carry the addresses it
// has NOW, or every peer dials the old one and calls it unreachable (found with tools/edgesim).
func TestAnAdvertisementCarriesTheAddressesOfNow(t *testing.T) {
	var mu sync.Mutex
	ip := net.IPv4(10, 89, 2, 5)
	r := NewLiveResponder(nil, "_rogerai._tcp", Advert{NodeID: "n_abc", Port: 44100}, func() []net.IP {
		mu.Lock()
		defer mu.Unlock()
		return []net.IP{ip}
	})
	p, err := r.Packet()
	require.NoError(t, err)
	require.True(t, bytes.Contains(p, []byte{10, 89, 2, 5}))
	mu.Lock()
	ip = net.IPv4(10, 89, 2, 6)
	mu.Unlock()
	p, err = r.Packet()
	require.NoError(t, err)
	require.True(t, bytes.Contains(p, []byte{10, 89, 2, 6}), "the new address is what is said")
	require.False(t, bytes.Contains(p, []byte{10, 89, 2, 5}), "never the old one")
}
