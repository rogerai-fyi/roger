package main

// Executable spec: features/edge/cli.feature - "roger edge is one command tree over the
// owner's fleet, whichever path reaches a node".
//
// REAL dependencies, no mocks of our own code:
//   - the REAL command dispatch (dispatch(), the same one main() calls) and the REAL
//     exit-code mapping, so an "exits 2" step is the process's answer, not a stand-in.
//   - the REAL internal/edge Fleet over the REAL internal/store Mem backend, persisted
//     through the REAL on-disk Edge state file the CLI reads on every run.
//   - REAL TCP listeners on 127.0.0.1 for reachability: a "reachable" transport is a
//     listener that is up, and an "unreachable" one is a port nothing is bound to. The
//     LAN-vs-relay preference is therefore exercised by real connections refusing.
//   - REAL mDNS: a real Responder and a real Browse exchanging real DNS wire bytes over
//     real UDP sockets, and REAL TLS listeners with REAL certificates from the REAL
//     towercore/cert authority for the scan scenarios. The only substitution is the
//     multicast GROUP (a loopback packet bus), because Linux `lo` carries no MULTICAST
//     flag - the same substitution features/edge/discovery.feature already makes.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// --- the loopback packet bus (a multicast group made of real UDP sockets) ---

type edgeBus struct {
	mu      sync.Mutex
	members []*edgeBusConn
}

func (b *edgeBus) join(t *testing.T) *edgeBusConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bus join: %v", err)
	}
	c := &edgeBusConn{pc: pc, bus: b}
	b.mu.Lock()
	b.members = append(b.members, c)
	b.mu.Unlock()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type edgeBusConn struct {
	pc  net.PacketConn
	bus *edgeBus
}

func (c *edgeBusConn) Send(p []byte) error {
	c.bus.mu.Lock()
	peers := append([]*edgeBusConn(nil), c.bus.members...)
	c.bus.mu.Unlock()
	for _, m := range peers {
		if m == c {
			continue
		}
		if _, err := c.pc.WriteTo(p, m.pc.LocalAddr()); err != nil {
			return err
		}
	}
	return nil
}

func (c *edgeBusConn) Recv(b []byte) (int, net.Addr, error) { return c.pc.ReadFrom(b) }
func (c *edgeBusConn) SetReadDeadline(t time.Time) error    { return c.pc.SetReadDeadline(t) }
func (c *edgeBusConn) Close() error                         { return c.pc.Close() }

// --- a peer: a real key, a real certificate, a real TLS listener, a real responder ---

type edgePeer struct {
	id     string
	ts     *httptest.Server
	advert edge.Advert
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *edgePeer) stop() {
	if p.cancel != nil {
		p.cancel()
		p.wg.Wait()
		p.cancel = nil
	}
	if p.ts != nil {
		p.ts.Close()
		p.ts = nil
	}
}

// --- scenario state -------------------------------------------------------

type edgeCLIBDD struct {
	t   *testing.T
	dir string

	ca    *cert.Authority
	bus   *edgeBus
	peers []*edgePeer

	// live listeners standing in for reachable transports.
	listeners []net.Listener

	// the last command's result.
	out  string
	err  error
	code int
	// prior runs kept for the "same shape" / "changes nothing" comparisons.
	prevOut string

	candidate string // the name/id the "<candidate>" placeholder resolves to
	prefix    string // the id prefix under test
	stdin     string // what the confirmation prompt reads
}

func (s *edgeCLIBDD) reset(t *testing.T) {
	for _, p := range s.peers {
		p.stop()
	}
	s.peers = nil
	for _, l := range s.listeners {
		_ = l.Close()
	}
	s.listeners = nil
	s.t = t
	s.bus = &edgeBus{}
	s.out, s.err, s.code, s.prevOut = "", nil, 0, ""
	s.candidate, s.prefix, s.stdin = "", "", ""
	s.dir = useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1") // no relay unless a node names one
	var err error
	if s.ca, err = cert.NewAuthority(cert.Config{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	// The suite drives loopback addresses only: a refused connection is instant, so a
	// short bound keeps a 34-scenario suite honest AND quick.
	edgeDialTimeout = 300 * time.Millisecond
	edgeStdin = strings.NewReader("")
	edgeDiscoveryOptions = defaultEdgeDiscoveryOptions
}

// --- fixtures -------------------------------------------------------------

// login writes the real auth record the CLI reads (client.LinkedLogin), so "logged in"
// is the production code path and not a flag this test invents.
func (s *edgeCLIBDD) login(who string) {
	s.t.Helper()
	dir := filepath.Dir(configPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"github_login": who, "github_id": 42, "bound_at": time.Now().Unix()})
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// listener stands a real TCP listener up and returns its address: a reachable transport.
func (s *edgeCLIBDD) listener() string {
	s.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.listeners = append(s.listeners, l)
	return l.Addr().String()
}

// deadAddr returns an address nothing is listening on: an unreachable transport.
func (s *edgeCLIBDD) deadAddr() string {
	s.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// enroll puts a node on the owner's Edge through the REAL fleet and the REAL state file.
func (s *edgeCLIBDD) enroll(n store.EdgeNode) store.EdgeNode {
	s.t.Helper()
	st, err := loadEdgeState()
	if err != nil {
		s.t.Fatal(err)
	}
	got, err := st.fleet.Enroll(n)
	if err != nil {
		s.t.Fatalf("enroll %s: %v", n.Name, err)
	}
	if err := st.save(); err != nil {
		s.t.Fatal(err)
	}
	return got
}

func (s *edgeCLIBDD) addCandidate(c store.EdgeNode) {
	s.t.Helper()
	st, err := loadEdgeState()
	if err != nil {
		s.t.Fatal(err)
	}
	st.candidates = append(st.candidates, c)
	if err := st.save(); err != nil {
		s.t.Fatal(err)
	}
}

func (s *edgeCLIBDD) fleetNow() []store.EdgeNode {
	s.t.Helper()
	st, err := loadEdgeState()
	if err != nil {
		s.t.Fatal(err)
	}
	list, err := st.fleet.List()
	if err != nil {
		s.t.Fatal(err)
	}
	return list
}

func (s *edgeCLIBDD) node(name string) (store.EdgeNode, bool) {
	for _, n := range s.fleetNow() {
		if n.Name == name {
			return n, true
		}
	}
	return store.EdgeNode{}, false
}

// newPeer stands a real node up on the bus: a real key, a real certificate from the
// account authority (unless one is supplied), a real TLS describe listener, and a real
// mDNS responder. advertFP overrides the advertised fingerprint, which is how a liar is
// built out of real parts.
func (s *edgeCLIBDD) newPeer(account string, caps []string, advertFP string) *edgePeer {
	s.t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	leaf, err := s.ca.Issue(id, pub)
	if err != nil {
		s.t.Fatal(err)
	}
	srv := edge.NewServer(edge.Describe{NodeID: id, Account: account, Kind: "host", Caps: caps},
		tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = srv.TLSConfig()
	ts.StartTLS()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "https://"))
	port, _ := strconv.Atoi(portStr)
	p := &edgePeer{id: id, ts: ts, advert: srv.Advert(port)}
	p.advert.IP = net.ParseIP(host)
	if advertFP != "" {
		p.advert.Fingerprint = advertFP
	}
	conn := s.bus.join(s.t)
	resp := edge.NewResponder(conn, edge.DefaultService, p.advert, []net.IP{net.ParseIP("127.0.0.1")})
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(1)
	go func() { defer p.wg.Done(); _ = resp.Serve(ctx) }()
	s.peers = append(s.peers, p)
	return p
}

// useBus points the scan seam at the loopback group and the real account authority.
func (s *edgeCLIBDD) useBus() {
	bus := s.bus
	ca := s.ca
	t := s.t
	edgeDiscoveryOptions = func(f *edge.Fleet) edge.Options {
		o := defaultEdgeDiscoveryOptions(f)
		o.Authority = ca
		o.Config.Window = 700 * time.Millisecond
		o.Plane = func() (edge.Transport, error) { return bus.join(t), nil }
		o.AdvertiseIPs = []net.IP{net.ParseIP("127.0.0.1")}
		return o
	}
}

// --- running the real CLI -------------------------------------------------

func (s *edgeCLIBDD) run(line string) error {
	s.prevOut = s.out
	line = strings.ReplaceAll(line, "<candidate>", s.candidate)
	args := strings.Fields(line)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("a command line starts with `roger`, got %q", line)
	}
	edgeStdin = strings.NewReader(s.stdin)
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err, s.code = out, err, exitCode(err)
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

// captureEdgeStdout runs fn with os.Stdout redirected and returns what it printed.
func captureEdgeStdout(fn func() error) (string, error) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	err := fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done, err
}

func (s *edgeCLIBDD) has(sub string) error {
	if !strings.Contains(strings.ToLower(s.out), strings.ToLower(sub)) {
		return fmt.Errorf("output does not mention %q:\n%s", sub, s.out)
	}
	return nil
}

func (s *edgeCLIBDD) hasNot(sub string) error {
	if strings.Contains(strings.ToLower(s.out), strings.ToLower(sub)) {
		return fmt.Errorf("output should not mention %q:\n%s", sub, s.out)
	}
	return nil
}

// --- Given ----------------------------------------------------------------

// aLoggedInOwnerWithAnEdge is the ordinary starting point: a logged-in owner, two nodes
// of different kinds both currently reachable (one LAN-direct, one through a relay), and
// the LAN they live on - a truthful peer advertising for this account and a peer whose
// advertisement lies about its certificate.
//
// The LAN half is in the Background because the spec's scan scenario carries no Given of
// its own: "an Edge" is the fleet AND the network it sits on.
func (s *edgeCLIBDD) aLoggedInOwnerWithAnEdge() error {
	s.login("owner")
	s.enroll(store.EdgeNode{
		ID: "n_workshop0000000000000000000000000000000000000aa", Name: "workshop-pi", Kind: "board",
		Caps:       []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "aa11"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Add(-30 * time.Second).Unix(),
	})
	s.enroll(store.EdgeNode{
		ID: "n_cabinet00000000000000000000000000000000000000bb", Name: "cabinet-jetson", Kind: "host",
		Caps:       []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "relay", Addr: s.listener()}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Add(-2 * time.Minute).Unix(),
	})
	s.enroll(store.EdgeNode{
		ID: "n_shed00000000000000000000000000000000000000000cc", Name: "shed-pi", Kind: "board",
		Caps:       []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.deadAddr(), Fingerprint: "cc33"}},
		Presence:   string(edge.PresenceDark), LastSeen: time.Now().Add(-3 * time.Hour).Unix(),
	})
	s.candidate = "n_seen00000000000000000000000000000000000000000dd"
	s.addCandidate(store.EdgeNode{
		ID: s.candidate, Name: s.candidate, Kind: "host", Pin: "dd44",
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Add(-20 * time.Second).Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "dd44"}},
	})
	s.useBus()
	s.newPeer("owner", []string{"serve"}, "")                      // truthful: verified
	s.newPeer("owner", []string{"serve"}, strings.Repeat("7", 64)) // lies about its certificate
	return nil
}

// anEdgeWithOnlyThisMachine empties the fleet: the owner has joined nothing yet.
func (s *edgeCLIBDD) anEdgeWithOnlyThisMachine() error {
	return os.Remove(edgeStatePath())
}

func (s *edgeCLIBDD) nodesOfSeveralKindsAndCapabilities() error {
	s.enroll(store.EdgeNode{
		ID: "n_sensor000000000000000000000000000000000000000cc", Name: "attic-sensor", Kind: "board",
		Caps:       []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "cc33"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Add(-5 * time.Second).Unix(),
	})
	return nil
}

func (s *edgeCLIBDD) aNodeClaimingWhoseProbeFailed(capName string) error {
	s.enroll(store.EdgeNode{
		ID: "n_claim0000000000000000000000000000000000000000dd", Name: "unproven-box", Kind: "host",
		Caps:       []store.EdgeCap{{Name: capName, State: string(edge.Claimed)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "dd44"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Add(-9 * time.Second).Unix(),
	})
	return nil
}

func (s *edgeCLIBDD) aNodeWhoseHeartbeatAgedOut() error {
	s.enroll(store.EdgeNode{
		ID: "n_dark0000000000000000000000000000000000000000ee", Name: "loft-pi", Kind: "board",
		Caps:       []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.deadAddr(), Fingerprint: "ee55"}},
		Presence:   string(edge.PresenceDark), LastSeen: time.Now().Add(-3 * time.Hour).Unix(),
	})
	return nil
}

func (s *edgeCLIBDD) aDiscoveredUnenrolledPeer() error {
	s.candidate = "n_cand0000000000000000000000000000000000000000ff"
	s.addCandidate(store.EdgeNode{
		ID: s.candidate, Name: s.candidate, Kind: "host",
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Add(-10 * time.Second).Unix(),
		Pin:        "ff66",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "ff66"}},
	})
	return nil
}

func (s *edgeCLIBDD) aNodeNamed(name string) error {
	s.enroll(store.EdgeNode{
		ID: "n_named000000000000000000000000000000000000000011", Name: name, Kind: "board",
		Caps:       []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "1177"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Add(-8 * time.Second).Unix(),
	})
	return nil
}

func (s *edgeCLIBDD) twoNodesWhoseIdsShareAPrefix() error {
	s.prefix = "n_twins"
	for i, name := range []string{"twin-one", "twin-two"} {
		s.enroll(store.EdgeNode{
			ID:   fmt.Sprintf("n_twins%d000000000000000000000000000000000000000%d", i, i),
			Name: name, Kind: "host",
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "22aa"}},
			Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
		})
	}
	return nil
}

func (s *edgeCLIBDD) aNodeInTheFleet() error { return s.aNodeNamed("bench-pi") }

// aDiscoveredCandidate is a candidate whose advertised fingerprint really is the
// certificate it serves: a real TLS listener, a real certificate, a real hash.
func (s *edgeCLIBDD) aDiscoveredCandidate() error {
	p := s.newPeer("", []string{"serve"}, "")
	s.candidate = p.id
	s.addCandidate(store.EdgeNode{
		ID: p.id, Name: p.id, Kind: "host", Pin: p.advert.Fingerprint,
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: p.advert.Addr(), Fingerprint: p.advert.Fingerprint}},
	})
	return nil
}

// aCandidateWhoseFingerprintDoesNotMatch advertises one fingerprint and serves another.
func (s *edgeCLIBDD) aCandidateWhoseFingerprintDoesNotMatch() error {
	p := s.newPeer("", []string{"serve"}, strings.Repeat("9", 64))
	s.candidate = p.id
	s.addCandidate(store.EdgeNode{
		ID: p.id, Name: p.id, Kind: "host", Pin: p.advert.Fingerprint,
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: p.advert.Addr(), Fingerprint: p.advert.Fingerprint}},
	})
	return nil
}

func (s *edgeCLIBDD) aNodeLANDirectAndANodeRelayOnly() error {
	s.enroll(store.EdgeNode{
		ID: "n_lan00000000000000000000000000000000000000000033", Name: "lan-box", Kind: "host",
		Caps:       []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "3388"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
	})
	s.enroll(store.EdgeNode{
		ID: "n_relay00000000000000000000000000000000000000000044", Name: "far-box", Kind: "host",
		Caps:       []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{{Kind: "relay", Addr: s.listener()}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
	})
	return nil
}

func (s *edgeCLIBDD) aNodeReachableBothWaysWhoseLANStopped() error {
	s.enroll(store.EdgeNode{
		ID: "n_both000000000000000000000000000000000000000000055", Name: "both-ways", Kind: "host",
		Caps: []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}},
		Transports: []store.EdgeTransport{
			{Kind: "lan", Addr: s.deadAddr(), Fingerprint: "5599"},
			{Kind: "relay", Addr: s.listener()},
		},
		Presence: string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
	})
	return nil
}

// noNetworkAndNoRelay closes every listener the fixtures stood up: nothing on this
// machine can reach anything, which is exactly what "no network" means to the command.
func (s *edgeCLIBDD) noNetworkAndNoRelay() error {
	for _, l := range s.listeners {
		_ = l.Close()
	}
	s.listeners = nil
	return nil
}

func (s *edgeCLIBDD) anOwnerWhoIsNotLoggedIn() error {
	if err := os.Remove(filepath.Join(filepath.Dir(configPath()), "auth.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// A LAN peer that belongs to nobody yet: a candidate, found without any login.
	s.useBus()
	p := s.newPeer("", []string{"sense"}, "")
	s.candidate = p.id
	return os.Remove(edgeStatePath())
}

// --- When -----------------------------------------------------------------

func (s *edgeCLIBDD) describesThatPrefix() error { return s.run("roger edge describe " + s.prefix) }

func (s *edgeCLIBDD) describesAnIDOfAnotherAccount() error {
	// The node EXISTS - on somebody else's Edge. It is written into the very state file
	// this machine reads, stamped with the other account, so the refusal is the fleet's
	// account scoping doing its job and not merely a missing row.
	const id = "n_theirs000000000000000000000000000000000000000066"
	b, err := os.ReadFile(edgeStatePath())
	if err != nil {
		return err
	}
	var snap map[string]any
	if err := json.Unmarshal(b, &snap); err != nil {
		return err
	}
	nodes, _ := snap["nodes"].([]any)
	snap["nodes"] = append(nodes, map[string]any{
		"id": id, "account": "somebody-else", "name": "their-box", "kind": "host",
	})
	nb, _ := json.Marshal(snap)
	if err := os.WriteFile(edgeStatePath(), nb, 0o600); err != nil {
		return err
	}
	return s.run("roger edge describe " + id)
}

func (s *edgeCLIBDD) namesANodeTwice(name string) error {
	if err := s.run("roger edge name workshop-pi " + name); err != nil {
		return err
	}
	if s.code != 0 {
		return fmt.Errorf("the first naming failed (%d):\n%s", s.code, s.out)
	}
	return s.run("roger edge name " + name + " " + name)
}

func (s *edgeCLIBDD) namesADifferentNode(name string) error {
	s.enroll(store.EdgeNode{
		ID: "n_other000000000000000000000000000000000000000077", Name: "other-box", Kind: "host",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: s.listener(), Fingerprint: "77bb"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
	})
	return s.run("roger edge name other-box " + name)
}

func (s *edgeCLIBDD) forgetsANameNotOnThisEdge() error {
	return s.run("roger edge forget no-such-node --yes")
}

func (s *edgeCLIBDD) triesToAdoptIt() error { return s.run("roger edge adopt " + s.candidate) }

func (s *edgeCLIBDD) describesEachOfThem() error {
	if err := s.run("roger edge describe lan-box"); err != nil {
		return err
	}
	lan := s.out
	if err := s.run("roger edge describe far-box"); err != nil {
		return err
	}
	s.prevOut = lan
	return nil
}

func (s *edgeCLIBDD) describesIt() error { return s.run("roger edge describe both-ways") }

// --- Then -----------------------------------------------------------------

func (s *edgeCLIBDD) exits(code int) error {
	if s.code != code {
		return fmt.Errorf("exit %d, want %d:\n%s", s.code, code, s.out)
	}
	return nil
}

func (s *edgeCLIBDD) isListedAsACommand(name string) error {
	if err := s.exits(0); err != nil {
		return err
	}
	return s.has("roger " + name)
}

func (s *edgeCLIBDD) descriptionNamesTheFleet() error {
	for _, line := range strings.Split(s.out, "\n") {
		if strings.Contains(line, "roger edge") && strings.Contains(strings.ToLower(line), "fleet") {
			return nil
		}
	}
	return fmt.Errorf("no one-line `roger edge` description naming the fleet:\n%s", s.out)
}

func (s *edgeCLIBDD) printsTheFleet() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if err := s.has("workshop-pi"); err != nil {
		return err
	}
	return s.has("cabinet-jetson")
}

func (s *edgeCLIBDD) noUsageError() error {
	if err := s.hasNot("usage:"); err != nil {
		return err
	}
	return s.hasNot("takes no arguments")
}

func (s *edgeCLIBDD) itDescribes(purpose string) error {
	if err := s.exits(0); err != nil {
		return err
	}
	return s.has(purpose)
}

func (s *edgeCLIBDD) namesTheValidSubcommands() error {
	for _, c := range []string{"list", "describe", "name", "forget", "adopt", "scan"} {
		if err := s.has(c); err != nil {
			return err
		}
	}
	return nil
}

func (s *edgeCLIBDD) saysItTakesNoArguments() error { return s.has("takes no arguments") }

func (s *edgeCLIBDD) saysThisMachineIsTheOnlyNode() error {
	return s.has("this machine is the only node")
}

func (s *edgeCLIBDD) namesTheActionThatAdds() error {
	if err := s.has("roger edge scan"); err != nil {
		return err
	}
	return s.has("roger edge adopt")
}

// eachRowCarriesTheColumns checks the fleet listing carries, for a real node, every
// column the spec names - and that the transport column is the one actually reaching it.
func (s *edgeCLIBDD) eachRowCarriesTheColumns() error {
	if err := s.exits(0); err != nil {
		return err
	}
	for _, want := range []string{"name", "kind", "capabilities", "transport", "last seen"} {
		if err := s.has(want); err != nil {
			return err
		}
	}
	row := s.rowFor("attic-sensor")
	if row == "" {
		return fmt.Errorf("no row for attic-sensor:\n%s", s.out)
	}
	for _, want := range []string{"board", "sense", "lan"} {
		if !strings.Contains(row, want) {
			return fmt.Errorf("row %q is missing %q", row, want)
		}
	}
	if !strings.Contains(row, "ago") {
		return fmt.Errorf("row %q does not say how long ago it was seen", row)
	}
	// The relay-only node's row says relay, not lan: the column is the transport IN USE.
	if r := s.rowFor("cabinet-jetson"); !strings.Contains(r, "relay") {
		return fmt.Errorf("the relay-reached node's row does not say relay: %q", r)
	}
	return nil
}

func (s *edgeCLIBDD) rowFor(name string) string {
	for _, line := range strings.Split(s.out, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

func (s *edgeCLIBDD) claimedIsMarkedDistinctly() error {
	if err := s.exits(0); err != nil {
		return err
	}
	claimed := s.rowFor("unproven-box")
	if !strings.Contains(claimed, "claimed") {
		return fmt.Errorf("the claimed capability is not marked claimed: %q", claimed)
	}
	verified := s.rowFor("workshop-pi")
	if strings.Contains(verified, "claimed") {
		return fmt.Errorf("a verified capability is marked claimed too: %q", verified)
	}
	return nil
}

func (s *edgeCLIBDD) darkIsListedWithItsAge() error {
	if err := s.exits(0); err != nil {
		return err
	}
	row := s.rowFor("loft-pi")
	if row == "" {
		return fmt.Errorf("the dark node was hidden:\n%s", s.out)
	}
	if !strings.Contains(strings.ToLower(row), "dark") || !strings.Contains(row, "ago") {
		return fmt.Errorf("the dark row does not say dark with an age: %q", row)
	}
	return nil
}

func (s *edgeCLIBDD) appearsUnderCandidatesNotTheFleet() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if err := s.has("candidate"); err != nil {
		return err
	}
	ci := strings.Index(strings.ToLower(s.out), "candidate")
	ni := strings.Index(s.out, s.candidate)
	if ni < 0 || ni < ci {
		return fmt.Errorf("the candidate is not under the candidates heading:\n%s", s.out)
	}
	for _, n := range s.fleetNow() {
		if n.ID == s.candidate {
			return fmt.Errorf("the candidate was silently made a member")
		}
	}
	return nil
}

func (s *edgeCLIBDD) saysHowToAdoptIt() error { return s.has("roger edge adopt") }

func (s *edgeCLIBDD) outputIsAJSONArray() error {
	if err := s.exits(0); err != nil {
		return err
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.out)), &arr); err != nil {
		return fmt.Errorf("not a JSON array (%v):\n%s", err, s.out)
	}
	if len(arr) == 0 {
		return fmt.Errorf("the array is empty; the fleet is not")
	}
	return nil
}

func (s *edgeCLIBDD) eachElementCarriesTheFields() error {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.out)), &arr); err != nil {
		return err
	}
	for _, el := range arr {
		for _, k := range []string{"id", "name", "kind", "capabilities", "transports", "last_seen", "state"} {
			if _, ok := el[k]; !ok {
				return fmt.Errorf("element %v is missing %q", el, k)
			}
		}
	}
	return nil
}

func (s *edgeCLIBDD) noSecretFields() error {
	low := strings.ToLower(s.out)
	for _, bad := range []string{"secret", "token", "private", "pin", "fingerprint", "password", "key"} {
		if strings.Contains(low, bad) {
			return fmt.Errorf("the JSON carries %q:\n%s", bad, s.out)
		}
	}
	return nil
}

// onlyAreShown is the filter outline: each phrase names the set that must remain.
func (s *edgeCLIBDD) onlyAreShown(what string) error {
	if err := s.exits(0); err != nil {
		return err
	}
	switch what {
	case "nodes with the serve capability":
		if err := s.has("cabinet-jetson"); err != nil {
			return err
		}
		return s.hasNot("workshop-pi")
	case "nodes with the sense capability":
		if err := s.has("workshop-pi"); err != nil {
			return err
		}
		return s.hasNot("cabinet-jetson")
	case "nodes whose heartbeat aged out":
		if err := s.has("shed-pi"); err != nil {
			return err
		}
		return s.hasNot("workshop-pi")
	case "discovered peers that are not members":
		if err := s.has(s.candidate); err != nil {
			return err
		}
		return s.hasNot("workshop-pi")
	}
	return fmt.Errorf("unknown filter expectation %q", what)
}

func (s *edgeCLIBDD) printsThatNode() error {
	if err := s.exits(0); err != nil {
		return err
	}
	return s.has("bench-pi")
}

func (s *edgeCLIBDD) idPrefixPrintsTheSameNode() error {
	byName := s.out
	n, ok := s.node("bench-pi")
	if !ok {
		return fmt.Errorf("bench-pi is not on the Edge")
	}
	if err := s.run("roger edge describe " + n.ID[:8]); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	if s.out != byName {
		return fmt.Errorf("the id prefix printed something else:\nby name:\n%s\nby prefix:\n%s", byName, s.out)
	}
	return nil
}

func (s *edgeCLIBDD) listsTheMatchingNodes() error {
	if err := s.has("twin-one"); err != nil {
		return err
	}
	return s.has("twin-two")
}

func (s *edgeCLIBDD) exitsWithMessage(code int, msg string) error {
	if err := s.exits(code); err != nil {
		return err
	}
	return s.has(msg)
}

func (s *edgeCLIBDD) revealsNothingAboutTheOtherAccount() error {
	return s.hasNot("somebody-else")
}

func (s *edgeCLIBDD) secondRunChangesNothing() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if _, ok := s.node("bench-pi"); !ok {
		return fmt.Errorf("the node lost its name on the second run")
	}
	return nil
}

func (s *edgeCLIBDD) renamingAgainSucceeds(name string) error {
	if err := s.run("roger edge name bench-pi " + name); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	if _, ok := s.node(name); !ok {
		return fmt.Errorf("the node was not renamed to %q", name)
	}
	return nil
}

func (s *edgeCLIBDD) namesTheHolder() error {
	if err := s.has("bench-pi"); err != nil {
		return err
	}
	return s.has("already")
}

func (s *edgeCLIBDD) asksForConfirmationNamingTheNode() error {
	if err := s.has("bench-pi"); err != nil {
		return err
	}
	if err := s.has("[y/N]"); err != nil {
		return err
	}
	if _, ok := s.node("bench-pi"); !ok {
		return fmt.Errorf("the node was removed without an answer")
	}
	return nil
}

func (s *edgeCLIBDD) yesSkipsThePrompt() error {
	s.stdin = ""
	if err := s.run("roger edge forget bench-pi --yes"); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	return s.hasNot("[y/N]")
}

func (s *edgeCLIBDD) theNodeIsGoneAndPinCleared() error {
	if _, ok := s.node("bench-pi"); ok {
		return fmt.Errorf("the node is still on the Edge")
	}
	b, err := os.ReadFile(edgeStatePath())
	if err != nil {
		return err
	}
	if strings.Contains(string(b), "1177") {
		return fmt.Errorf("the pin survived the forget:\n%s", b)
	}
	return nil
}

func (s *edgeCLIBDD) nothingToForget() error {
	if err := s.exits(0); err != nil {
		return err
	}
	return s.has("nothing to forget")
}

func (s *edgeCLIBDD) candidateBecomesAMemberWithNoCapabilities() error {
	if err := s.exits(0); err != nil {
		return err
	}
	n, ok := s.node(s.candidate)
	if !ok {
		return fmt.Errorf("the candidate did not become a member:\n%s", s.out)
	}
	if len(n.Caps) != 0 {
		return fmt.Errorf("the adopted node arrived with capabilities: %v", n.Caps)
	}
	return nil
}

func (s *edgeCLIBDD) adoptingAMemberIsANoOp() error {
	before := len(s.fleetNow())
	if err := s.run("roger edge adopt " + s.candidate); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	if after := len(s.fleetNow()); after != before {
		return fmt.Errorf("the fleet changed on a re-adopt: %d -> %d", before, after)
	}
	return s.has("already")
}

func (s *edgeCLIBDD) exitsNamingTheMismatch() error {
	if err := s.exits(1); err != nil {
		return err
	}
	return s.has("fingerprint mismatch")
}

func (s *edgeCLIBDD) nothingWasAdded() error {
	for _, n := range s.fleetNow() {
		if n.ID == s.candidate {
			return fmt.Errorf("the refused candidate was added anyway")
		}
	}
	return nil
}

func (s *edgeCLIBDD) reportsThePeersItVerified() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if err := s.has("verified"); err != nil {
		return err
	}
	return s.has(s.peers[0].id)
}

func (s *edgeCLIBDD) reportsEachRefusalWithTheReason() error {
	if err := s.has("refused"); err != nil {
		return err
	}
	if err := s.has(s.peers[1].id); err != nil {
		return err
	}
	return s.has("fingerprint mismatch")
}

func (s *edgeCLIBDD) exitsZeroEvenWhenNothingFound() error {
	for _, p := range s.peers {
		p.stop()
	}
	s.peers = nil
	if err := s.run("roger edge scan"); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	return s.has("nothing")
}

// theOutputShapeIsIdentical compares the LABELS of the two describes, not their values.
func (s *edgeCLIBDD) theOutputShapeIsIdentical() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if a, b := edgeLabels(s.prevOut), edgeLabels(s.out); a != b {
		return fmt.Errorf("the two describes have different shapes:\n%s\nvs\n%s", a, b)
	}
	return nil
}

// edgeLabels reduces an output to its field labels (everything before the value), so a
// shape comparison is not a value comparison.
func edgeLabels(out string) string {
	var labels []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			labels = append(labels, "<title>")
			continue
		}
		labels = append(labels, f[0])
	}
	return strings.Join(labels, "|")
}

func (s *edgeCLIBDD) transportOnlyWithVerbose() error {
	if err := s.hasNot("carried"); err != nil {
		return err
	}
	if strings.Contains(s.prevOut, "carried") {
		return fmt.Errorf("the first describe revealed its transport without --verbose:\n%s", s.prevOut)
	}
	for _, name := range []string{"lan-box", "far-box"} {
		if err := s.run("roger edge describe " + name + " --verbose"); err != nil {
			return err
		}
		if err := s.exits(0); err != nil {
			return err
		}
		if err := s.has("carried"); err != nil {
			return err
		}
	}
	if err := s.has("relay"); err != nil { // far-box was carried by the relay
		return err
	}
	return nil
}

func (s *edgeCLIBDD) succeedsOverTheRelay() error {
	if err := s.exits(0); err != nil {
		return err
	}
	return s.hasNot("carried") // silent fall-through: nothing about transports without --verbose
}

func (s *edgeCLIBDD) verboseSaysTheLANAttemptFailedFirst() error {
	if err := s.run("roger edge describe both-ways --verbose"); err != nil {
		return err
	}
	if err := s.exits(0); err != nil {
		return err
	}
	if err := s.has("lan"); err != nil {
		return err
	}
	if err := s.has("failed"); err != nil {
		return err
	}
	li := strings.Index(strings.ToLower(s.out), "lan")
	ri := strings.Index(strings.ToLower(s.out), "carried over relay")
	if ri < 0 || li > ri {
		return fmt.Errorf("--verbose does not report the failed LAN attempt before the relay:\n%s", s.out)
	}
	return nil
}

func (s *edgeCLIBDD) printsTheLastKnownFleetStale() error {
	if err := s.has("stale"); err != nil {
		return err
	}
	if err := s.has("workshop-pi"); err != nil { // the last known fleet, still shown
		return err
	}
	return s.has("ago") // with the age of that knowledge
}

func (s *edgeCLIBDD) reportsLANCandidates() error {
	if err := s.exits(0); err != nil {
		return err
	}
	if err := s.has("candidate"); err != nil {
		return err
	}
	return s.has(s.candidate)
}

func (s *edgeCLIBDD) saysAdoptingRequiresALogin() error {
	if err := s.has("login"); err != nil {
		return err
	}
	return s.has("adopt")
}

// --- the suite ------------------------------------------------------------

func TestEdgeCLIFeature(t *testing.T) {
	st := &edgeCLIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				for _, p := range st.peers {
					p.stop()
				}
				st.peers = nil
				for _, l := range st.listeners {
					_ = l.Close()
				}
				st.listeners = nil
				return c, nil
			})

			sc.Given(`^a logged-in owner with an Edge$`, st.aLoggedInOwnerWithAnEdge)
			sc.Given(`^an Edge with only this machine$`, st.anEdgeWithOnlyThisMachine)
			sc.Given(`^nodes of several kinds and capabilities$`, st.nodesOfSeveralKindsAndCapabilities)
			sc.Given(`^a node claiming "([^"]*)" whose probe failed$`, st.aNodeClaimingWhoseProbeFailed)
			sc.Given(`^a node whose heartbeat aged out$`, st.aNodeWhoseHeartbeatAgedOut)
			sc.Given(`^a discovered unenrolled peer$`, st.aDiscoveredUnenrolledPeer)
			sc.Given(`^a node named "([^"]*)"$`, st.aNodeNamed)
			sc.Given(`^two nodes whose ids share a prefix$`, st.twoNodesWhoseIdsShareAPrefix)
			sc.Given(`^a node in the fleet$`, st.aNodeInTheFleet)
			sc.Given(`^a discovered candidate$`, st.aDiscoveredCandidate)
			sc.Given(`^a candidate whose fingerprint does not match the certificate it serves$`,
				st.aCandidateWhoseFingerprintDoesNotMatch)
			sc.Given(`^a node reachable LAN-direct and a node reachable only through a relay$`,
				st.aNodeLANDirectAndANodeRelayOnly)
			sc.Given(`^a node reachable both ways whose LAN port stops answering$`,
				st.aNodeReachableBothWaysWhoseLANStopped)
			sc.Given(`^no network and no relay connection$`, st.noNetworkAndNoRelay)
			sc.Given(`^an owner who is not logged in$`, st.anOwnerWhoIsNotLoggedIn)

			sc.When(`^the owner runs "([^"]*)"$`, st.run)
			sc.When(`^the owner describes that prefix$`, st.describesThatPrefix)
			sc.When(`^the owner describes an id that belongs to another account$`, st.describesAnIDOfAnotherAccount)
			sc.When(`^the owner names a node "([^"]*)" twice$`, st.namesANodeTwice)
			sc.When(`^the owner names a different node "([^"]*)"$`, st.namesADifferentNode)
			sc.When(`^the owner forgets a name that is not on this Edge$`, st.forgetsANameNotOnThisEdge)
			sc.When(`^the owner tries to adopt it$`, st.triesToAdoptIt)
			sc.When(`^the owner describes each of them$`, st.describesEachOfThem)
			sc.When(`^the owner describes it$`, st.describesIt)

			sc.Then(`^"([^"]*)" is listed as a command$`, st.isListedAsACommand)
			sc.Then(`^its one-line description names the fleet$`, st.descriptionNamesTheFleet)
			sc.Then(`^it prints the fleet$`, st.printsTheFleet)
			sc.Then(`^it does not print a usage error$`, st.noUsageError)
			sc.Then(`^it describes "([^"]*)"$`, st.itDescribes)
			sc.Then(`^it exits (\d+)$`, st.exits)
			sc.Then(`^it names the valid subcommands$`, st.namesTheValidSubcommands)
			sc.Then(`^it says the command takes no arguments$`, st.saysItTakesNoArguments)
			sc.Then(`^it says this machine is the only node$`, st.saysThisMachineIsTheOnlyNode)
			sc.Then(`^it names the action that would add another$`, st.namesTheActionThatAdds)
			sc.Then(`^each row carries the name, the kind, the verified capabilities, the transport in use and how long ago the node was seen$`,
				st.eachRowCarriesTheColumns)
			sc.Then(`^that capability is shown as claimed, distinctly from a verified one$`,
				st.claimedIsMarkedDistinctly)
			sc.Then(`^it is listed as dark with its last-seen age$`, st.darkIsListedWithItsAge)
			sc.Then(`^it appears under candidates, not among the fleet$`, st.appearsUnderCandidatesNotTheFleet)
			sc.Then(`^the output says how to adopt it$`, st.saysHowToAdoptIt)
			sc.Then(`^the output is a JSON array$`, st.outputIsAJSONArray)
			sc.Then(`^each element carries id, name, kind, capabilities, transports, last_seen and state$`,
				st.eachElementCarriesTheFields)
			sc.Then(`^no field carries a secret, a token or a private key$`, st.noSecretFields)
			sc.Then(`^only (.+) are shown$`, st.onlyAreShown)
			sc.Then(`^it prints that node$`, st.printsThatNode)
			sc.Then(`^running it with an unambiguous id prefix prints the same node$`, st.idPrefixPrintsTheSameNode)
			sc.Then(`^it lists the matching nodes so the owner can disambiguate$`, st.listsTheMatchingNodes)
			sc.Then(`^it exits (\d+) with "([^"]*)"$`, st.exitsWithMessage)
			sc.Then(`^the message reveals nothing about the other account$`, st.revealsNothingAboutTheOtherAccount)
			sc.Then(`^the second run succeeds and changes nothing$`, st.secondRunChangesNothing)
			sc.Then(`^renaming it again to "([^"]*)" succeeds$`, st.renamingAgainSucceeds)
			sc.Then(`^it names the node already holding it$`, st.namesTheHolder)
			sc.Then(`^it asks for confirmation naming the node$`, st.asksForConfirmationNamingTheNode)
			sc.Then(`^running it with --yes skips the prompt$`, st.yesSkipsThePrompt)
			sc.Then(`^after it succeeds the node is gone and its pin is cleared$`, st.theNodeIsGoneAndPinCleared)
			sc.Then(`^it exits 0 and says there was nothing to forget$`, st.nothingToForget)
			sc.Then(`^the candidate becomes a member with no capabilities until it declares them$`,
				st.candidateBecomesAMemberWithNoCapabilities)
			sc.Then(`^adopting something that is already a member is a clean no-op$`, st.adoptingAMemberIsANoOp)
			sc.Then(`^it exits 1 naming the mismatch$`, st.exitsNamingTheMismatch)
			sc.Then(`^nothing is added to the fleet$`, st.nothingWasAdded)
			sc.Then(`^it reports the peers it verified$`, st.reportsThePeersItVerified)
			sc.Then(`^it reports each peer it refused with the reason$`, st.reportsEachRefusalWithTheReason)
			sc.Then(`^it exits 0 even when it found nothing$`, st.exitsZeroEvenWhenNothingFound)
			sc.Then(`^the output shape is identical$`, st.theOutputShapeIsIdentical)
			sc.Then(`^each says which transport carried the request only when asked with --verbose$`,
				st.transportOnlyWithVerbose)
			sc.Then(`^the command succeeds over the relay$`, st.succeedsOverTheRelay)
			sc.Then(`^--verbose says the LAN attempt failed first$`, st.verboseSaysTheLANAttemptFailedFirst)
			sc.Then(`^it prints the last known fleet marked as stale, with the age of that knowledge$`,
				st.printsTheLastKnownFleetStale)
			sc.Then(`^it reports LAN candidates$`, st.reportsLANCandidates)
			sc.Then(`^it says that adopting them requires a login$`, st.saysAdoptingRequiresALogin)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/cli.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the roger edge command-surface scenarios failed")
	}
}
