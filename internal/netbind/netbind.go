// Package netbind is the one place roger opens a listening socket (features/packaging/
// one_binary.feature section 7.4): the standalone plane's posture check, the Edge's
// per-interface wildcard, the loopback free-port listeners of the TUI and the web console, and
// the joined hub's stated posture. Locality is internal/netpriv's rule.
//
// A listener is bound ONCE and handed to the server that uses it. Nothing here probes a port,
// closes it and expects it to be free a moment later: another process can take it in between.
package netbind

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/netpriv"
)

// FreeWindow is how far above the asked port ListenFree and SuggestPort look.
const FreeWindow = 200

// Resolve validates the address a standalone Tower's consumer plane is asked to listen on and
// returns the address to bind plus a human note describing the exposure. The posture is the
// standalone spec's "refuses to masquerade on a public address":
//
//   - loopback or a private-LAN address: allowed, and the note states which.
//   - the UNSPECIFIED address (0.0.0.0 / ::), or any public IP: REFUSED, because an unbound
//     standalone Tower on a routable address is a broker lookalike an attacker can point a
//     victim's ROGER_BROKER at. It is allowed only with an explicit acknowledged override, and
//     even then the note says plainly what was opened.
//   - a hostname is refused rather than resolved: resolving it is a DNS lookup the airgap
//     posture forbids, and a name that resolves privately once can resolve publicly next.
//
// The caller supplies its own default for an empty bind (the plane's is localplane.DefaultBind).
func Resolve(addr string, allowPublic bool) (bind, note string, err error) {
	host, port, serr := net.SplitHostPort(addr)
	if serr != nil {
		return "", "", fmt.Errorf("--bind %q is not a valid host:port", addr)
	}
	// A numeric range check, not a service-name resolver: 0 asks the OS for an ephemeral port,
	// which tests and a dynamic bind rely on; only a non-number or a value outside 0..65535 is
	// refused.
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return "", "", fmt.Errorf("--bind %q has an invalid port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", "", fmt.Errorf("--bind host %q must be a literal IP (a hostname would need a DNS lookup the airgap posture forbids)", host)
	}
	switch netpriv.Classify(ip) {
	case netpriv.Loopback:
		return addr, "listening on loopback: reachable only from this host", nil
	case netpriv.Unspecified:
		if !allowPublic {
			return "", "", fmt.Errorf("--bind %q listens on ALL interfaces, which exposes this Tower as a public broker lookalike; pass --allow-public to acknowledge, or bind a specific loopback/LAN address", addr)
		}
		return addr, "WARNING: listening on ALL interfaces (--allow-public) - this Tower is reachable from every network it is attached to", nil
	case netpriv.Private, netpriv.ULA:
		return addr, "listening on a private-LAN address: reachable from the local network", nil
	}
	if !allowPublic {
		return "", "", fmt.Errorf("--bind %q is a PUBLIC address; a standalone Tower there is a broker lookalike for phishing ROGER_BROKER. Pass --allow-public to acknowledge, or bind a loopback/LAN address", addr)
	}
	return addr, "WARNING: listening on a PUBLIC address (--allow-public) - anyone who can reach it can try this Tower", nil
}

// Posture names where a bind host is reachable from: "loopback", "private" (RFC 1918, ULA or
// link-local) or "public" (everything else, including a wildcard, which listens on every
// interface). A host that is not an IP literal is "a name".
func Posture(host string) string {
	if host == "" {
		return "public"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "a name"
	}
	switch netpriv.Classify(ip) {
	case netpriv.Loopback:
		return "loopback"
	case netpriv.Private, netpriv.ULA, netpriv.LinkLocal:
		return "private"
	}
	return "public"
}

// Stated binds addr for a server that may be public by design (the joined hub) and returns the
// listener plus one line stating its posture for the log: "<role> listening on <addr>
// (<posture>)". A wildcard is stated as public, because it listens on every interface. A
// hostname is refused like at every other bind site: binding a name means a lookup whose
// answer can change.
func Stated(role, addr string) (net.Listener, string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("%s bind %q is not a valid host:port", role, addr)
	}
	posture := Posture(host)
	if posture == "a name" {
		return nil, "", fmt.Errorf("%s bind %q names a host; bind a literal address (or omit the host to listen on every interface)", role, addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		posture = "public: every interface"
	}
	return ln, fmt.Sprintf("%s listening on %s (%s)", role, ln.Addr(), posture), nil
}

// ListenFree binds addr ("host:port"), or, when that port is taken, the first free port above
// it on the same host (up to FreeWindow), and returns the open listener: one bind, no probe.
// Port 0, or no usable port, lets the OS choose. When the whole window is busy the OS chooses
// rather than dead-ending on "address in use".
func ListenFree(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		host, portStr = "127.0.0.1", "0"
	}
	start, perr := strconv.Atoi(portStr)
	if perr != nil || start <= 0 {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	for p := start; p < start+FreeWindow && p <= 65535; p++ {
		if ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p))); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

// SuggestPort answers a free loopback port at or above start (0 = any) for a process that
// will bind it LATER: onboarding saves it in the config, and a local model's runtime is handed
// it on its command line. It is a suggestion, never a bind this process holds; the process
// that binds it must cope with the port being taken meanwhile (a share binds through
// ListenFree; a model serve claims the port exclusively and tries another).
func SuggestPort(start int) (int, error) {
	ln, err := ListenFree(net.JoinHostPort("127.0.0.1", strconv.Itoa(start)))
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// RescanEvery is how often a wildcard listener from ListenEach rereads this machine's interface
// addresses. It is the multicast transport's rejoin period: an address that appears (a DHCP renewal,
// a cable moved) starts answering, and one that left stops, without restarting anything. Tests
// shorten it.
var RescanEvery = 10 * time.Second

// ListenEach opens a listener for bind. An explicit host is bound as given. A wildcard
// (":port") listens on each interface address that home accepts, all on one port, so a
// listener meant for this machine and its LAN never answers on a public interface. It keeps
// following the addresses: every RescanEvery it binds a qualifying address that appeared and
// closes one that left. Link-local addresses are skipped: they need a zone. addrs lists the
// interface addresses (nil = the system's); home decides which ones qualify (nil = netpriv.IsHome).
func ListenEach(bind string, addrs func() ([]net.Addr, error), home func(net.IP) bool) (net.Listener, error) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, err
	}
	if host != "" {
		return net.Listen("tcp", bind)
	}
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	if home == nil {
		home = netpriv.IsHome
	}
	m := &multiListener{port: port, addrs: addrs, home: home,
		lns: map[string]net.Listener{}, conns: make(chan net.Conn), done: make(chan struct{})}
	if err := m.rescan(); err != nil {
		return nil, err
	}
	if len(m.lns) == 0 {
		return nil, errors.New("no loopback or private interface could be bound on port " + port)
	}
	go m.follow()
	return m, nil
}

// Addrs lists every address a listener from ListenEach is bound to (one for a single listener).
func Addrs(ln net.Listener) []net.Addr {
	if m, ok := ln.(*multiListener); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := make([]net.Addr, 0, len(m.lns))
		for _, l := range m.lns {
			out = append(out, l.Addr())
		}
		return out
	}
	return []net.Addr{ln.Addr()}
}

// multiListener is one listener over several addresses on the same port, following the
// machine's addresses as they change.
type multiListener struct {
	mu    sync.Mutex
	port  string // "0" until the first address is bound; then every address shares it
	first net.Addr
	lns   map[string]net.Listener // keyed by the address's IP
	addrs func() ([]net.Addr, error)
	home  func(net.IP) bool
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// rescan binds every qualifying address not yet bound and closes every listener whose address
// left. An error reading the addresses changes nothing.
func (m *multiListener) rescan() error {
	list, err := m.addrs()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	default:
	}
	want := map[string]bool{}
	for _, a := range list {
		ipn, ok := a.(*net.IPNet)
		if !ok || netpriv.Classify(ipn.IP) == netpriv.LinkLocal || !m.home(ipn.IP) {
			continue
		}
		key := ipn.IP.String()
		want[key] = true
		if _, bound := m.lns[key]; bound {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(key, m.port))
		if err != nil {
			continue // that address cannot take the port: the others still serve
		}
		if m.first == nil {
			m.first = ln.Addr()
			m.port = strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) // every address shares the first port
		}
		m.lns[key] = ln
		go m.accept(ln)
	}
	for key, ln := range m.lns {
		if !want[key] {
			_ = ln.Close()
			delete(m.lns, key)
		}
	}
	return nil
}

func (m *multiListener) follow() {
	for {
		t := time.NewTimer(RescanEvery)
		select {
		case <-m.done:
			t.Stop()
			return
		case <-t.C:
			_ = m.rescan()
		}
	}
}

func (m *multiListener) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		select {
		case m.conns <- c:
		case <-m.done:
			_ = c.Close()
			return
		}
	}
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	m.once.Do(func() {
		m.mu.Lock()
		close(m.done)
		for key, ln := range m.lns {
			_ = ln.Close()
			delete(m.lns, key)
		}
		m.mu.Unlock()
	})
	return nil
}

// Addr is the first address bound; every address shares its port, so a caller that reads the
// port from it is right for all of them, even after that address itself has left.
func (m *multiListener) Addr() net.Addr { return m.first }
