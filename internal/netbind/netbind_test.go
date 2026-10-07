package netbind

// The standalone plane's exposure posture (features/tower/standalone_consumer_plane.feature):
// loopback or private by default, and no masquerading on a public or all-interfaces address
// without an explicit acknowledged override.

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePosture(t *testing.T) {
	cases := []struct {
		name        string
		addr        string
		allowPublic bool
		wantErr     string // substring; "" means allowed
		noteHas     string
	}{
		{"explicit loopback", "127.0.0.1:9000", false, "", "loopback"},
		{"ipv6 loopback", "[::1]:9000", false, "", "loopback"},
		{"private LAN", "192.168.1.10:9000", false, "", "private-LAN"},
		{"private 10.x", "10.2.3.4:8787", false, "", "private-LAN"},
		{"all interfaces refused", "0.0.0.0:8787", false, "ALL interfaces", ""},
		{"all interfaces with override", "0.0.0.0:8787", true, "", "ALL interfaces"},
		{"public refused", "203.0.113.5:8787", false, "PUBLIC", ""},
		{"public with override", "203.0.113.5:8787", true, "", "PUBLIC"},
		{"hostname refused", "roggentoo:8787", false, "literal IP", ""},
		{"garbage refused", "not-an-addr", false, "host:port", ""},
		{"bad port refused", "127.0.0.1:not-a-port", false, "invalid port", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bind, note, err := Resolve(tc.addr, tc.allowPublic)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.Empty(t, bind)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, bind)
			require.Contains(t, note, tc.noteHas)
		})
	}
}

// TestListenFreePortFallback: when the configured port is already taken, ListenFree
// auto-picks a higher free port instead of dead-ending (the TUI tune-in, the web console and `roger use` share this). It
// must never return the busy port.
func TestListenFreePortFallback(t *testing.T) {
	// Occupy a port, then ask ListenFree to start at that same port.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not occupy a port: %v", err)
	}
	defer busy.Close()
	_, busyPortStr, _ := net.SplitHostPort(busy.Addr().String())
	busyPort, _ := strconv.Atoi(busyPortStr)

	ln, err := ListenFree("127.0.0.1:" + busyPortStr)
	if err != nil {
		t.Fatalf("ListenFree should fall back to a free port, got error: %v", err)
	}
	defer ln.Close()
	_, gotPortStr, _ := net.SplitHostPort(ln.Addr().String())
	gotPort, _ := strconv.Atoi(gotPortStr)
	if gotPort == busyPort {
		t.Errorf("ListenFree returned the busy port %d (should have picked another)", busyPort)
	}
	if gotPort <= busyPort {
		t.Errorf("fallback port %d should be above the busy start %d", gotPort, busyPort)
	}
}

// TestListenFreePortBindsRequestedWhenFree: a free start port is used as-is.
func TestListenFreePortBindsRequestedWhenFree(t *testing.T) {
	// Find a definitely-free port, close it, then ask ListenFree to bind it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(probe.Addr().String())
	probe.Close()

	ln, err := ListenFree("127.0.0.1:" + portStr)
	if err != nil {
		t.Fatalf("ListenFree on a free port errored: %v", err)
	}
	defer ln.Close()
	_, gotStr, _ := net.SplitHostPort(ln.Addr().String())
	if gotStr != portStr {
		t.Errorf("free start port %s not used (got %s)", portStr, gotStr)
	}
}

// SuggestPort answers a free loopback port at or above start for a later process, and holds
// nothing: the port can be bound again straight away.
func TestSuggestPort(t *testing.T) {
	p, err := SuggestPort(40000)
	require.NoError(t, err)
	require.GreaterOrEqual(t, p, 40000)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
	require.NoError(t, err, "SuggestPort must not keep the port it suggests")
	_ = ln.Close()
}

func TestPosture(t *testing.T) {
	for host, want := range map[string]string{
		"":            "public",
		"0.0.0.0":     "public",
		"127.0.0.1":   "loopback",
		"::1":         "loopback",
		"192.168.1.9": "private",
		"fd00::1":     "private",
		"169.254.1.1": "private",
		"8.8.8.8":     "public",
		"100.64.0.1":  "public",
		"hub.lan":     "a name",
	} {
		require.Equal(t, want, Posture(host), host)
	}
}

func TestStated(t *testing.T) {
	ln, note, err := Stated("hub", "127.0.0.1:0")
	require.NoError(t, err)
	require.Contains(t, note, "hub listening on 127.0.0.1:")
	require.Contains(t, note, "(loopback)")
	_ = ln.Close()

	ln, note, err = Stated("hub", ":0")
	require.NoError(t, err)
	require.Contains(t, note, "(public: every interface)")
	_ = ln.Close()

	_, _, err = Stated("hub", "hub.example:8443")
	require.ErrorContains(t, err, "names a host")
	_, _, err = Stated("hub", "not-an-addr")
	require.ErrorContains(t, err, "host:port")

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	_, _, err = Stated("hub", busy.Addr().String())
	require.ErrorContains(t, err, "cannot listen")
}

func TestListenFreeEdges(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "not-an-addr", "127.0.0.1:nope"} {
		ln, err := ListenFree(addr)
		require.NoError(t, err, addr)
		require.True(t, ln.Addr().(*net.TCPAddr).IP.IsLoopback(), addr)
		require.NotZero(t, ln.Addr().(*net.TCPAddr).Port, addr)
		_ = ln.Close()
	}
}

func TestListenEach(t *testing.T) {
	// An explicit host is bound as given.
	ln, err := ListenEach("127.0.0.1:0", nil, nil)
	require.NoError(t, err)
	require.Len(t, Addrs(ln), 1)
	_ = ln.Close()

	// A wildcard skips link-local and anything home refuses; nothing qualifying is an error.
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("169.254.1.1"), Mask: net.CIDRMask(16, 32)},
			&net.IPNet{IP: net.ParseIP("8.8.8.8"), Mask: net.CIDRMask(8, 32)},
			&net.IPAddr{IP: net.ParseIP("127.0.0.1")},
		}, nil
	}
	_, err = ListenEach(":0", addrs, nil)
	require.ErrorContains(t, err, "no loopback or private interface")

	_, err = ListenEach("bad", nil, nil)
	require.Error(t, err)
	_, err = ListenEach(":0", func() ([]net.Addr, error) { return nil, net.ErrClosed }, nil)
	require.ErrorIs(t, err, net.ErrClosed)

	// A real multi-listener accepts on every address and closes cleanly.
	two := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("127.0.0.2"), Mask: net.CIDRMask(8, 32)},
		}, nil
	}
	m, err := ListenEach(":0", two, func(net.IP) bool { return true })
	require.NoError(t, err)
	all := Addrs(m)
	require.Len(t, all, 2)
	require.Equal(t, all[0].String(), m.Addr().String())
	for _, a := range all {
		go func(a string) {
			if c, err := net.Dial("tcp", a); err == nil {
				_ = c.Close()
			}
		}(a.String())
		c, err := m.Accept()
		require.NoError(t, err)
		_ = c.Close()
	}
	require.NoError(t, m.Close())
	require.NoError(t, m.Close(), "a second close is harmless")
	_, err = m.Accept()
	require.ErrorIs(t, err, net.ErrClosed)
}
