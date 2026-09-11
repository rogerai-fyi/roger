package edge

// Unit tests for the hand-rolled mDNS codec and the multicast plane. They live INSIDE
// the package because the interesting surface is the decoder that reads bytes off an
// unauthenticated multicast group, and the interesting inputs are malformed.

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildAndParseResponseRoundTrip(t *testing.T) {
	ad := Advert{NodeID: "n_abc", Account: "acct-1", Kind: "host",
		Caps: []string{"sense", "actuate"}, Port: 9443, Fingerprint: "ff00"}
	pkt, err := buildResponse(DefaultService, ad.NodeID, ad.NodeID, uint16(ad.Port), ad.TXT(),
		[]net.IP{net.ParseIP("192.168.1.5"), net.ParseIP("fd00::5")})
	require.NoError(t, err)

	m, err := parseMessage(pkt)
	require.NoError(t, err)
	require.True(t, m.response)

	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.5"), Port: 5353}
	ads := advertsFrom(m, DefaultService, from)
	require.Len(t, ads, 1)
	require.Equal(t, "n_abc", ads[0].NodeID)
	require.Equal(t, "acct-1", ads[0].Account)
	require.Equal(t, "host", ads[0].Kind)
	require.Equal(t, []string{"sense", "actuate"}, ads[0].Caps)
	require.Equal(t, 9443, ads[0].Port)
	require.Equal(t, "ff00", ads[0].Fingerprint)
	require.Equal(t, "192.168.1.5:9443", ads[0].Addr())
}

func TestBuildQueryIsAPTRQuestionForTheService(t *testing.T) {
	q, err := buildQuery(DefaultService)
	require.NoError(t, err)
	m, err := parseMessage(q)
	require.NoError(t, err)
	require.False(t, m.response)
	require.Len(t, m.questions, 1)
	require.Equal(t, DefaultService+"."+mdnsDomain, m.questions[0].name)
	require.Equal(t, typePTR, m.questions[0].typ)

	r := &Responder{service: DefaultService}
	require.True(t, r.asksForUs(m))
	other := &Responder{service: "_printer._tcp"}
	require.False(t, other.asksForUs(m))
}

func TestEncoderRefusesUnusableNames(t *testing.T) {
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	_, err := appendName(nil, string(long))
	require.Error(t, err, "a label past 63 bytes must be refused")
	_, err = appendName(nil, "a..b")
	require.Error(t, err, "an empty label must be refused")

	// A name past 255 bytes.
	var name string
	for i := 0; i < 20; i++ {
		name += "abcdefghijabcdefghij."
	}
	_, err = appendName(nil, name+"end")
	require.Error(t, err)

	// A TXT character-string past 255 bytes.
	big := make([]byte, 300)
	_, err = buildResponse(DefaultService, "n_a", "n_a", 1, []string{string(big)}, nil)
	require.Error(t, err)

	// A bad instance label fails the whole build rather than emitting a broken packet.
	_, err = buildResponse(DefaultService, string(long), "n_a", 1, nil, nil)
	require.Error(t, err)
	_, err = buildResponse(DefaultService, "n_a", string(long), 1, nil, nil)
	require.Error(t, err)

	// A nil IP is skipped, not encoded as garbage.
	pkt, err := buildResponse(DefaultService, "n_a", "n_a", 1, nil, []net.IP{nil})
	require.NoError(t, err)
	m, err := parseMessage(pkt)
	require.NoError(t, err)
	for _, r := range m.answers {
		require.NotEqual(t, typeA, r.typ)
	}
}

// TestDecoderRefusesHostileMessages is the reason this codec is in-tree and fuzzed:
// every one of these arrives on a multicast group anybody on the LAN can write to.
func TestDecoderRefusesHostileMessages(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"header only cut":  {0, 1, 2},
		"counts past cap":  append(make([]byte, 0), 0, 0, 0x84, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0),
		"question cut off": {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c', 0},
		"name runs off":    {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 9, 'a'},
		"reserved label":   {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x40, 0},
		"truncated ptr":    {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseMessage(in)
			require.Error(t, err)
		})
	}
	// Too large outright.
	_, err := parseMessage(make([]byte, maxMsgSize+1))
	require.Error(t, err)
}

// TestDecodeNameRefusesPointerLoops is the classic mDNS denial of service: a
// compression pointer that points at itself, or forward, so a naive decoder spins.
func TestDecodeNameRefusesPointerLoops(t *testing.T) {
	// A pointer at offset 12 pointing to itself.
	self := make([]byte, 14)
	binary.BigEndian.PutUint16(self[12:], 0xc00c)
	_, _, err := decodeName(self, 12)
	require.Error(t, err)

	// Two pointers pointing at each other.
	pair := make([]byte, 20)
	binary.BigEndian.PutUint16(pair[12:], 0xc00e)
	binary.BigEndian.PutUint16(pair[14:], 0xc00c)
	_, _, err = decodeName(pair, 12)
	require.Error(t, err)

	// A pointer that jumps FORWARD is refused even though it terminates.
	fwd := make([]byte, 20)
	binary.BigEndian.PutUint16(fwd[12:], 0xc010)
	fwd[16] = 1
	fwd[17] = 'a'
	fwd[18] = 0
	_, _, err = decodeName(fwd, 12)
	require.Error(t, err)

	// Out of range offsets.
	_, _, err = decodeName([]byte{0}, 5)
	require.Error(t, err)
	_, _, err = decodeName([]byte{0}, -1)
	require.Error(t, err)

	// A legitimate BACKWARD pointer still works: other stacks compress, and refusing
	// them all would make us deaf to Avahi and Bonjour.
	buf := []byte{3, 'a', 'b', 'c', 0, 0, 0, 0}
	binary.BigEndian.PutUint16(buf[5:], 0xc000)
	name, next, err := decodeName(buf, 5)
	require.NoError(t, err)
	require.Equal(t, "abc", name)
	require.Equal(t, 7, next)
}

func TestDecodeRecordSkipsAndRefuses(t *testing.T) {
	// An unrecognised type is skipped, not refused: a LAN is full of other people's
	// records and none of them is an error.
	body, err := appendRR(nil, "x.local", 99, classIN, 60, []byte{1, 2, 3})
	require.NoError(t, err)
	head := make([]byte, 12)
	binary.BigEndian.PutUint16(head[2:], 0x8400)
	binary.BigEndian.PutUint16(head[6:], 1)
	m, err := parseMessage(append(head, body...))
	require.NoError(t, err)
	require.Empty(t, m.answers)

	// Malformed rdata of a type we DO read is refused.
	for _, bad := range []struct {
		typ  uint16
		data []byte
	}{
		{typeA, []byte{1, 2, 3}},
		{typeAAAA, []byte{1}},
		{typeSRV, []byte{0, 0}},
		{typeTXT, []byte{9, 'a'}},
	} {
		body, err := appendRR(nil, "x.local", bad.typ, classIN, 60, bad.data)
		require.NoError(t, err)
		_, err = parseMessage(append(head, body...))
		require.Error(t, err, "type %d", bad.typ)
	}
}

func TestAdvertsFromIgnoresIncompleteRecordSets(t *testing.T) {
	head := make([]byte, 12)
	binary.BigEndian.PutUint16(head[2:], 0x8400)

	// PTR with no SRV: nowhere to dial, so nothing is kept.
	ptr, err := appendName(nil, "n_x."+DefaultService+"."+mdnsDomain)
	require.NoError(t, err)
	body, err := appendRR(nil, DefaultService+"."+mdnsDomain, typePTR, classIN, 60, ptr)
	require.NoError(t, err)
	binary.BigEndian.PutUint16(head[6:], 1)
	m, err := parseMessage(append(head, body...))
	require.NoError(t, err)
	require.Empty(t, advertsFrom(m, DefaultService, nil))

	// A complete set but no fingerprint: unverifiable, so it is not a lead.
	pkt, err := buildResponse(DefaultService, "n_y", "n_y", 80,
		[]string{"id=n_y", "port=80"}, []net.IP{net.ParseIP("10.0.0.2")})
	require.NoError(t, err)
	m, err = parseMessage(pkt)
	require.NoError(t, err)
	require.Empty(t, advertsFrom(m, DefaultService, nil))

	// A PTR for somebody ELSE's service is not ours.
	pkt, err = buildResponse("_printer._tcp", "n_z", "n_z", 80,
		Advert{NodeID: "n_z", Fingerprint: "aa", Port: 80}.TXT(), []net.IP{net.ParseIP("10.0.0.3")})
	require.NoError(t, err)
	m, err = parseMessage(pkt)
	require.NoError(t, err)
	require.Empty(t, advertsFrom(m, DefaultService, nil))

	// No A record: the packet's source address is the fallback, and it is still only
	// a hint - the certificate check is unchanged either way.
	pkt, err = buildResponse(DefaultService, "n_w", "n_w", 81,
		Advert{NodeID: "n_w", Fingerprint: "bb", Port: 81}.TXT(), nil)
	require.NoError(t, err)
	m, err = parseMessage(pkt)
	require.NoError(t, err)
	got := advertsFrom(m, DefaultService, &net.UDPAddr{IP: net.ParseIP("10.0.0.9")})
	require.Len(t, got, 1)
	require.Equal(t, "10.0.0.9:81", got[0].Addr())
	// And with no source either, there is nowhere to dial.
	require.Empty(t, advertsFrom(m, DefaultService, nil))
}

func TestEqualNameAndTrimRoot(t *testing.T) {
	require.True(t, equalName("_RogerAI._TCP.local.", "_rogerai._tcp.local"))
	require.False(t, equalName("a.local", "b.local"))
	require.False(t, equalName("a.local", "aa.local"))
	require.Equal(t, "a.b", trimRoot("a.b.."))
	require.Equal(t, "", trimRoot("..."))
}

// FuzzParseMessage keeps the decoder honest against inputs nobody thought of. The
// contract is only that it never panics and never spins: any byte string is either a
// message or an error.
func FuzzParseMessage(f *testing.F) {
	pkt, _ := buildResponse(DefaultService, "n_a", "n_a", 1,
		Advert{NodeID: "n_a", Fingerprint: "aa", Port: 1}.TXT(), []net.IP{net.ParseIP("10.0.0.1")})
	f.Add(pkt)
	q, _ := buildQuery(DefaultService)
	f.Add(q)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		m, err := parseMessage(in)
		if err != nil {
			return
		}
		advertsFrom(m, DefaultService, &net.UDPAddr{IP: net.ParseIP("10.0.0.1")})
	})
}

// --- the multicast plane --------------------------------------------------

// TestMulticastTransportMovesRealPackets drives the real transport methods over real
// UDP sockets. The group join is the one thing a loopback test cannot do (Linux `lo`
// carries no MULTICAST flag); everything else here is the production code path.
func TestMulticastTransportMovesRealPackets(t *testing.T) {
	dst, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer dst.Close()
	src, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	tp := &multicastTransport{
		conns: []*net.UDPConn{src},
		group: dst.LocalAddr().(*net.UDPAddr),
		recv:  make(chan packet, 4),
		done:  make(chan struct{}),
	}
	go tp.pump(src)

	require.NoError(t, tp.Send([]byte("hello")))
	buf := make([]byte, 64)
	n, _, err := dst.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "hello", string(buf[:n]))

	// A packet arriving at the transport's own socket comes back out of Recv.
	_, err = dst.WriteTo([]byte("pong"), src.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	require.NoError(t, tp.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, from, err := tp.Recv(buf)
	require.NoError(t, err)
	require.Equal(t, "pong", string(buf[:n]))
	require.NotNil(t, from)

	// An expired deadline is a timeout, not a failure.
	require.NoError(t, tp.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	_, _, err = tp.Recv(buf)
	require.Error(t, err)
	require.True(t, isTimeout(err))
	require.True(t, timeoutError{}.Temporary())
	require.Equal(t, "i/o timeout", timeoutError{}.Error())

	require.NoError(t, tp.Close())
	require.NoError(t, tp.Close(), "closing twice must not panic on the closed channel")
	require.NoError(t, tp.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err = tp.Recv(buf)
	require.Error(t, err)
	require.False(t, isTimeout(err), "a closed transport is closed, not slow")
	require.Error(t, tp.Send([]byte("after close")))
}

func TestNewMulticastTransportNeedsALAN(t *testing.T) {
	// Down, loopback, and non-multicast interfaces are all skipped, so a machine with
	// only those has no LAN and is told so instead of browsing nothing forever.
	_, err := NewMulticastTransport([]net.Interface{
		{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		{Index: 2, Name: "eth0", Flags: net.FlagMulticast}, // down
		{Index: 3, Name: "tun0", Flags: net.FlagUp},        // no multicast
	})
	require.ErrorIs(t, err, ErrNoLAN)
	_, err = NewMulticastTransport(nil)
	require.ErrorIs(t, err, ErrNoLAN)
}

// TestNewMulticastTransportJoinsARealGroup runs the success path when this host
// actually has a private, multicast-capable interface. It joins and closes without
// sending anything, so it puts no traffic on the network.
func TestNewMulticastTransportJoinsARealGroup(t *testing.T) {
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	usable := false
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 &&
			HasLAN(InterfaceAddrs([]net.Interface{ifi})) {
			usable = true
		}
	}
	if !usable {
		t.Skip("no private multicast-capable interface on this host")
	}
	tp, err := NewMulticastTransport(ifaces)
	if err != nil {
		t.Skipf("the group would not join here: %v", err)
	}
	require.NoError(t, tp.Close())
}
