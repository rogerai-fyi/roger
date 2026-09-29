package edge_test

// Table-driven units for the node vocabulary: the parts of the spec that are pure rules
// rather than behavior over a store, plus the error paths the scenarios do not walk.

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestCapabilityVocabulary(t *testing.T) {
	for _, c := range edge.Capabilities() {
		require.True(t, c.Valid(), c)
		require.NotEmpty(t, c.VerificationMethod(), c)
	}
	require.False(t, edge.Capability("root").Valid())
	require.Empty(t, edge.Capability("root").VerificationMethod())
	require.Len(t, edge.Capabilities(), 7)
}

func TestKindBoundsWhatMayBeAsked(t *testing.T) {
	cases := []struct {
		kind     edge.Kind
		encoding string
		allowed  []edge.Capability
	}{
		{edge.Host, "json", edge.Capabilities()},
		{edge.Board, "compact", []edge.Capability{edge.Classify, edge.Sense, edge.Actuate}},
		{edge.Mobile, "json", []edge.Capability{edge.Serve, edge.Classify, edge.Sense, edge.Actuate, edge.Operate, edge.Maintain}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			require.True(t, tc.kind.Valid())
			require.Equal(t, tc.encoding, tc.kind.Encoding())
			want := map[edge.Capability]bool{}
			for _, c := range tc.allowed {
				want[c] = true
			}
			for _, c := range edge.Capabilities() {
				require.Equal(t, want[c], tc.kind.MayDeclare(c), "%s may declare %s", tc.kind, c)
			}
			require.False(t, tc.kind.MayDeclare("root"), "an unknown capability is never allowed")
		})
	}
	require.False(t, edge.Kind("toaster").Valid())
	require.Empty(t, edge.Kind("toaster").Encoding())
	require.False(t, edge.Kind("toaster").MayDeclare(edge.Sense))
	require.Len(t, edge.Kinds(), 3)
}

func TestValidName(t *testing.T) {
	long := strings.Repeat("a", edge.MaxNameLen)
	cases := map[string]bool{
		"bench-pi": true, "pi5-cabinet-2": true, "a": true, "UPPER": true,
		"box_1": true, "box.1": true, long: true,
		"": false, "bench pi": false, "../etc/passwd": false, long + "a": false,
		"-lead": false, ".lead": false, "a..b": false, "a/b": false, "a\tb": false,
		"a\nb": false, "café": false, "9lives": true,
	}
	for name, ok := range cases {
		err := edge.ValidName(name)
		if ok {
			require.NoError(t, err, "%q should be accepted", name)
		} else {
			require.Error(t, err, "%q should be rejected", name)
		}
	}
}

func TestNodeIDIsTheKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := edge.NodeID(pub)
	require.Equal(t, id, edge.NodeID(pub), "derivation is deterministic")
	require.True(t, strings.HasPrefix(id, "n_"))
	// It must fit in one DNS label, because it IS the mDNS instance label.
	require.LessOrEqual(t, len(id), 63)
	seen := map[string]bool{id: true}
	for i := 0; i < 200; i++ {
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		oid := edge.NodeID(other)
		require.False(t, seen[oid])
		seen[oid] = true
	}
}

func TestNewNodeStartsEveryCapabilityUnverified(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	n := edge.NewNode(pub, "box", edge.Host, edge.Serve, edge.Actuate)
	require.Equal(t, edge.NodeID(pub), n.ID)
	byName := map[string]store.EdgeCap{}
	for _, c := range n.Caps {
		byName[c.Name] = c
	}
	require.Equal(t, string(edge.Claimed), byName["serve"].State)
	require.Equal(t, string(edge.PendingConfirmation), byName["actuate"].State,
		"actuate has no probe that could ever promote it")
	require.False(t, edge.Routable(n, edge.Serve))
	require.False(t, edge.Routable(n, edge.Actuate))
	require.False(t, edge.Routable(n, edge.Sense))
	require.True(t, edge.Declares(n, edge.Serve))
	require.False(t, edge.Declares(n, edge.Sense))
}

func TestAdvertTXTRoundTripAndAddr(t *testing.T) {
	ad := edge.Advert{NodeID: "n_a", Account: "acct-1", Kind: "board",
		Caps: []string{"sense"}, Port: 443, Fingerprint: "AABB", IP: net.ParseIP("10.0.0.1")}
	txt := ad.TXT()
	require.Contains(t, txt, "id=n_a")
	require.Contains(t, txt, "acct=acct-1")
	require.Contains(t, txt, "caps=sense")
	require.Contains(t, txt, "port=443")
	require.Equal(t, "10.0.0.1:443", ad.Addr())

	// An unenrolled node advertises an EMPTY account, and no capability list.
	fresh := edge.Advert{NodeID: "n_b", Port: 1, Fingerprint: "cc"}
	require.Contains(t, fresh.TXT(), "acct=")
	require.Contains(t, fresh.TXT(), "caps=")
	require.Empty(t, edge.Advert{NodeID: "n_c"}.Addr())
	require.Empty(t, edge.Advert{NodeID: "n_c", IP: net.ParseIP("10.0.0.1")}.Addr())
}

// TestAdvertAuthPort: only an authority advertises where it answers claims (auth=), and a member or
// phone stays byte-identical to the six keys it always had (features/edge/claim.feature).
func TestAdvertAuthPort(t *testing.T) {
	// An authority carries auth=<port>, so a phone that was adopted knows where to claim.
	authority := edge.Advert{NodeID: "n_a", Account: "edge-local", Kind: "host",
		Port: 8791, Fingerprint: "aabb", AuthPort: 33537}
	require.Contains(t, authority.TXT(), "auth=33537")

	// A member (or a phone) with no authority port emits NO auth key - the record is the same six
	// keys as before, so nothing that reads the phone's advert is surprised.
	member := edge.Advert{NodeID: "n_b", Account: "edge-local", Kind: "mobile",
		Port: 9001, Fingerprint: "ccdd"}
	for _, kv := range member.TXT() {
		require.NotContains(t, kv, "auth=", "a non-authority must not advertise an auth port")
	}
	require.Len(t, member.TXT(), 6)

}

func TestAdvertiseAddrsAndHasLAN(t *testing.T) {
	mk := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		require.NoError(t, err)
		return &net.IPNet{IP: ip, Mask: n.Mask}
	}
	got := edge.AdvertiseAddrs([]net.Addr{
		mk("127.0.0.1/8"), mk("10.1.2.3/8"), mk("192.168.1.20/24"), mk("fd00::5/64"),
		mk("169.254.10.11/16"), mk("fe80::1/64"), mk("8.8.8.8/32"),
		&net.IPAddr{IP: net.ParseIP("172.16.0.9")},
		&net.UDPAddr{IP: net.ParseIP("172.17.0.9")},
		&net.TCPAddr{IP: net.ParseIP("192.168.9.9")}, // a shape we do not read
	})
	var names []string
	for _, ip := range got {
		names = append(names, ip.String())
	}
	require.ElementsMatch(t,
		[]string{"127.0.0.1", "10.1.2.3", "192.168.1.20", "fd00::5", "172.16.0.9", "172.17.0.9"},
		names)

	require.True(t, edge.HasLAN([]net.Addr{mk("192.168.1.20/24")}))
	require.False(t, edge.HasLAN([]net.Addr{mk("127.0.0.1/8")}), "loopback alone is not a LAN")
	require.False(t, edge.HasLAN([]net.Addr{mk("169.254.10.11/16")}))
	require.False(t, edge.HasLAN(nil))

	// A down interface contributes nothing.
	require.Empty(t, edge.InterfaceAddrs([]net.Interface{{Name: "eth0"}}))
	// A real enumeration must at least not blow up.
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	edge.InterfaceAddrs(ifaces)
}

func TestFingerprintHelpers(t *testing.T) {
	require.Len(t, edge.Fingerprint([]byte("x")), 64)
	require.Empty(t, edge.FingerprintOf(nil))
}

// An id is not a name. A machine enrolled without one carries its node id as its record name, and a
// 48-character id drawn as the title of a box is what made the EDGE screen unreadable.
func TestLabelNeverShowsAnIdAsAName(t *testing.T) {
	id := "n_ebab8e241f058e18234af760ad2a1ca976647af7583e0029"
	require.True(t, edge.LooksLikeID(id))
	require.Equal(t, "n_ebab8e24", edge.Label(id, id), "an unnamed machine reads as a SHORT id")
	require.Equal(t, "n_ebab8e24", edge.Label("", id))
	require.Equal(t, "RogGentooEdged", edge.Label("RogGentooEdged", id), "a name the owner chose wins")

	// The SHAPE decides, not equality with the record's id: a machine may legitimately be called
	// after itself, and a test rig often is.
	require.False(t, edge.LooksLikeID("workshop"))
	require.Equal(t, "workshop", edge.Label("workshop", "workshop"))
	require.False(t, edge.LooksLikeID("n_short"), "too short to be an id")
	require.False(t, edge.LooksLikeID("n_not-hex-at-all-really-no"), "an id is hex")
	require.False(t, edge.LooksLikeID("gentle-ibex-14"))
	require.Equal(t, "", edge.Label("", ""))
	require.Equal(t, "n_ebab8e24", edge.ShortID(id))
	require.Equal(t, "short", edge.ShortID("short"))
}

// One word per kind, shared by every surface: the TUI map, the Limoni screens and the console must
// not each invent their own (the map said PC where the rack said machine).
func TestOneWordPerKindForEverySurface(t *testing.T) {
	for kind, want := range map[string]string{
		string(edge.Host): "machine", string(edge.Mobile): "phone", string(edge.Board): "board",
		"weather-balloon": "device", "": "",
	} {
		require.Equal(t, want, edge.KindWord(kind), "kind %q", kind)
	}
}

// A board says what it is at length; a card has room for which board it is. The record keeps the
// model exactly as the machine reported it - this is display shortening and nothing routes on it.
func TestAHardwareModelIsShortenedForACardAndNeverHalfAWord(t *testing.T) {
	for in, want := range map[string]string{
		"Raspberry Pi 4 Model B Rev 1.5":        "Pi 4",
		"Raspberry Pi Zero 2 W Rev 1.0":         "Pi Zero 2 W",
		"Raspberry Pi 5 Model B Rev 1.0":        "Pi 5",
		"NVIDIA Jetson Orin Nano Developer Kit": "NVIDIA Jetson",
		"Dell Inc. XPS 13 9310":                 "Dell Inc. XPS",
		"  spaced   out  model  ":               "spaced out",
		"":                                      "",
		"Supercalifragilisticexpialidocious":    "Supercalifragi",
	} {
		if got := edge.MachineShort(in); got != want {
			t.Errorf("MachineShort(%q) = %q, want %q", in, got, want)
		}
	}
	// A model that is only "Model X" keeps it rather than shortening itself to nothing.
	if got := edge.MachineShort("Model B"); got != "Model B" {
		t.Errorf("MachineShort(\"Model B\") = %q", got)
	}
}

// The model comes from the node's own instances, and a node that has not said reports nothing
// rather than something invented.
func TestAHardwareModelComesFromTheNodesOwnReport(t *testing.T) {
	n := store.EdgeNode{Instances: []store.EdgeInstance{
		{Name: "a"},
		{Name: "b", Software: &store.EdgeSoftware{Machine: "Raspberry Pi 4 Model B Rev 1.5"}},
	}}
	if got := edge.MachineOf(n); got != "Raspberry Pi 4 Model B Rev 1.5" {
		t.Errorf("MachineOf = %q, want the model whole", got)
	}
	if got := edge.MachineOf(store.EdgeNode{}); got != "" {
		t.Errorf("a node that has not said reports %q", got)
	}
}
