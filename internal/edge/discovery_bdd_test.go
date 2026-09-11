package edge_test

// Executable spec: features/edge/discovery.feature - "the advertisement is a hint, the
// certificate is the proof".
//
// REAL dependencies, no mocks:
//   - a real mDNS responder and a real browser, exchanging real DNS wire bytes over
//     real UDP sockets on 127.0.0.1. The only substitution is the GROUP: instead of
//     joining 224.0.0.251, each participant's socket is fanned out to the others by
//     testBus, because Linux `lo` carries no MULTICAST flag and a test that needs a
//     real multicast-capable NIC is a test that does not run in CI. Everything above
//     the socket - query, response, PTR/SRV/TXT/A encoding and decoding, the browse
//     window, the ceiling - is the production path.
//   - real TLS listeners with real certificates, issued by the real
//     internal/towercore/cert authority, and the real DialTLS that dials them.
//   - the real internal/store Mem backend for the fleet.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// --- the loopback packet bus ---------------------------------------------

// testBus is a multicast group made of real UDP sockets: Send writes the packet to
// every other member. Same semantics as a group join, no NIC required.
type testBus struct {
	mu      sync.Mutex
	members []*busConn
	blocked bool
}

func (b *testBus) block() { b.mu.Lock(); b.blocked = true; b.mu.Unlock() }

func (b *testBus) join(t *testing.T) *busConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bus join: %v", err)
	}
	if uc, ok := pc.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(4 << 20) // a flood test must not be a kernel-buffer test
	}
	c := &busConn{pc: pc, bus: b}
	b.mu.Lock()
	b.members = append(b.members, c)
	b.mu.Unlock()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type busConn struct {
	pc  net.PacketConn
	bus *testBus
}

func (c *busConn) Send(p []byte) error {
	c.bus.mu.Lock()
	blocked := c.bus.blocked
	peers := append([]*busConn(nil), c.bus.members...)
	c.bus.mu.Unlock()
	if blocked {
		return fmt.Errorf("the network blocks multicast")
	}
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

func (c *busConn) Recv(b []byte) (int, net.Addr, error) { return c.pc.ReadFrom(b) }
func (c *busConn) SetReadDeadline(t time.Time) error    { return c.pc.SetReadDeadline(t) }
func (c *busConn) Close() error                         { return c.pc.Close() }
func (c *busConn) plane() (edge.Transport, error)       { return c, nil }

// --- a fake clock the fleet and discovery share --------------------------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// --- a peer: a real key, a real certificate, a real TLS listener ---------

type peerRig struct {
	name    string
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	id      string
	account string

	leaf   *x509.Certificate
	ts     *httptest.Server
	srv    *edge.Server
	ip     net.IP
	port   int
	advert edge.Advert

	conn   *busConn
	resp   *edge.Responder
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *peerRig) stop() {
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

// --- the scenario state ---------------------------------------------------

type discState struct {
	t     *testing.T
	bus   *testBus
	clock *fakeClock

	db    store.Store
	ca    *cert.Authority // the acct-1 authority
	ca2   *cert.Authority // somebody else's authority
	fleet *edge.Fleet

	alpha *peerRig
	peers map[string]*peerRig

	env  map[string]string
	disc *edge.Discovery
	// ifaces is what the machine reports; loopback-only is "no LAN".
	ifaces []net.Interface

	logs     []string
	logMu    sync.Mutex
	report   edge.Report
	dialed   *edge.Peer
	dialErr  error
	adverts  []edge.Advert
	darkSeen int64
	preHist  []store.EdgeEvent
	preList  int
	baseline time.Duration
	browsing chan struct{}
	startDur time.Duration
	subject  string // the peer a "it ..." step refers to
	flood    int
}

func (s *discState) reset() {
	for _, p := range s.peers {
		p.stop()
	}
	if s.alpha != nil {
		s.alpha.stop()
	}
	if s.disc != nil {
		s.disc.Stop()
	}
	s.bus = &testBus{}
	s.clock = &fakeClock{t: time.Now()}
	s.db = store.NewMem()
	s.peers = map[string]*peerRig{}
	s.env = map[string]string{}
	s.disc, s.alpha, s.fleet = nil, nil, nil
	s.logs, s.report = nil, edge.Report{}
	s.dialed, s.dialErr, s.adverts = nil, nil, nil
	s.darkSeen, s.preHist, s.preList = 0, nil, 0
	s.baseline, s.browsing, s.startDur = 0, nil, 0
	s.subject, s.flood = "", 0
	// A machine with a real LAN, by default.
	s.ifaces = []net.Interface{{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback}}
	var err error
	if s.ca, err = cert.NewAuthority(cert.Config{TTL: time.Hour}); err != nil {
		s.t.Fatal(err)
	}
	if s.ca2, err = cert.NewAuthority(cert.Config{TTL: time.Hour}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *discState) log(line string) {
	s.logMu.Lock()
	s.logs = append(s.logs, line)
	s.logMu.Unlock()
}

func (s *discState) logsWith(sub string) int {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	n := 0
	for _, l := range s.logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// certFor issues a real certificate from the account authority for a node id.
func (s *discState) certFor(auth *cert.Authority, id string, pub ed25519.PublicKey) *x509.Certificate {
	s.t.Helper()
	c, err := auth.Issue(id, pub)
	if err != nil {
		s.t.Fatalf("issue %s: %v", id, err)
	}
	return c
}

// customCert mints a certificate with an arbitrary template, signed by `parent` (the
// account authority, or a self-signature). This is how the defect table is driven with
// certificates that are really signed and really wrong.
func (s *discState) customCert(id string, pub ed25519.PublicKey, priv ed25519.PrivateKey,
	notBefore, notAfter time.Time, selfSigned bool, issuer *cert.Authority) *x509.Certificate {
	s.t.Helper()
	u, _ := url.Parse("spiffe://rogerai.fm/tower/" + id)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: id},
		URIs:                  []*url.URL{u},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	var (
		der    []byte
		err    error
		parent = tmpl
		signer any
	)
	if selfSigned {
		signer = priv
	} else {
		parent = issuer.Root()
		signer = issuer.RootKey()
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		s.t.Fatalf("custom cert: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		s.t.Fatal(err)
	}
	return c
}

// newPeer stands a node up for real: a keypair, a certificate, a TLS listener serving
// describe, and an mDNS responder on the bus.
func (s *discState) newPeer(name, account string, declared []string, leaf *x509.Certificate, priv ed25519.PrivateKey, id string) *peerRig {
	s.t.Helper()
	p := &peerRig{name: name, account: account, id: id, priv: priv, leaf: leaf}
	crt := tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}
	p.srv = edge.NewServer(edge.Describe{
		NodeID: id, Account: account, Kind: "host", Caps: declared,
	}, crt)
	ts := httptest.NewUnstartedServer(p.srv.Handler())
	ts.TLS = p.srv.TLSConfig()
	ts.StartTLS()
	p.ts = ts
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "https://"))
	p.ip = net.ParseIP(host)
	p.port, _ = strconv.Atoi(portStr)
	p.advert = p.srv.Advert(p.port)
	s.peers[name] = p
	return p
}

// truthfulPeer is the ordinary case: a certificate from this Edge's authority, and an
// advertisement that tells the truth about it.
func (s *discState) truthfulPeer(name, account string, declared []string, auth *cert.Authority) *peerRig {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	leaf := s.certFor(auth, id, pub)
	p := s.newPeer(name, account, declared, leaf, priv, id)
	p.pub = pub
	return p
}

// advertise starts a peer's responder on the bus.
func (s *discState) advertise(p *peerRig) {
	p.conn = s.bus.join(s.t)
	p.resp = edge.NewResponder(p.conn, edge.DefaultService, p.advert, []net.IP{net.ParseIP("127.0.0.1")})
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(1)
	go func() { defer p.wg.Done(); _ = p.resp.Serve(ctx) }()
}

// startAlpha builds and starts the instance under test.
func (s *discState) startAlpha() {
	cfg := edge.ConfigFromEnv(func(k string) string { return s.env[k] })
	cfg.Window = 700 * time.Millisecond
	cfg.Interval = time.Hour
	cfg.ManualPasses = true // every pass in this suite is an explicit RunOnce
	if s.flood > 0 {
		cfg.Ceiling = 8
		cfg.Window = 2 * time.Second
	}
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(s.clock.Now)
	opts := edge.Options{
		Fleet:        s.fleet,
		Authority:    s.ca,
		Self:         s.alpha.advert,
		Config:       cfg,
		AdvertiseIPs: []net.IP{net.ParseIP("127.0.0.1")},
		Interfaces:   func() []net.Interface { return s.ifaces },
		Now:          s.clock.Now,
		Log:          s.log,
	}
	if len(s.ifaces) > 0 && s.ifaces[0].Name != "lo-only" {
		opts.Plane = func() (edge.Transport, error) { return s.bus.join(s.t), nil }
	}
	s.disc = edge.New(opts)
	start := time.Now()
	if err := s.disc.Start(context.Background()); err != nil {
		s.t.Fatalf("start: %v", err)
	}
	s.startDur = time.Since(start)
}

// browseOnce runs one discovery pass and keeps the report.
func (s *discState) browseOnce() {
	if s.disc == nil {
		s.startAlpha()
	}
	s.report = s.disc.RunOnce(context.Background())
}

// probe browses the bus as an independent listener, so "did alpha advertise" is
// answered by real packets rather than by asking alpha.
func (s *discState) probe(window time.Duration) []edge.Advert {
	c := s.bus.join(s.t)
	res, err := edge.Browse(context.Background(), c, edge.DefaultService, window, 64)
	if err != nil {
		return nil
	}
	return res.Adverts
}

func (s *discState) findAdvert(ads []edge.Advert, id string) (edge.Advert, bool) {
	for _, a := range ads {
		if a.NodeID == id {
			return a, true
		}
	}
	return edge.Advert{}, false
}

func (s *discState) node(id string) (store.EdgeNode, bool) {
	list, err := s.fleet.List()
	if err != nil {
		s.t.Fatal(err)
	}
	for _, n := range list {
		if n.ID == id {
			return n, true
		}
	}
	return store.EdgeNode{}, false
}

// ===========================================================================
// STEPS
// ===========================================================================

func (s *discState) instanceEnrolled(name, account string) error {
	s.alpha = s.truthfulPeer(name, account, []string{"serve"}, s.ca)
	s.subject = name
	return nil
}

func (s *discState) discoveryEnabled() error {
	s.env[edge.EnvDiscovery] = "1"
	return nil
}

func (s *discState) instanceStarts(name string) error {
	if p, ok := s.peers[name]; ok && p != s.alpha {
		s.advertise(p)
		return nil
	}
	s.startAlpha()
	if s.disc.Advertising() {
		// Give the responder a moment to be listening before anybody browses.
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func (s *discState) advertisesService(service string) error {
	ads := s.probe(700 * time.Millisecond)
	s.adverts = ads
	if _, ok := s.findAdvert(ads, s.alpha.id); !ok {
		return fmt.Errorf("nothing answered a browse for %q (%d adverts seen)", service, len(ads))
	}
	if service != edge.DefaultService {
		return fmt.Errorf("the spec names %q, the code advertises %q", service, edge.DefaultService)
	}
	return nil
}

func (s *discState) advertisementCarriesTheFields() error {
	ad, ok := s.findAdvert(s.adverts, s.alpha.id)
	if !ok {
		return fmt.Errorf("alpha's advertisement was not received")
	}
	switch {
	case ad.NodeID != s.alpha.id:
		return fmt.Errorf("node id = %q", ad.NodeID)
	case ad.Account != "acct-1":
		return fmt.Errorf("account = %q", ad.Account)
	case len(ad.Caps) != 1 || ad.Caps[0] != "serve":
		return fmt.Errorf("capabilities = %v", ad.Caps)
	case ad.Port != s.alpha.port:
		return fmt.Errorf("port = %d, want %d", ad.Port, s.alpha.port)
	case ad.Fingerprint != s.alpha.srv.Fingerprint():
		return fmt.Errorf("fingerprint = %q, want %q", ad.Fingerprint, s.alpha.srv.Fingerprint())
	case len(ad.Fingerprint) != 64:
		return fmt.Errorf("fingerprint %q is not a SHA-256 hex digest", ad.Fingerprint)
	}
	return nil
}

func (s *discState) advertisementCarriesNoSecret() error {
	pkt, err := s.disc.Responder().Packet()
	if err != nil {
		return err
	}
	blob := string(pkt)
	// The actual private key, in both spellings it could plausibly leak in.
	for _, secret := range []string{string(s.alpha.priv), string(s.alpha.priv.Seed())} {
		if strings.Contains(blob, secret) {
			return fmt.Errorf("the advertisement carries the private key")
		}
	}
	for _, word := range []string{"token", "secret", "bearer", "password", "PRIVATE KEY", "rog-grant_"} {
		if strings.Contains(strings.ToLower(blob), strings.ToLower(word)) {
			return fmt.Errorf("the advertisement carries %q", word)
		}
	}
	return nil
}

func (s *discState) peerDialsOnAdvertisedPort(name string) error {
	p := s.peers[name]
	s.dialed, s.dialErr = edge.DialTLS(context.Background(),
		net.JoinHostPort(p.ip.String(), strconv.Itoa(p.port)))
	return s.dialErr
}

func (s *discState) certHashesToAdvertisedFingerprint(name string) error {
	p := s.peers[name]
	if s.dialed == nil {
		return fmt.Errorf("no certificate was captured")
	}
	if s.dialed.Fingerprint != p.advert.Fingerprint {
		return fmt.Errorf("served %q, advertised %q", s.dialed.Fingerprint, p.advert.Fingerprint)
	}
	if s.dialed.Fingerprint != edge.Fingerprint(p.srv.TLSConfig().Certificates[0].Certificate[0]) {
		return fmt.Errorf("the hash is not of the certificate actually served")
	}
	return nil
}

func (s *discState) discoveryEnvIs(v string) error {
	s.env[edge.EnvDiscovery] = v
	return nil
}

func (s *discState) itAdvertises(yesno string) error {
	want := yesno == "yes"
	if got := s.disc.Advertising(); got != want {
		return fmt.Errorf("Advertising() = %v, want %v", got, want)
	}
	ads := s.probe(400 * time.Millisecond)
	_, heard := s.findAdvert(ads, s.alpha.id)
	if heard != want {
		return fmt.Errorf("heard on the wire = %v, want %v", heard, want)
	}
	return nil
}

func (s *discState) itBrowses(yesno string) error {
	want := yesno == "yes"
	if got := s.disc.Browsing(); got != want {
		return fmt.Errorf("Browsing() = %v, want %v", got, want)
	}
	if !want {
		// And a pass, if somebody asks for one anyway, does nothing but say so.
		rep := s.disc.RunOnce(context.Background())
		if rep.Dials != 0 || rep.Seen != 0 {
			return fmt.Errorf("a disabled discovery still browsed: %+v", rep)
		}
	}
	return nil
}

func (s *discState) instanceWithNoAccount(name string) error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	// No account means no account authority: an unenrolled instance signs its own.
	leaf := s.customCert(id, pub, priv, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), true, nil)
	p := s.newPeer(name, "", nil, leaf, priv, id)
	p.pub = pub
	s.subject = name
	return nil
}

func (s *discState) advertisementCarriesEmptyAccount() error {
	p := s.peers[s.subject]
	ads := s.probe(700 * time.Millisecond)
	ad, ok := s.findAdvert(ads, p.id)
	if !ok {
		return fmt.Errorf("%q did not advertise", s.subject)
	}
	if ad.Account != "" {
		return fmt.Errorf("account field = %q, want empty", ad.Account)
	}
	return nil
}

func (s *discState) peerTreatsItAsACandidate() error {
	s.browseOnce()
	p := s.peers[s.subject]
	if _, ok := s.node(p.id); ok {
		return fmt.Errorf("an unenrolled peer became a member")
	}
	for _, c := range s.disc.Candidates() {
		if c.ID == p.id {
			if c.Presence != string(edge.PresenceCandidate) {
				return fmt.Errorf("presence = %q, want CANDIDATE", c.Presence)
			}
			return nil
		}
	}
	return fmt.Errorf("the unenrolled peer is not offered as a candidate either")
}

func (s *discState) instanceAdvertises(string) error {
	return nil
}

// synthetic addresses: the rule is a pure function of the address set, so it is asserted
// against one rather than against whatever NIC the test host happens to have.
func lanAddrs() []net.Addr {
	mk := func(cidr string) net.Addr {
		ip, n, _ := net.ParseCIDR(cidr)
		return &net.IPNet{IP: ip, Mask: n.Mask}
	}
	return []net.Addr{
		mk("127.0.0.1/8"), mk("::1/128"),
		mk("10.1.2.3/8"), mk("172.16.5.6/12"), mk("192.168.1.20/24"), mk("fd00::5/64"),
		mk("169.254.10.11/16"), mk("fe80::1/64"),
		mk("8.8.8.8/32"), mk("2606:4700::1111/128"),
	}
}

func (s *discState) advertisesOnlyOnPrivate() error {
	got := edge.AdvertiseAddrs(lanAddrs())
	want := map[string]bool{"127.0.0.1": true, "::1": true, "10.1.2.3": true,
		"172.16.5.6": true, "192.168.1.20": true, "fd00::5": true}
	if len(got) != len(want) {
		return fmt.Errorf("advertised on %v", got)
	}
	for _, ip := range got {
		if !want[ip.String()] {
			return fmt.Errorf("advertised on a non-private address %s", ip)
		}
	}
	return nil
}

func (s *discState) neverOnLinkLocal() error {
	for _, ip := range edge.AdvertiseAddrs(lanAddrs()) {
		if ip.IsLinkLocalUnicast() || strings.HasPrefix(ip.String(), "169.254.") {
			return fmt.Errorf("advertised on link-local %s", ip)
		}
	}
	// And a machine that has ONLY a link-local address has no LAN to advertise on.
	ip, n, _ := net.ParseCIDR("169.254.10.11/16")
	if edge.HasLAN([]net.Addr{&net.IPNet{IP: ip, Mask: n.Mask}}) {
		return fmt.Errorf("a link-local-only machine was treated as having a LAN")
	}
	return nil
}

func (s *discState) truthfulPeerEnrolled(name, account string) error {
	// The advertisement claims MORE than the node will admit to over its certificate,
	// so "the capabilities are the ones describe reported" has something to bite on.
	p := s.truthfulPeer(name, account, []string{"sense"}, s.ca)
	p.advert.Caps = []string{"serve", "actuate", "relay"}
	s.advertise(p)
	s.subject = name
	return nil
}

func (s *discState) alphaBrowses(string) error {
	s.browseOnce()
	return nil
}

func (s *discState) appearsVerified(name string) error {
	p := s.peers[name]
	n, ok := s.node(p.id)
	if !ok {
		return fmt.Errorf("%q is not in the fleet; report: %+v", name, s.report)
	}
	if n.Presence != string(edge.PresenceVerified) {
		return fmt.Errorf("%q presence = %q, want VERIFIED", name, n.Presence)
	}
	return nil
}

func (s *discState) capsAreFromDescribe() error {
	p := s.peers[s.subject]
	n, _ := s.node(p.id)
	var names []string
	for _, c := range n.Caps {
		names = append(names, c.Name)
	}
	if len(names) != 1 || names[0] != "sense" {
		return fmt.Errorf("capabilities = %v, want the describe answer [sense], not the advertised %v",
			names, p.advert.Caps)
	}
	return nil
}

func (s *discState) peerAdvertisingWrongFingerprint() error {
	p := s.truthfulPeer("liar", "acct-1", []string{"sense"}, s.ca)
	p.advert.Fingerprint = strings.Repeat("0", 64)
	s.advertise(p)
	s.subject = "liar"
	return nil
}

func (s *discState) browsesAndDialsIt(string) error {
	s.browseOnce()
	return nil
}

func (s *discState) peerNotInFleet() error {
	p := s.peers[s.subject]
	if _, ok := s.node(p.id); ok {
		return fmt.Errorf("the peer is in the fleet")
	}
	return nil
}

func (s *discState) reportedWithBothFingerprints(reason string) error {
	p := s.peers[s.subject]
	f, ok := s.report.Find(reason)
	if !ok {
		return fmt.Errorf("no %q refusal in the report: %+v", reason, s.report.Refusals)
	}
	if f.Advertised != p.advert.Fingerprint {
		return fmt.Errorf("advertised fingerprint = %q", f.Advertised)
	}
	if f.Observed != p.srv.Fingerprint() {
		return fmt.Errorf("observed fingerprint = %q, want the served %q", f.Observed, p.srv.Fingerprint())
	}
	return nil
}

func (s *discState) oneWarningNamesAddressAndFingerprints() error {
	p := s.peers[s.subject]
	addr := net.JoinHostPort(p.ip.String(), strconv.Itoa(p.port))
	hits := 0
	for _, w := range s.report.Warnings {
		if strings.Contains(w, addr) && strings.Contains(w, p.advert.Fingerprint) &&
			strings.Contains(w, p.srv.Fingerprint()) {
			hits++
		}
	}
	if hits != 1 {
		return fmt.Errorf("%d warning lines name the address and both fingerprints: %v", hits, s.report.Warnings)
	}
	if s.logsWith(addr) != 1 {
		return fmt.Errorf("the owner was told %d times, want once: %v", s.logsWith(addr), s.logs)
	}
	return nil
}

func (s *discState) verifiedNodeAtOneAddress(name string) error {
	if err := s.truthfulPeerEnrolled(name, "acct-1"); err != nil {
		return err
	}
	s.browseOnce()
	return s.appearsVerified(name)
}

func (s *discState) attackerAtDifferentAddress() error {
	victim := s.peers[s.subject]
	victim.stop() // the real node is off the air; only the attacker is talking
	// The attacker holds its own key and a certificate this authority really signed -
	// for ITSELF. It advertises the victim's id and account, truthfully fingerprinting
	// its own certificate so there is no fingerprint mismatch to catch it on.
	att := s.truthfulPeer("attacker", "acct-1", []string{"serve"}, s.ca)
	att.advert.NodeID = victim.id
	att.advert.Account = "acct-1"
	s.advertise(att)
	return nil
}

func (s *discState) browsesAndDialsTheAttacker(string) error {
	s.browseOnce()
	return nil
}

func (s *discState) attackerFailsVerification() error {
	if len(s.report.Refusals) == 0 {
		return fmt.Errorf("the attacker was not refused: %+v", s.report)
	}
	att := s.peers["attacker"]
	if _, ok := s.node(att.id); ok {
		return fmt.Errorf("the attacker's own id entered the fleet")
	}
	return nil
}

func (s *discState) keepsItsPlace(name string) error {
	p := s.peers[name]
	n, ok := s.node(p.id)
	if !ok {
		return fmt.Errorf("%q lost its place in the fleet", name)
	}
	want := net.JoinHostPort(p.ip.String(), strconv.Itoa(p.port))
	if got := edge.LANAddr(n); got != want {
		return fmt.Errorf("%q moved to %q, want its original %q", name, got, want)
	}
	if n.Presence != string(edge.PresenceVerified) {
		return fmt.Errorf("%q is no longer VERIFIED: %q", name, n.Presence)
	}
	return nil
}

func (s *discState) reportedAs(reason string) error {
	if !s.report.Has(reason) {
		return fmt.Errorf("no %q in the report: %+v", reason, s.report.Refusals)
	}
	return nil
}

func (s *discState) strangerWithValidCert(name, account string) error {
	p := s.truthfulPeer(name, account, []string{"serve"}, s.ca2)
	s.advertise(p)
	s.subject = name
	return nil
}

func (s *discState) namedPeerNotInFleet(name string) error {
	p := s.peers[name]
	if _, ok := s.node(p.id); ok {
		return fmt.Errorf("%q is in the fleet", name)
	}
	return nil
}

func (s *discState) notOfferedAsCandidate() error {
	p := s.peers[s.subject]
	for _, c := range s.disc.Candidates() {
		if c.ID == p.id {
			return fmt.Errorf("a peer of another account was offered as a candidate")
		}
	}
	return nil
}

func (s *discState) nothingWrittenToTheStore(name string) error {
	p := s.peers[name]
	for _, acct := range []string{"acct-1", "acct-2", p.account} {
		rows, err := s.db.EdgeNodesOfAccount(acct)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.ID == p.id {
				return fmt.Errorf("a row for %q was written under %q", name, acct)
			}
		}
	}
	if _, ok, _ := s.db.EdgeNodeByID("acct-1", p.id); ok {
		return fmt.Errorf("a row for %q exists", name)
	}
	return nil
}

func (s *discState) peerWithDefectiveCert(defect string) error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	now := time.Now()
	var leaf *x509.Certificate
	switch defect {
	case "expired":
		leaf = s.customCert(id, pub, priv, now.Add(-2*time.Hour), now.Add(-time.Hour), false, s.ca)
	case "not yet valid":
		leaf = s.customCert(id, pub, priv, now.Add(time.Hour), now.Add(2*time.Hour), false, s.ca)
	case "signed by an unknown authority":
		leaf = s.customCert(id, pub, priv, now.Add(-time.Minute), now.Add(time.Hour), false, s.ca2)
	case "valid for a different node id":
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		leaf = s.customCert(edge.NodeID(other), pub, priv, now.Add(-time.Minute), now.Add(time.Hour), false, s.ca)
	case "self-signed while claiming an account":
		leaf = s.customCert(id, pub, priv, now.Add(-time.Minute), now.Add(time.Hour), true, nil)
	default:
		return fmt.Errorf("unknown defect %q", defect)
	}
	p := s.newPeer("defective", "acct-1", []string{"sense"}, leaf, priv, id)
	p.pub = pub
	s.advertise(p)
	s.subject = "defective"
	return nil
}

func (s *discState) alphaDialsIt(string) error {
	s.browseOnce()
	return nil
}

func (s *discState) refusedWithReason(reason string) error {
	if !s.report.Has(reason) {
		return fmt.Errorf("refused with %+v, want reason %q", s.report.Refusals, reason)
	}
	return nil
}

func (s *discState) itDoesNotAppearInTheFleet() error { return s.peerNotInFleet() }

func (s *discState) peerWhosePortAcceptsNothing() error {
	// A real port that really refuses: bind it, learn the number, close it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	leaf := s.certFor(s.ca, id, pub)
	p := &peerRig{name: "silent", account: "acct-1", id: id, priv: priv, pub: pub,
		ip: addr.IP, port: addr.Port}
	p.srv = edge.NewServer(edge.Describe{NodeID: id, Account: "acct-1", Kind: "host"},
		tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv})
	p.advert = p.srv.Advert(addr.Port)
	s.peers["silent"] = p
	s.advertise(p)
	s.subject = "silent"
	return nil
}

func (s *discState) dialFailsOnceReportedAs(reason string) error {
	if !s.report.Has(reason) {
		return fmt.Errorf("report = %+v, want %q", s.report.Refusals, reason)
	}
	if s.report.Dials != 1 {
		return fmt.Errorf("%d dials in one pass, want exactly 1", s.report.Dials)
	}
	return nil
}

func (s *discState) retriedNextIntervalNotTightLoop() error {
	first := s.report.Dials
	second := s.disc.RunOnce(context.Background())
	if second.Dials != 1 {
		return fmt.Errorf("the next interval made %d dials, want 1", second.Dials)
	}
	if !second.Has(edge.ReasonUnreachable) {
		return fmt.Errorf("the retry did not report unreachable: %+v", second.Refusals)
	}
	if first+second.Dials != 2 {
		return fmt.Errorf("two intervals made %d dials, want 2", first+second.Dials)
	}
	return nil
}

func (s *discState) unenrolledPeerAdvertisingTruthfully(name string) error {
	if err := s.instanceWithNoAccount(name); err != nil {
		return err
	}
	s.advertise(s.peers[name])
	s.subject = name
	return nil
}

func (s *discState) appearsAsCandidate(name string) error {
	p := s.peers[name]
	found := false
	for _, c := range s.disc.Candidates() {
		if c.ID == p.id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%q is not among the candidates", name)
	}
	if _, ok := s.node(p.id); ok {
		return fmt.Errorf("%q is in the fleet, which is not clearly separate", name)
	}
	return nil
}

func (s *discState) adoptingRequiresExplicitAction() error {
	p := s.peers[s.subject]
	// Browsing again does not adopt it. Only an owner does.
	s.disc.RunOnce(context.Background())
	if _, ok := s.node(p.id); ok {
		return fmt.Errorf("browsing adopted the candidate by itself")
	}
	if _, err := s.disc.Adopt("n_never-seen", "ghost"); err == nil {
		return fmt.Errorf("a node nobody advertised could be adopted")
	}
	return nil
}

func (s *discState) untilAdoptedNoCapsNoTraffic() error {
	p := s.peers[s.subject]
	var cand store.EdgeNode
	for _, c := range s.disc.Candidates() {
		if c.ID == p.id {
			cand = c
		}
	}
	if len(cand.Caps) != 0 {
		return fmt.Errorf("a candidate carries capabilities: %+v", cand.Caps)
	}
	g := store.Grant{ID: "g", Owner: "acct-1", Nodes: []string{p.id}}
	if err := s.fleet.AuthorizeInvoke(g, p.id, ""); err == nil {
		return fmt.Errorf("a candidate accepted traffic")
	}
	// The owner's explicit action, and only then, makes it a member.
	if _, err := s.disc.Adopt(p.id, "adopted"); err != nil {
		return fmt.Errorf("adoption failed: %v", err)
	}
	n, ok := s.node(p.id)
	if !ok {
		return fmt.Errorf("adoption did not make it a member")
	}
	if edge.Routable(n, edge.Serve) {
		return fmt.Errorf("adoption granted a verified capability")
	}
	return nil
}

func (s *discState) verifiedNodeInFleet(name string) error {
	return s.verifiedNodeAtOneAddress(name)
}

func (s *discState) stopsAdvertisingAndAnswering(name string) error {
	n, _ := s.node(s.peers[name].id)
	s.darkSeen = n.LastSeen
	s.peers[name].stop()
	return nil
}

func (s *discState) afterTTLShownDark(name string) error {
	s.clock.advance(edge.DefaultTTL + time.Minute)
	s.browseOnce()
	n, ok := s.node(s.peers[name].id)
	if !ok {
		return fmt.Errorf("%q was dropped instead of darkened", name)
	}
	if n.Presence != string(edge.PresenceDark) {
		return fmt.Errorf("%q presence = %q, want DARK", name, n.Presence)
	}
	if n.LastSeen != s.darkSeen {
		return fmt.Errorf("last-seen was rewritten: %d, want %d", n.LastSeen, s.darkSeen)
	}
	return nil
}

func (s *discState) stillListed() error {
	p := s.peers[s.subject]
	if _, ok := s.node(p.id); !ok {
		return fmt.Errorf("the dark node is not listed")
	}
	return nil
}

func (s *discState) aDarkNode(name string) error {
	if err := s.verifiedNodeInFleet(name); err != nil {
		return err
	}
	if err := s.stopsAdvertisingAndAnswering(name); err != nil {
		return err
	}
	if err := s.afterTTLShownDark(name); err != nil {
		return err
	}
	n, _ := s.node(s.peers[name].id)
	s.preHist = append([]store.EdgeEvent(nil), n.History...)
	list, _ := s.fleet.List()
	s.preList = len(list)
	return nil
}

func (s *discState) advertisesAgainMatchingCert(name string) error {
	old := s.peers[name]
	// The same key and the SAME certificate: a node coming back, not a new one.
	leaf := old.leaf
	p := s.newPeer(name, "acct-1", []string{"sense"}, leaf, old.priv, old.id)
	p.pub = old.pub
	// Its pin is the certificate the owner already accepted.
	n, _ := s.node(old.id)
	p.advert.Fingerprint = n.Pin
	crt := tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: old.priv}
	if edge.Fingerprint(crt.Certificate[0]) != n.Pin {
		return fmt.Errorf("the fixture did not reproduce the pinned certificate")
	}
	s.advertise(p)
	s.subject = name
	s.browseOnce()
	return nil
}

func (s *discState) returnsToVerified() error {
	p := s.peers[s.subject]
	n, ok := s.node(p.id)
	if !ok {
		return fmt.Errorf("the node is gone")
	}
	if n.Presence != string(edge.PresenceVerified) {
		return fmt.Errorf("presence = %q, want VERIFIED; report %+v", n.Presence, s.report)
	}
	return nil
}

func (s *discState) nameHistoryPlaceUnchanged() error {
	p := s.peers[s.subject]
	n, _ := s.node(p.id)
	if n.Name != p.id {
		return fmt.Errorf("the name changed to %q", n.Name)
	}
	if len(n.History) < len(s.preHist) {
		return fmt.Errorf("history shrank: %d < %d", len(n.History), len(s.preHist))
	}
	for i, e := range s.preHist {
		if n.History[i] != e {
			return fmt.Errorf("history entry %d was rewritten: %+v, want %+v", i, n.History[i], e)
		}
	}
	list, _ := s.fleet.List()
	if len(list) != s.preList {
		return fmt.Errorf("the fleet size changed: %d, want %d", len(list), s.preList)
	}
	return nil
}

func (s *discState) noDuplicateRow() error {
	p := s.peers[s.subject]
	list, _ := s.fleet.List()
	n := 0
	for _, x := range list {
		if x.ID == p.id {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d rows for the returning node, want 1", n)
	}
	rows, _ := s.db.EdgeNodesOfAccount("acct-1")
	n = 0
	for _, x := range rows {
		if x.ID == p.id {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d stored rows for the returning node, want 1", n)
	}
	return nil
}

func (s *discState) peerAdvertisesWithNonMatchingCert(name string) error {
	old := s.peers[name]
	// A certificate this authority really signed, for this node id, bound to a
	// DIFFERENT key: it chains, it names the right node, and it is not the one the
	// owner pinned.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	leaf := s.certFor(s.ca, old.id, pub)
	p := s.newPeer("impostor", "acct-1", []string{"sense"}, leaf, priv, old.id)
	p.pub = pub
	s.advertise(p)
	s.browseOnce()
	return nil
}

func (s *discState) refusedAs(reason string) error { return s.reportedAs(reason) }

func (s *discState) staysDark(name string) error {
	n, ok := s.node(s.peers[name].id)
	if !ok {
		return fmt.Errorf("%q vanished", name)
	}
	if n.Presence != string(edge.PresenceDark) {
		return fmt.Errorf("%q presence = %q, want DARK", name, n.Presence)
	}
	if n.Pin == "" {
		return fmt.Errorf("the pin was cleared by the impostor")
	}
	return nil
}

func (s *discState) ownerToldPinCanBeCleared() error {
	for _, w := range s.report.Warnings {
		if strings.Contains(w, "clear") && strings.Contains(w, "pin") {
			return nil
		}
	}
	return fmt.Errorf("no warning tells the owner the pin can be cleared: %v", s.report.Warnings)
}

func (s *discState) reachableBothWays(name string) error {
	p := s.truthfulPeer(name, "acct-1", []string{"sense"}, s.ca)
	s.advertise(p)
	s.subject = name
	// Already known through the relay, before the LAN ever sees it.
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(s.clock.Now)
	n := edge.NewNode(p.pub, name, edge.Host)
	n.Transports = []store.EdgeTransport{{Kind: "relay", Addr: "relay.rogerai.fm"}}
	if _, err := s.fleet.Enroll(n); err != nil {
		return err
	}
	s.browseOnce()
	return nil
}

func (s *discState) fleetIsListed() error { return nil }

func (s *discState) appearsExactlyOnce(name string) error {
	p := s.peers[name]
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	n := 0
	for _, x := range list {
		if x.ID == p.id {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%q appears %d times, want once", name, n)
	}
	return nil
}

func (s *discState) transportsLANThenRelay() error {
	p := s.peers[s.subject]
	n, _ := s.node(p.id)
	if len(n.Transports) != 2 {
		return fmt.Errorf("transports = %+v, want a LAN and a relay entry", n.Transports)
	}
	if n.Transports[0].Kind != "lan" || n.Transports[1].Kind != "relay" {
		return fmt.Errorf("transports are not LAN-first: %+v", n.Transports)
	}
	return nil
}

func (s *discState) relayOnlyNode(name string) error {
	// gamma is reached only through the relay: that is what a registered Station is.
	if err := s.db.UpsertNode(store.NodeRecord{
		NodeID: name, Reg: protocol.NodeRegistration{NodeID: name, PubKey: "beef"},
		LastSeen: s.clock.Now().Unix(), RegisteredAt: 1,
	}); err != nil {
		return err
	}
	if err := s.db.BindNode(name, "acct-1"); err != nil {
		return err
	}
	// And a LAN-discovered node to compare it against: the claim under test is that
	// the two rows differ ONLY in their transports, which needs both rows to exist.
	if err := s.truthfulPeerEnrolled("beta", "acct-1"); err != nil {
		return err
	}
	s.browseOnce()
	return nil
}

func (s *discState) appearsWithTransportOnly(name, kind string) error {
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.ID != name {
			continue
		}
		if len(n.Transports) != 1 || n.Transports[0].Kind != kind {
			return fmt.Errorf("%q transports = %+v, want only %q", name, n.Transports, kind)
		}
		return nil
	}
	return fmt.Errorf("%q is not in the fleet", name)
}

func (s *discState) nothingDiffersExceptTransports() error {
	list, _ := s.fleet.List()
	var relayNode, lanNode store.EdgeNode
	for _, n := range list {
		if n.ID == "gamma" {
			relayNode = n
		} else if edge.LANAddr(n) != "" {
			lanNode = n
		}
	}
	if relayNode.ID == "" || lanNode.ID == "" {
		return fmt.Errorf("need both a relay node and a LAN node to compare: %+v", list)
	}
	// Same record type, same account scope, same shape: named, kinded, presence-marked
	// and capability-marked. The transports are the difference, and the pin rides them.
	type shape struct {
		Account, Kind, Presence string
		Named, Capped           bool
	}
	sh := func(n store.EdgeNode) shape {
		return shape{n.Account, n.Kind, n.Presence, n.Name != "", len(n.Caps) > 0}
	}
	if sh(relayNode) != sh(lanNode) {
		return fmt.Errorf("the relay-only node differs beyond its transports:\n relay: %+v\n   lan: %+v",
			sh(relayNode), sh(lanNode))
	}
	if edge.LANAddr(relayNode) != "" {
		return fmt.Errorf("the relay-only node claims a LAN transport")
	}
	return nil
}

func (s *discState) networkBlocksMulticast() error {
	s.bus.block()
	return nil
}

func (s *discState) browsesAndFindsNothing(string) error {
	s.browseOnce()
	return nil
}

func (s *discState) discoveryReports(note string) error {
	for _, n := range s.report.Notes {
		if n == note {
			return nil
		}
	}
	return fmt.Errorf("notes = %v, want %q", s.report.Notes, note)
}

func (s *discState) stillServesAndRelays(string) error {
	// A real request, over the real TLS listener, while discovery is in whatever state
	// the scenario put it in.
	p, err := edge.DialTLS(context.Background(),
		net.JoinHostPort(s.alpha.ip.String(), strconv.Itoa(s.alpha.port)))
	if err != nil {
		return fmt.Errorf("alpha stopped serving when discovery failed: %v", err)
	}
	if p.Describe.NodeID != s.alpha.id {
		return fmt.Errorf("alpha served somebody else's answer")
	}
	return nil
}

func (s *discState) unavailableSaidOnce() error {
	for i := 0; i < 3; i++ {
		s.disc.RunOnce(context.Background())
	}
	if n := s.logsWith("discovery is unavailable"); n != 1 {
		return fmt.Errorf("said unavailable %d times across 4 intervals, want once: %v", n, s.logs)
	}
	return nil
}

func (s *discState) browseTakesFiveSeconds() error {
	s.startAlpha()
	// A real relay-shaped request first, to know what "normal speed" is here.
	t0 := time.Now()
	if err := s.stillServesAndRelays("alpha"); err != nil {
		return err
	}
	s.baseline = time.Since(t0)
	s.browsing = make(chan struct{})
	go func() {
		defer close(s.browsing)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// A browse window far longer than any request should ever wait on.
		_, _ = edge.Browse(ctx, s.bus.join(s.t), edge.DefaultService, 5*time.Second, 64)
	}()
	go func() { s.disc.RunOnce(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (s *discState) consumerRelaysThrough(name string) error {
	t0 := time.Now()
	if err := s.stillServesAndRelays(name); err != nil {
		return err
	}
	s.startDur = time.Since(t0)
	return nil
}

func (s *discState) relayCompletesAtNormalSpeed() error {
	if s.startDur > 500*time.Millisecond {
		return fmt.Errorf("the relay took %v during a 5s browse", s.startDur)
	}
	if s.baseline > 0 && s.startDur > 20*s.baseline+100*time.Millisecond {
		return fmt.Errorf("the relay took %v, baseline %v", s.startDur, s.baseline)
	}
	return nil
}

func (s *discState) nothingWaitedOnDiscovery() error {
	select {
	case <-s.browsing:
		return fmt.Errorf("the browse had already finished; the test proved nothing")
	default:
	}
	return nil
}

func (s *discState) floodArrives(n int) error {
	s.flood = n
	s.startAlpha()
	sender := s.bus.join(s.t)
	for i := 0; i < n; i++ {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		id := edge.NodeID(pub)
		ad := edge.Advert{NodeID: id, Account: "acct-1", Kind: "host",
			Caps: []string{"sense"}, Port: 9, Fingerprint: strings.Repeat("a", 64)}
		pkt, err := edge.NewResponder(nil, edge.DefaultService, ad, []net.IP{net.ParseIP("127.0.0.1")}).Packet()
		if err != nil {
			return err
		}
		if err := sender.Send(pkt); err != nil {
			return err
		}
	}
	s.report = s.disc.RunOnce(context.Background())
	return nil
}

func (s *discState) atMostCeilingRetained() error {
	if s.report.Seen == 0 {
		return fmt.Errorf("no records arrived; the flood proved nothing")
	}
	kept := s.report.Seen - s.report.Dropped
	if kept > 8 {
		return fmt.Errorf("retained %d records, ceiling is 8", kept)
	}
	return nil
}

func (s *discState) excessCountedNotStored() error {
	if s.report.Dropped <= 0 {
		return fmt.Errorf("nothing was counted as dropped out of %d seen", s.report.Seen)
	}
	if len(s.disc.Candidates()) > 8 {
		return fmt.Errorf("%d candidates retained, ceiling is 8", len(s.disc.Candidates()))
	}
	rows, _ := s.db.EdgeNodesOfAccount("acct-1")
	if len(rows) > 8 {
		return fmt.Errorf("%d rows written by a flood", len(rows))
	}
	return nil
}

func (s *discState) memoryAndDialsBounded() error {
	if s.report.Dials > 8 {
		return fmt.Errorf("%d dials from one flooded interval, ceiling is 8", s.report.Dials)
	}
	return nil
}

func (s *discState) noNonLoopbackInterface() error {
	// A loopback-only machine: up, but with no multicast-capable private interface.
	s.ifaces = []net.Interface{{Index: 1, Name: "lo-only", Flags: net.FlagUp | net.FlagLoopback}}
	return nil
}

func (s *discState) discoveryReportsUnavailableOnce() error {
	if n := s.logsWith("discovery is unavailable"); n != 1 {
		return fmt.Errorf("said unavailable %d times, want once: %v", n, s.logs)
	}
	s.disc.RunOnce(context.Background())
	s.disc.RunOnce(context.Background())
	if n := s.logsWith("discovery is unavailable"); n != 1 {
		return fmt.Errorf("said unavailable %d times after three passes, want once: %v", n, s.logs)
	}
	if !s.disc.Report().Has("") && len(s.disc.Report().Notes) == 0 {
		return fmt.Errorf("the report says nothing about being unavailable")
	}
	return nil
}

func (s *discState) startupNotDelayed() error {
	if s.startDur > 250*time.Millisecond {
		return fmt.Errorf("Start took %v on a machine with no LAN", s.startDur)
	}
	if s.disc.Advertising() || s.disc.Browsing() {
		return fmt.Errorf("discovery claims to be running with no LAN")
	}
	return nil
}

func TestDiscoveryFeature(t *testing.T) {
	st := &discState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.disc != nil {
					st.disc.Stop()
					st.disc = nil
				}
				for _, p := range st.peers {
					p.stop()
				}
				if st.alpha != nil {
					st.alpha.stop()
				}
				return c, nil
			})
			sc.Step(`^a roger instance "([^"]*)" enrolled to account "([^"]*)"$`, st.instanceEnrolled)
			sc.Step(`^LAN discovery is enabled$`, st.discoveryEnabled)
			sc.Step(`^"([^"]*)" starts$`, st.instanceStarts)
			sc.Step(`^it advertises the mDNS service "([^"]*)"$`, st.advertisesService)
			sc.Step(`^the advertisement carries the node id, the account id, the declared capabilities, the port, and the SHA-256 fingerprint of the certificate it will present$`, st.advertisementCarriesTheFields)
			sc.Step(`^the advertisement carries no secret, no token and no private key$`, st.advertisementCarriesNoSecret)
			sc.Step(`^a peer dials "([^"]*)" on the advertised port$`, st.peerDialsOnAdvertisedPort)
			sc.Step(`^the certificate "([^"]*)" presents hashes to the fingerprint it advertised$`, st.certHashesToAdvertisedFingerprint)
			sc.Step(`^ROGERAI_EDGE_DISCOVERY is "([^"]*)"$`, st.discoveryEnvIs)
			sc.Step(`^it advertises (yes|no)$`, st.itAdvertises)
			sc.Step(`^it browses for peers (yes|no)$`, st.itBrowses)
			sc.Step(`^a roger instance "([^"]*)" with no account$`, st.instanceWithNoAccount)
			sc.Step(`^its advertisement carries an empty account field$`, st.advertisementCarriesEmptyAccount)
			sc.Step(`^a peer treats it as a candidate, never as a member$`, st.peerTreatsItAsACandidate)
			sc.Step(`^"([^"]*)" advertises$`, st.instanceAdvertises)
			sc.Step(`^it advertises only on loopback and RFC1918 or IPv6-ULA interfaces$`, st.advertisesOnlyOnPrivate)
			sc.Step(`^it never advertises on a link-local 169\.254 address$`, st.neverOnLinkLocal)
			sc.Step(`^a peer "([^"]*)" enrolled to account "([^"]*)" advertising a truthful record$`, st.truthfulPeerEnrolled)
			sc.Step(`^"([^"]*)" browses$`, st.alphaBrowses)
			sc.Step(`^"([^"]*)" appears as a VERIFIED node of the fleet$`, st.appearsVerified)
			sc.Step(`^its capabilities are the ones its certificate-backed describe reported, not the ones the advertisement claimed$`, st.capsAreFromDescribe)
			sc.Step(`^a peer advertising a fingerprint that does not match its certificate$`, st.peerAdvertisingWrongFingerprint)
			sc.Step(`^"([^"]*)" browses and dials it$`, st.browsesAndDialsIt)
			sc.Step(`^the peer does NOT appear in the fleet$`, st.peerNotInFleet)
			sc.Step(`^it appears in the discovery report as "([^"]*)" with the observed and advertised fingerprints$`, st.reportedWithBothFingerprints)
			sc.Step(`^one warning line names the address and both fingerprints$`, st.oneWarningNamesAddressAndFingerprints)
			sc.Step(`^a verified node "([^"]*)" at one address$`, st.verifiedNodeAtOneAddress)
			sc.Step(`^an attacker advertising the same node id and account at a different address$`, st.attackerAtDifferentAddress)
			sc.Step(`^"([^"]*)" browses and dials the attacker$`, st.browsesAndDialsTheAttacker)
			sc.Step(`^the attacker fails certificate verification$`, st.attackerFailsVerification)
			sc.Step(`^"([^"]*)" keeps its place in the fleet, at its original address$`, st.keepsItsPlace)
			sc.Step(`^the attempt is reported as "([^"]*)"$`, st.reportedAs)
			sc.Step(`^a peer "([^"]*)" enrolled to account "([^"]*)" with a valid certificate$`, st.strangerWithValidCert)
			sc.Step(`^"([^"]*)" does not appear in the fleet$`, st.namedPeerNotInFleet)
			sc.Step(`^it is not offered as a candidate$`, st.notOfferedAsCandidate)
			sc.Step(`^nothing about "([^"]*)" is written to the fleet store$`, st.nothingWrittenToTheStore)
			sc.Step(`^a peer advertising a record whose certificate is (.+)$`, st.peerWithDefectiveCert)
			sc.Step(`^"([^"]*)" dials it$`, st.alphaDialsIt)
			sc.Step(`^the peer is refused with reason "([^"]*)"$`, st.refusedWithReason)
			sc.Step(`^it does not appear in the fleet$`, st.itDoesNotAppearInTheFleet)
			sc.Step(`^a peer advertising a record whose port accepts nothing$`, st.peerWhosePortAcceptsNothing)
			sc.Step(`^the dial fails once and is reported as "([^"]*)"$`, st.dialFailsOnceReportedAs)
			sc.Step(`^it is retried on the next discovery interval, not in a tight loop$`, st.retriedNextIntervalNotTightLoop)
			sc.Step(`^a peer "([^"]*)" with no account advertising truthfully$`, st.unenrolledPeerAdvertisingTruthfully)
			sc.Step(`^"([^"]*)" appears as a CANDIDATE, clearly separate from the fleet$`, st.appearsAsCandidate)
			sc.Step(`^adopting it requires the owner's explicit action$`, st.adoptingRequiresExplicitAction)
			sc.Step(`^until adopted it has no capabilities and receives no traffic$`, st.untilAdoptedNoCapsNoTraffic)
			sc.Step(`^a verified node "([^"]*)" in the fleet$`, st.verifiedNodeInFleet)
			sc.Step(`^"([^"]*)" stops advertising and stops answering$`, st.stopsAdvertisingAndAnswering)
			sc.Step(`^after the candidate TTL "([^"]*)" is shown DARK with its last-seen time$`, st.afterTTLShownDark)
			sc.Step(`^it is still listed, because a fleet that silently drops members hides the problem$`, st.stillListed)
			sc.Step(`^a DARK node "([^"]*)"$`, st.aDarkNode)
			sc.Step(`^"([^"]*)" advertises again with the same node id and a matching certificate$`, st.advertisesAgainMatchingCert)
			sc.Step(`^it returns to VERIFIED$`, st.returnsToVerified)
			sc.Step(`^its name, its history and its place in the fleet are unchanged$`, st.nameHistoryPlaceUnchanged)
			sc.Step(`^no duplicate row is created$`, st.noDuplicateRow)
			sc.Step(`^a peer advertises node id "([^"]*)" with a certificate that does not match the pin$`, st.peerAdvertisesWithNonMatchingCert)
			sc.Step(`^it is refused as "([^"]*)"$`, st.refusedAs)
			sc.Step(`^"([^"]*)" stays DARK$`, st.staysDark)
			sc.Step(`^the owner is told the pin can be cleared deliberately$`, st.ownerToldPinCanBeCleared)
			sc.Step(`^a verified node "([^"]*)" reachable both on the LAN and through a relay$`, st.reachableBothWays)
			sc.Step(`^the fleet is listed$`, st.fleetIsListed)
			sc.Step(`^"([^"]*)" appears exactly once$`, st.appearsExactlyOnce)
			sc.Step(`^its transports list LAN first and relay second$`, st.transportsLANThenRelay)
			sc.Step(`^a node "([^"]*)" that is connected to a relay but not on this LAN$`, st.relayOnlyNode)
			sc.Step(`^"([^"]*)" appears with transport "([^"]*)" only$`, st.appearsWithTransportOnly)
			sc.Step(`^nothing about it differs from a LAN-discovered node except its transports$`, st.nothingDiffersExceptTransports)
			sc.Step(`^the network blocks multicast$`, st.networkBlocksMulticast)
			sc.Step(`^"([^"]*)" browses and finds nothing$`, st.browsesAndFindsNothing)
			sc.Step(`^discovery reports "([^"]*)"$`, st.discoveryReports)
			sc.Step(`^"([^"]*)" still serves and relays exactly as before$`, st.stillServesAndRelays)
			sc.Step(`^one line says discovery is unavailable, once, not per interval$`, st.unavailableSaidOnce)
			sc.Step(`^a discovery browse that takes 5 seconds$`, st.browseTakesFiveSeconds)
			sc.Step(`^a consumer relays a request through "([^"]*)"$`, st.consumerRelaysThrough)
			sc.Step(`^the relay completes at its normal speed$`, st.relayCompletesAtNormalSpeed)
			sc.Step(`^nothing about the relay waited on discovery$`, st.nothingWaitedOnDiscovery)
			sc.Step(`^(\d+) advertised records arrive in one interval$`, st.floodArrives)
			sc.Step(`^at most the configured candidate ceiling is retained$`, st.atMostCeilingRetained)
			sc.Step(`^the excess is counted and reported, not stored$`, st.excessCountedNotStored)
			sc.Step(`^memory and dial attempts stay bounded$`, st.memoryAndDialsBounded)
			sc.Step(`^the machine has no non-loopback interface$`, st.noNonLoopbackInterface)
			sc.Step(`^discovery reports unavailable once$`, st.discoveryReportsUnavailableOnce)
			sc.Step(`^startup is not delayed$`, st.startupNotDelayed)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/discovery.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the LAN discovery scenarios failed")
	}
}
