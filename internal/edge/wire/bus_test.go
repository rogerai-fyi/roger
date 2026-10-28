package wire

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// bus is an in-memory multicast plane: every Send reaches every other joined end. It stands in
// for the network only; the codec, the responder and the browser on top of it are the real ones.
type bus struct {
	mu   sync.Mutex
	ends []*busEnd
}

type busEnd struct {
	b    *bus
	from net.Addr
	in   chan []byte
	dl   time.Time
	mu   sync.Mutex
}

func (b *bus) join(ip string) *busEnd {
	e := &busEnd{b: b, from: &net.UDPAddr{IP: net.ParseIP(ip), Port: 5353}, in: make(chan []byte, 4096)}
	b.mu.Lock()
	b.ends = append(b.ends, e)
	b.mu.Unlock()
	return e
}

func (e *busEnd) Send(p []byte) error {
	e.b.mu.Lock()
	defer e.b.mu.Unlock()
	for _, o := range e.b.ends {
		if o != e {
			select {
			case o.in <- append([]byte(nil), p...):
			default:
			}
		}
	}
	return nil
}

func (e *busEnd) Recv(p []byte) (int, net.Addr, error) {
	e.mu.Lock()
	dl := e.dl
	e.mu.Unlock()
	wait := time.Until(dl)
	if dl.IsZero() {
		wait = time.Hour
	}
	if wait <= 0 {
		return 0, nil, timeoutError{}
	}
	select {
	case m := <-e.in:
		return copy(p, m), e.from, nil
	case <-time.After(wait):
		return 0, nil, timeoutError{}
	}
}

func (e *busEnd) SetReadDeadline(t time.Time) error {
	e.mu.Lock()
	e.dl = t
	e.mu.Unlock()
	return nil
}

func (e *busEnd) Close() error { return nil }

// TestResponderAnswersABrowse: a real responder answers a real browse over the plane, and the
// browser reassembles the advertisement it sent; a different service is not answered.
func TestResponderAnswersABrowse(t *testing.T) {
	b := &bus{}
	ad := Advert{NodeID: "n_tower1", Name: "garage", Kind: "tower", Port: 8787}
	r := NewResponder(b.join("192.168.1.50"), "_rogertower._tcp", ad, []net.IP{net.ParseIP("192.168.1.50")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Serve(ctx) }()

	res, err := Browse(ctx, b.join("192.168.1.10"), "_rogertower._tcp", 300*time.Millisecond, 10)
	require.NoError(t, err)
	require.Len(t, res.Adverts, 1)
	got := res.Adverts[0]
	require.Equal(t, "n_tower1", got.NodeID)
	require.Equal(t, "garage", got.Name)
	require.Equal(t, "tower", got.Kind)
	require.Equal(t, 8787, got.Port)
	require.True(t, got.IP.Equal(net.ParseIP("192.168.1.50")))

	other, err := Browse(ctx, b.join("192.168.1.11"), "_rogerai._tcp", 200*time.Millisecond, 10)
	require.NoError(t, err)
	require.Empty(t, other.Adverts, "a responder answers only its own service")
}

// TestBrowseCeilingBoundsAFlood: past the ceiling, adverts are counted, never stored.
func TestBrowseCeilingBoundsAFlood(t *testing.T) {
	b := &bus{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 20; i++ {
		id := "n_" + string(rune('a'+i))
		r := NewResponder(b.join("192.168.1.60"), "_rogertower._tcp", Advert{NodeID: id, Kind: "tower", Port: 8787},
			[]net.IP{net.ParseIP("192.168.1.60")})
		go func() { _ = r.Serve(ctx) }()
	}
	res, err := Browse(ctx, b.join("192.168.1.10"), "_rogertower._tcp", 400*time.Millisecond, 5)
	require.NoError(t, err)
	require.Len(t, res.Adverts, 5)
	require.Equal(t, 20, res.Seen)
	require.Equal(t, 15, res.Dropped)
}

// TestAdvertHelpers covers the TXT authority key, address extraction and interface flattening.
func TestAdvertHelpers(t *testing.T) {
	txt := Advert{NodeID: "n", AuthPort: 39009}.TXT()
	require.Contains(t, txt, "auth=39009")
	for _, a := range []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.1")}, &net.IPAddr{IP: net.ParseIP("10.0.0.2")},
		&net.UDPAddr{IP: net.ParseIP("10.0.0.3")}} {
		require.NotNil(t, addrIP(a))
	}
	require.Nil(t, addrIP(&net.TCPAddr{IP: net.ParseIP("10.0.0.4")}))
	require.Empty(t, InterfaceAddrs([]net.Interface{{Name: "down0"}}), "a down interface contributes nothing")
}
