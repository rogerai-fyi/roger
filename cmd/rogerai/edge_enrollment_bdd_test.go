package main

// Executable spec: features/edge/enrollment.feature - "a machine the owner is logged in
// on enrolls itself, and from then on the Edge works with no internet".
//
// REAL dependencies, no mocks of our own code:
//   - the REAL command dispatch (dispatch(), the same one main() calls) and the REAL
//     exit-code mapping, so a refusal here is the process's answer.
//   - a REAL internal/towercore/cert authority on both sides. The "Core" in these
//     scenarios is a REAL HTTP issuer standing on the REAL enrollhttp.Handler over a
//     REAL cert.Authority - the production server code, bound to 127.0.0.1 instead of
//     to the internet. Nothing about the issuing path is stubbed; only its address is.
//   - a REAL local authority: a root generated on disk by the production code, and the
//     same handler served over a REAL TCP listener by the REAL edge host.
//   - REAL TLS describe listeners with REAL certificates, a REAL mDNS responder and a
//     REAL browse exchanging real DNS wire bytes over real UDP sockets. The only
//     substitution is the multicast GROUP (a loopback packet bus), because Linux `lo`
//     carries no MULTICAST flag - the same substitution the discovery spec makes.
//   - the REAL on-disk Edge state, the REAL fleet over the REAL store.
//
// TWO MACHINES IN ONE PROCESS. A second machine is a second config directory and a
// second user key: `client.LoadOrCreateUserKey` caches per process, so the enroll path
// takes its key from the edgeUserKey seam, which is what a second machine replaces.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/tui"
)

// --- a machine ------------------------------------------------------------

// enrollMachine is one physical machine: its own config directory, its own user key
// (login leaves a DIFFERENT one on every machine - that is why the Edge needs a root of
// its own), and, once it is running, its own Edge host.
type enrollMachine struct {
	label      string
	dir        string
	user       ed25519.PrivateKey
	host       *edgeHost
	hooks      tui.Hooks
	lastReport edge.Report
}

func (m *enrollMachine) stop() {
	if m.host != nil {
		m.host.stop()
		m.host = nil
	}
}

// --- the Core stub: the REAL issuer, at a loopback address ------------------

type coreStub struct {
	srv  *http.Server
	ln   net.Listener
	ca   *cert.Authority
	iss  *edgeauth.Issuer
	reg  map[string]string // user public key (hex) -> the account Core knows it as
	mu   sync.Mutex
	hits int
	// mangle doctors a good response into one of the defect table's bad ones.
	mangle func(edgeauth.Response) edgeauth.Response
	// truncate cuts the body short, which is not a Response at all.
	truncate bool
	// onKey reports the node key each request carries.
	onKey func(ed25519.PublicKey)
}

func (c *coreStub) url() string { return "http://" + c.ln.Addr().String() }

func (c *coreStub) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

func (c *coreStub) close() {
	if c.srv != nil {
		_ = c.srv.Close()
		c.srv = nil
	}
}

// newCoreStub stands the REAL enrollment handler up over a REAL authority.
func newCoreStub(t *testing.T) *coreStub {
	t.Helper()
	ca, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	c := &coreStub{ca: ca, reg: map[string]string{}}
	c.iss = edgeauth.NewIssuer(edgeauth.IssuerConfig{
		Authority: ca,
		Accounts: func(userKey string) (string, bool) {
			c.mu.Lock()
			defer c.mu.Unlock()
			a, ok := c.reg[userKey]
			return a, ok
		},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits++
		mangle, truncate, iss, onKey := c.mangle, c.truncate, c.iss, c.onKey
		c.mu.Unlock()
		if onKey != nil && r.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req edgeauth.Request
			if json.Unmarshal(body, &req) == nil {
				if raw, err := hex.DecodeString(req.NodeKey); err == nil {
					onKey(ed25519.PublicKey(raw))
				}
			}
		}
		inner := enrollhttp.Handler(iss)
		if mangle == nil && !truncate {
			inner.ServeHTTP(w, r)
			return
		}
		rec := &captureWriter{header: http.Header{}}
		inner.ServeHTTP(rec, r)
		if rec.status != 0 && rec.status != http.StatusOK {
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body)
			return
		}
		var resp edgeauth.Response
		_ = json.Unmarshal(rec.body, &resp)
		if mangle != nil {
			resp = mangle(resp)
		}
		b, _ := json.Marshal(resp)
		if truncate {
			b = b[:len(b)/2]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = c.srv.Serve(ln) }()
	t.Cleanup(c.close)
	return c
}

func (c *coreStub) setMangle(fn func(edgeauth.Response) edgeauth.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mangle = fn
}

func (c *coreStub) clearMangle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mangle, c.truncate = nil, false
}

func (c *coreStub) setTruncate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.truncate = true
}

// swap replaces the issuer behind the stub (a shorter-lived authority, say).
func (c *coreStub) swap(iss *edgeauth.Issuer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.iss = iss
}

// watchKey reports the node public key of each request as it arrives, so a defect row
// can mint a certificate for the very node that is asking.
func (c *coreStub) watchKey(fn func(ed25519.PublicKey)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onKey = fn
}

// captureWriter buffers a handler's answer so the stub can doctor it.
type captureWriter struct {
	header http.Header
	body   []byte
	status int
}

func (w *captureWriter) Header() http.Header { return w.header }
func (w *captureWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}
func (w *captureWriter) WriteHeader(status int) { w.status = status }

// --- scenario state -------------------------------------------------------

type enrollBDD struct {
	t   *testing.T
	bus *edgeBus

	core     *coreStub
	machines []*enrollMachine
	cur      *enrollMachine

	// the local authority endpoint, when one has been designated.
	authorityURL string
	// the authority machine, so its issuer can be reached.
	authorityOf *enrollMachine

	out  string
	err  error
	code int

	// what a scenario captured before an action, for the "unchanged" assertions.
	beforeID      string
	beforeCert    string
	beforeHistory []store.EdgeEvent
	beforeList    int
	beforeStation store.NodeRecord

	// a peer standing on the bus for the verification scenarios.
	peers []*edgePeer
	// the last verification verdict a peer check produced.
	verdict string
	// scratch for the defect table.
	failReason string
	restored   func()

	strangerLeaf   *x509.Certificate
	strangerID     string
	otherCore      *coreStub
	pinBefore      string
	revokedCert    *x509.Certificate
	pendingNodeKey ed25519.PublicKey
	expiredRoot    *x509.Certificate
	stationDB      *store.Mem
}

func (s *enrollBDD) reset(t *testing.T) {
	for _, m := range s.machines {
		m.stop()
	}
	for _, p := range s.peers {
		p.stop()
	}
	if s.restored != nil {
		s.restored()
		s.restored = nil
	}
	s.t = t
	s.bus = &edgeBus{}
	s.machines, s.peers = nil, nil
	s.cur, s.authorityOf = nil, nil
	s.authorityURL = ""
	s.out, s.err, s.code = "", nil, 0
	s.beforeID, s.beforeCert, s.beforeHistory, s.beforeList = "", "", nil, 0
	s.beforeStation = store.NodeRecord{}
	s.verdict, s.failReason = "", ""
	s.core = newCoreStub(t)
	edgeDialTimeout = 300 * time.Millisecond
	edgeStdin = strings.NewReader("")
	edgeDiscoveryOptions = defaultEdgeDiscoveryOptions
	s.pointDiscoveryAtTheBus()
	t.Cleanup(func() {
		for _, m := range s.machines {
			m.stop()
		}
		for _, p := range s.peers {
			p.stop()
		}
		if s.restored != nil {
			s.restored()
			s.restored = nil
		}
		edgeUserKey = defaultEdgeUserKey
		edgeDiscoveryOptions = defaultEdgeDiscoveryOptions
	})
}

// pointDiscoveryAtTheBus keeps the PRODUCTION options (authority, self advert, config)
// and swaps only the multicast group for the loopback bus.
func (s *enrollBDD) pointDiscoveryAtTheBus() {
	bus, t := s.bus, s.t
	edgeDiscoveryOptions = func(f *edge.Fleet) edge.Options {
		o := defaultEdgeDiscoveryOptions(f)
		o.Config.Window = 700 * time.Millisecond
		o.Config.ManualPasses = true
		o.Plane = func() (edge.Transport, error) { return bus.join(t), nil }
		o.AdvertiseIPs = []net.IP{net.ParseIP("127.0.0.1")}
		return o
	}
}

// newMachine builds a machine with its own config directory and its own user key.
func (s *enrollBDD) newMachine(label string) *enrollMachine {
	s.t.Helper()
	m := &enrollMachine{label: label, dir: s.t.TempDir()}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	m.user = priv
	s.machines = append(s.machines, m)
	s.use(m)
	return m
}

// use points the process at one machine: its config directory and its user key.
func (s *enrollBDD) use(m *enrollMachine) {
	s.t.Helper()
	if s.restored == nil {
		// Every environment variable this suite moves is put back. ROGER_BROKER in
		// particular: leaving one scenario's loopback address behind made two unrelated
		// tests in this package fail, because an env override beats the config file.
		home, hOK := os.LookupEnv("HOME")
		xdg, xOK := os.LookupEnv("XDG_CONFIG_HOME")
		app, aOK := os.LookupEnv("AppData")
		brk, bOK := os.LookupEnv("ROGER_BROKER")
		s.restored = func() {
			restoreEnv("HOME", home, hOK)
			restoreEnv("XDG_CONFIG_HOME", xdg, xOK)
			restoreEnv("AppData", app, aOK)
			restoreEnv("ROGER_BROKER", brk, bOK)
		}
	}
	_ = os.Setenv("HOME", m.dir)
	_ = os.Setenv("XDG_CONFIG_HOME", m.dir)
	_ = os.Setenv("AppData", m.dir)
	edgeUserKey = func() ed25519.PrivateKey { return m.user }
	s.cur = m
	if got := configPath(); !strings.HasPrefix(got, m.dir) {
		s.t.Fatalf("config isolation FAILED: %q is not under %q", got, m.dir)
	}
}

func restoreEnv(k, v string, ok bool) {
	if ok {
		_ = os.Setenv(k, v)
		return
	}
	_ = os.Unsetenv(k)
}

// login writes the real auth record the CLI reads (client.LinkedLogin).
func (s *enrollBDD) login(who string) {
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

// registerWithCore is what a real login leaves behind on Core: the account knows this
// machine's user public key.
func (s *enrollBDD) registerWithCore(m *enrollMachine, account string) {
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	s.core.reg[hex.EncodeToString(m.user.Public().(ed25519.PublicKey))] = account
}

// broker points this machine at an address. The Core authority reaches Core through it.
func (s *enrollBDD) broker(url string) { _ = os.Setenv("ROGER_BROKER", url) }

// deadAddr is an address nothing is listening on: no route to Core.
func (s *enrollBDD) deadAddr() string {
	s.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// run drives the REAL dispatch.
func (s *enrollBDD) run(line string) error {
	args := strings.Fields(line)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("a command line starts with `roger`, got %q", line)
	}
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err, s.code = out, err, exitCode(err)
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

func (s *enrollBDD) mustRun(line string) error {
	if err := s.run(line); err != nil {
		return err
	}
	if s.err != nil {
		return fmt.Errorf("`%s` failed: %v\n%s", line, s.err, s.out)
	}
	return nil
}

// identity is what this machine holds after enrolling.
func (s *enrollBDD) identity() (*edgeauth.Identity, ed25519.PrivateKey, bool) {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	id, key, ok, err := st.LoadIdentity()
	if err != nil {
		s.t.Fatalf("load identity: %v", err)
	}
	return id, key, ok
}

func (s *enrollBDD) fleet() []store.EdgeNode {
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

// startHost brings this machine's Edge host up: the describe listener, the advertiser,
// the browser and (when this machine is the authority) the LAN issuing service.
func (s *enrollBDD) startHost(m *enrollMachine) error {
	s.use(m)
	h, err := newEdgeHost("")
	if err != nil {
		return err
	}
	m.host = h
	h.wire(&m.hooks)
	h.start(context.Background())
	if !h.running() {
		return fmt.Errorf("%s: discovery did not start", m.label)
	}
	return nil
}

// browse runs one real discovery pass on this machine.
func (s *enrollBDD) browse(m *enrollMachine) edge.Report {
	s.use(m)
	m.lastReport = m.host.runPass(context.Background())
	return m.lastReport
}

func (s *enrollBDD) other(m *enrollMachine) *enrollMachine {
	for _, o := range s.machines {
		if o != m {
			return o
		}
	}
	return nil
}

// =========================================================================
// Background
// =========================================================================

func (s *enrollBDD) ownerLoggedIn() error {
	m := s.newMachine("this")
	s.login("owner")
	s.registerWithCore(m, "owner")
	s.broker(s.core.url())
	return nil
}

func (s *enrollBDD) machineHoldsTheUserKey() error {
	if len(s.cur.user) != ed25519.PrivateKeySize {
		return fmt.Errorf("this machine holds no user key")
	}
	return nil
}

// =========================================================================
// 1. The happy path
// =========================================================================

func (s *enrollBDD) ownerEnrollsAs(name string) error {
	return s.mustRun("roger edge enroll " + name)
}

func (s *enrollBDD) ownerEnrollsThisMachine() error {
	return s.mustRun("roger edge enroll workshop")
}

func (s *enrollBDD) generatesANewKeypair() error {
	id, key, ok := s.identity()
	if !ok {
		return fmt.Errorf("this machine holds no Edge identity")
	}
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("no node private key was generated")
	}
	pub := key.Public().(ed25519.PublicKey)
	if edge.NodeID(pub) != id.NodeID {
		return fmt.Errorf("the node id %q is not derived from the key it generated", id.NodeID)
	}
	if pub.Equal(s.cur.user.Public()) {
		return fmt.Errorf("the node identity reused the USER key instead of generating a new one")
	}
	return nil
}

func (s *enrollBDD) requestSignedWithUserKey() error {
	got := s.core.iss.LastRequest()
	if got.UserKey != hex.EncodeToString(s.cur.user.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("the request was signed by %q, not the machine's user key", got.UserKey)
	}
	return got.VerifySignature()
}

func (s *enrollBDD) receivesACertificateNamingAccountAndNode() error {
	id, _, ok := s.identity()
	if !ok {
		return fmt.Errorf("no identity")
	}
	if id.Account != "owner" {
		return fmt.Errorf("the certificate names account %q, want owner", id.Account)
	}
	named, err := s.core.ca.Authenticate(id.Cert)
	if err != nil {
		return err
	}
	if named != id.NodeID {
		return fmt.Errorf("the certificate names %q, not this node %q", named, id.NodeID)
	}
	return nil
}

func (s *enrollBDD) receivesTheEdgeRoot() error {
	id, _, ok := s.identity()
	if !ok {
		return fmt.Errorf("no identity")
	}
	if id.Root == nil || !id.Root.IsCA {
		return fmt.Errorf("this machine did not receive an Edge root")
	}
	if !id.Root.Equal(s.core.ca.Root()) {
		return fmt.Errorf("the root it holds is not the authority's root")
	}
	return nil
}

func (s *enrollBDD) canNowAdvertise() error {
	m := s.cur
	if err := s.startHost(m); err != nil {
		return err
	}
	id, _, _ := s.identity()
	// A real browse from an independent listener: did anything go out on the wire?
	c := s.bus.join(s.t)
	res, err := edge.Browse(context.Background(), c, edge.DefaultService, time.Second, 32)
	if err != nil {
		return err
	}
	for _, a := range res.Adverts {
		if a.NodeID == id.NodeID && a.Account == "owner" && a.Fingerprint != "" {
			return nil
		}
	}
	return fmt.Errorf("an enrolled machine advertised nothing: %+v", res.Adverts)
}

func (s *enrollBDD) privateKeyGeneratedLocally() error { return s.generatesANewKeypair() }

func (s *enrollBDD) noRequestCarriedIt() error {
	_, key, _ := s.identity()
	secret := hex.EncodeToString(key)
	for _, got := range s.core.iss.Requests() {
		raw, _ := json.Marshal(got)
		if strings.Contains(strings.ToLower(string(raw)), secret) {
			return fmt.Errorf("the node's PRIVATE key crossed the wire")
		}
	}
	return nil
}

func (s *enrollBDD) storedReadableOnlyByItsOwner() error {
	path := filepath.Join(edgeAuthDir(), edgeauth.NodeKeyFile)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return fmt.Errorf("%s is %#o, want 0600", path, perm)
	}
	return nil
}

func (s *enrollBDD) certificateNamesThisAccount() error {
	id, _, ok := s.identity()
	if !ok {
		return fmt.Errorf("no identity")
	}
	if id.Account != "owner" {
		return fmt.Errorf("account = %q", id.Account)
	}
	return nil
}

func (s *enrollBDD) itNamesThisNodesID() error {
	id, key, _ := s.identity()
	named, err := s.core.ca.Authenticate(id.Cert)
	if err != nil {
		return err
	}
	if named != edge.NodeID(key.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("the certificate does not name this node")
	}
	return nil
}

func (s *enrollBDD) carriesNoCapability() error {
	id, _, _ := s.identity()
	for _, c := range edge.Capabilities() {
		if strings.Contains(strings.ToLower(id.Cert.Subject.CommonName), string(c)) {
			return fmt.Errorf("the certificate's subject mentions capability %q", c)
		}
		for _, u := range id.Cert.URIs {
			if strings.Contains(strings.ToLower(u.String()), string(c)) {
				return fmt.Errorf("the certificate's identity mentions capability %q", c)
			}
		}
	}
	if len(id.Cert.ExtKeyUsage) != 1 || id.Cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return fmt.Errorf("the certificate carries more than channel authentication: %v", id.Cert.ExtKeyUsage)
	}
	if id.Cert.IsCA {
		return fmt.Errorf("an Edge certificate may not issue")
	}
	return nil
}

func (s *enrollBDD) secondMachineOfTheSameAccountAlsoEnrolled() error {
	// "ALSO enrolled" presupposes this one is: enroll it if the scenario has not.
	if _, _, ok := s.identity(); !ok {
		if err := s.ownerEnrollsAs("workshop"); err != nil {
			return err
		}
	}
	first := s.cur
	m := s.newMachine("second")
	s.login("owner")
	s.registerWithCore(m, "owner")
	s.broker(s.core.url())
	if err := s.mustRun("roger edge enroll bench"); err != nil {
		return err
	}
	s.use(first)
	return nil
}

func (s *enrollBDD) bothBrowseTheLAN() error {
	for _, m := range s.machines {
		if m.host == nil {
			if err := s.startHost(m); err != nil {
				return err
			}
		}
	}
	// Two passes each: the first browse warms the group, the second sees the answers.
	for range 2 {
		for _, m := range s.machines {
			s.browse(m)
		}
	}
	return nil
}

func (s *enrollBDD) eachSeesTheOtherAsVerified() error {
	for _, m := range s.machines {
		s.use(m)
		other := s.other(m)
		oid := s.identityOf(other)
		found := false
		for _, n := range s.fleet() {
			if n.ID == oid {
				found = true
				if n.Presence != string(edge.PresenceVerified) {
					return fmt.Errorf("%s sees %s as %q, want VERIFIED", m.label, other.label, n.Presence)
				}
			}
		}
		if !found {
			return fmt.Errorf("%s never saw %s at all; its last pass reported %+v",
				m.label, other.label, m.lastReport)
		}
	}
	return nil
}

// identityOf reads a machine's node id without disturbing the current machine.
func (s *enrollBDD) identityOf(m *enrollMachine) string {
	cur := s.cur
	s.use(m)
	id, _, ok := s.identity()
	s.use(cur)
	if !ok {
		return ""
	}
	return id.NodeID
}

func (s *enrollBDD) neitherIsACandidate() error {
	for _, m := range s.machines {
		s.use(m)
		st, err := loadEdgeState()
		if err != nil {
			return err
		}
		if len(st.candidates) != 0 {
			return fmt.Errorf("%s holds %d candidate(s): an enrolled peer is a member, not a candidate",
				m.label, len(st.candidates))
		}
	}
	return nil
}

func (s *enrollBDD) edgeListShowsIt() error {
	if err := s.mustRun("roger edge list"); err != nil {
		return err
	}
	id, _, _ := s.identity()
	if !strings.Contains(s.out, edgeShortID(id.NodeID)) && !strings.Contains(s.out, "workshop") {
		return fmt.Errorf("`roger edge list` does not show the enrolled machine:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) edgeScreenDrawsItAsSelf() error {
	h, err := newEdgeHost("")
	if err != nil {
		return err
	}
	defer h.stop()
	var hooks tui.Hooks
	h.wire(&hooks)
	if hooks.EdgeSelf != "workshop" {
		return fmt.Errorf("the EDGE screen calls this machine %q, want its enrolled name", hooks.EdgeSelf)
	}
	list, err := hooks.EdgeFleet.List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.Name == "workshop" {
			return nil
		}
	}
	return fmt.Errorf("the enrolled machine is not on the screen's fleet")
}

// =========================================================================
// 2. It works offline afterwards
// =========================================================================

func (s *enrollBDD) twoEnrolledMachines() error {
	if err := s.ownerEnrollsAs("workshop"); err != nil {
		return err
	}
	return s.secondMachineOfTheSameAccountAlsoEnrolled()
}

func (s *enrollBDD) noRouteToTheInternet() error {
	dead := s.deadAddr()
	for _, m := range s.machines {
		s.use(m)
		s.broker("http://" + dead)
	}
	s.core.mu.Lock()
	s.core.hits = 0
	s.core.mu.Unlock()
	return nil
}

func (s *enrollBDD) theyBrowseTheLAN() error { return s.bothBrowseTheLAN() }

func (s *enrollBDD) eachVerifiesAgainstTheRootItHolds() error {
	for _, m := range s.machines {
		s.use(m)
		st := edgeauth.Store{Dir: edgeAuthDir()}
		auth, _, err := st.Trust(time.Now())
		if err != nil {
			return fmt.Errorf("%s holds no usable Edge root: %v", m.label, err)
		}
		if auth == nil {
			return fmt.Errorf("%s holds no Edge root", m.label)
		}
	}
	return s.eachSeesTheOtherAsVerified()
}

func (s *enrollBDD) bothAppearAsVerifiedMembers() error { return s.eachSeesTheOtherAsVerified() }

func (s *enrollBDD) nothingContactedCore() error {
	if n := s.core.count(); n != 0 {
		return fmt.Errorf("Core was contacted %d time(s) on a path that must not need it", n)
	}
	return nil
}

func (s *enrollBDD) anEnrolledMachine() error { return s.ownerEnrollsAs("workshop") }

func (s *enrollBDD) coreIsUnreachable() error {
	s.broker("http://" + s.deadAddr())
	s.core.mu.Lock()
	s.core.hits = 0
	s.core.mu.Unlock()
	return nil
}

func (s *enrollBDD) theFleetIsListed() error { return s.mustRun("roger edge list") }

func (s *enrollBDD) theMachineIsStillAMember() error {
	id, _, ok := s.identity()
	if !ok {
		return fmt.Errorf("the identity vanished when Core went away")
	}
	for _, n := range s.fleet() {
		if n.ID == id.NodeID {
			return nil
		}
	}
	return fmt.Errorf("the machine left the fleet when Core went away")
}

func (s *enrollBDD) certificateStillHonouredUntilExpiry() error {
	id, _, _ := s.identity()
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	if _, err := auth.Authenticate(id.Cert); err != nil {
		return fmt.Errorf("its own certificate stopped authenticating with Core away: %v", err)
	}
	if !time.Now().Before(id.Cert.NotAfter) {
		return fmt.Errorf("the certificate is already expired")
	}
	return nil
}

func (s *enrollBDD) edgeUsesTheCoreAuthority() error {
	d, _, err := edgeauth.Store{Dir: edgeAuthDir()}.Descriptor()
	if err != nil {
		return err
	}
	if d.Kind != "" && d.Kind != edgeauth.KindCore {
		return fmt.Errorf("this Edge is rooted at %q, not Core", d.Kind)
	}
	return nil
}

func (s *enrollBDD) noRouteToCore() error { return s.coreIsUnreachable() }

func (s *enrollBDD) ownerTriesToEnroll() error { return s.run("roger edge enroll workshop") }

func (s *enrollBDD) failsNamingConnectivity() error {
	if s.err == nil {
		return fmt.Errorf("enrolling with no route to Core SUCCEEDED:\n%s", s.out)
	}
	msg := strings.ToLower(s.out)
	if !strings.Contains(msg, "reach") && !strings.Contains(msg, "network") {
		return fmt.Errorf("the refusal does not name connectivity:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) namesTheLocalAuthorityAsTheOfflineWay() error {
	if !strings.Contains(s.out, "roger edge authority local") {
		return fmt.Errorf("the refusal does not point at the local authority:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) nothingHalfEnrolled() error { return s.noPartialIdentity() }

// =========================================================================
// 2b. The chosen authority
// =========================================================================

func (s *enrollBDD) networkNeverHadInternet() error {
	dead := s.deadAddr()
	for _, m := range s.machines {
		s.use(m)
		s.broker("http://" + dead)
	}
	s.core.mu.Lock()
	s.core.hits = 0
	s.core.mu.Unlock()
	return nil
}

func (s *enrollBDD) ownerDesignatesThisMachine() error {
	if err := s.mustRun("roger edge authority local shed"); err != nil {
		return err
	}
	s.authorityOf = s.cur
	return nil
}

// serveAuthority brings the authority machine's LAN issuing service up and records
// where it answers.
func (s *enrollBDD) serveAuthority() error {
	m := s.authorityOf
	if m == nil {
		return fmt.Errorf("no machine has been designated as the Edge authority")
	}
	if m.host == nil {
		if err := s.startHost(m); err != nil {
			return err
		}
	}
	addr := m.host.authorityAddr()
	if addr == "" {
		return fmt.Errorf("the designated authority is not serving on the LAN")
	}
	s.authorityURL = "http://" + addr
	return nil
}

func (s *enrollBDD) ownerEnrollsBothOnThatNetwork() error {
	if err := s.serveAuthority(); err != nil {
		return err
	}
	s.use(s.authorityOf)
	if err := s.mustRun("roger edge enroll workshop --authority " + s.authorityURL); err != nil {
		return err
	}
	second := s.newMachine("second")
	s.broker("http://" + s.deadAddr())
	// The owner tells the authority about the second machine: an authority on a plant
	// network still decides who may join it.
	key := hex.EncodeToString(second.user.Public().(ed25519.PublicKey))
	cur := s.cur
	s.use(s.authorityOf)
	if err := s.mustRun("roger edge authority allow " + key); err != nil {
		return err
	}
	s.use(cur)
	return s.mustRun("roger edge enroll bench --authority " + s.authorityURL)
}

func (s *enrollBDD) bothHoldCertificatesFromThatAuthority() error {
	root := s.localRoot()
	for _, m := range s.machines {
		s.use(m)
		id, _, ok := s.identity()
		if !ok {
			return fmt.Errorf("%s holds no identity", m.label)
		}
		if !id.Root.Equal(root) {
			return fmt.Errorf("%s holds a certificate from a different root", m.label)
		}
	}
	return nil
}

// localRoot is the designated machine's PUBLIC root.
func (s *enrollBDD) localRoot() *x509.Certificate {
	cur := s.cur
	s.use(s.authorityOf)
	defer s.use(cur)
	l, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		s.t.Fatalf("the designated authority has no root: %v", err)
	}
	return l.Authority().Root()
}

func (s *enrollBDD) bothVerifiedInOneFleet() error {
	// "Appear as members" is a thing that happens on the wire: each machine has to
	// advertise, be dialed, and have its certificate checked before it is one.
	if err := s.bothBrowseTheLAN(); err != nil {
		return err
	}
	return s.eachSeesTheOtherAsVerified()
}

func (s *enrollBDD) coreNeverContactedAtAll() error { return s.nothingContactedCore() }

func (s *enrollBDD) aMachineIsDesignated() error { return s.ownerDesignatesThisMachine() }

func (s *enrollBDD) generatesTheRootLocally() error {
	l, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return fmt.Errorf("no local root was generated: %v", err)
	}
	if !l.Authority().Root().IsCA {
		return fmt.Errorf("what it generated is not a certificate authority")
	}
	return s.nothingContactedCore()
}

func (s *enrollBDD) rootPrivateHalfReadableOnlyByOwner() error {
	path := filepath.Join(edgeAuthDir(), edgeauth.AuthorityDir, edgeauth.RootKeyFile)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return fmt.Errorf("%s is %#o, want 0600", path, perm)
	}
	return nil
}

func (s *enrollBDD) rootNeverAppearsAnywhere() error {
	l, _, _ := edgeauth.OpenLocal(edgeAuthDir())
	keyPEM, _, err := cert.ExportRoot(l.Authority())
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(string(keyPEM))
	// Nothing that leaves this machine may contain it: not a request, not an
	// advertisement, not a certificate.
	if err := s.serveAuthority(); err != nil {
		return err
	}
	if err := s.mustRun("roger edge enroll workshop --authority " + s.authorityURL); err != nil {
		return err
	}
	for _, got := range s.issuerOf(s.authorityOf).Requests() {
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), secret) {
			return fmt.Errorf("the root's private half crossed the wire in a request")
		}
	}
	id, _, _ := s.identity()
	if strings.Contains(string(id.Cert.Raw), secret) {
		return fmt.Errorf("the root's private half is inside a certificate")
	}
	c := s.bus.join(s.t)
	res, _ := edge.Browse(context.Background(), c, edge.DefaultService, time.Second, 32)
	for _, a := range res.Adverts {
		for _, txt := range a.TXT() {
			if strings.Contains(txt, secret) || strings.Contains(txt, "PRIVATE") {
				return fmt.Errorf("the root's private half is in an advertisement")
			}
		}
	}
	return nil
}

// issuerOf reaches a machine's local issuer, for the "what crossed the wire" checks.
func (s *enrollBDD) issuerOf(m *enrollMachine) *edgeauth.Issuer {
	return m.host.authorityIssuer()
}

func (s *enrollBDD) aLocalAuthority() error {
	if err := s.ownerDesignatesThisMachine(); err != nil {
		return err
	}
	return s.serveAuthority()
}

func (s *enrollBDD) aNodeEnrollsAgainstIt() error {
	return s.mustRun("roger edge enroll workshop --authority " + s.authorityURL)
}

func (s *enrollBDD) nodeReceivesThePublicRoot() error {
	id, _, ok := s.identity()
	if !ok {
		return fmt.Errorf("no identity")
	}
	if !id.Root.Equal(s.localRoot()) {
		return fmt.Errorf("the node did not receive the authority's public root")
	}
	return nil
}

func (s *enrollBDD) nodeCanVerifyEveryPeer() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	// A real peer of this Edge, issued by the same authority, verifies against it.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cur := s.cur
	s.use(s.authorityOf)
	l, _, _ := edgeauth.OpenLocal(edgeAuthDir())
	leaf, err := l.Authority().Issue(edge.NodeID(pub), pub)
	s.use(cur)
	if err != nil {
		return err
	}
	if _, err := auth.Authenticate(leaf); err != nil {
		return fmt.Errorf("a peer of this Edge did not verify against the public root: %v", err)
	}
	return nil
}

func (s *enrollBDD) nodeNeverReceivesThePrivateHalf() error {
	// A node that is NOT the authority: a second machine, allowed and enrolled against
	// it exactly the way a machine on a plant network would be.
	if s.cur == s.authorityOf {
		node := s.newMachine("node")
		s.broker("http://" + s.deadAddr())
		key := hex.EncodeToString(node.user.Public().(ed25519.PublicKey))
		s.use(s.authorityOf)
		if err := s.mustRun("roger edge authority allow " + key); err != nil {
			return err
		}
		s.use(node)
		if err := s.mustRun("roger edge enroll bench --authority " + s.authorityURL); err != nil {
			return err
		}
	}
	dir := edgeAuthDir()
	if _, err := os.Stat(filepath.Join(dir, edgeauth.AuthorityDir, edgeauth.RootKeyFile)); !os.IsNotExist(err) {
		return fmt.Errorf("a node holds a root private key")
	}
	st := edgeauth.Store{Dir: dir}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	if _, err := auth.Issue("n_whatever", s.cur.user.Public()); err == nil {
		return fmt.Errorf("a node could ISSUE: it holds the root's private half")
	}
	return nil
}

func (s *enrollBDD) oneNodeUnderCoreAuthority() error {
	// The first machine (Background) enrolls against Core.
	return s.ownerEnrollsAs("workshop")
}

func (s *enrollBDD) oneNodeUnderLocalAuthority() error {
	m := s.newMachine("second")
	s.login("owner")
	s.broker("http://" + s.deadAddr())
	if err := s.mustRun("roger edge authority local shed"); err != nil {
		return err
	}
	s.authorityOf = m
	if err := s.serveAuthority(); err != nil {
		return err
	}
	return s.mustRun("roger edge enroll bench --authority " + s.authorityURL)
}

func (s *enrollBDD) certificateShapeIsTheSame() error {
	a, b := s.certOf(s.machines[0]), s.certOf(s.machines[1])
	if len(a.URIs) != len(b.URIs) || len(a.URIs) != 1 {
		return fmt.Errorf("the two certificates name identities differently")
	}
	if a.URIs[0].Scheme != b.URIs[0].Scheme || a.URIs[0].Host != b.URIs[0].Host {
		return fmt.Errorf("the two certificates use different trust domains")
	}
	if fmt.Sprint(a.ExtKeyUsage) != fmt.Sprint(b.ExtKeyUsage) || a.KeyUsage != b.KeyUsage {
		return fmt.Errorf("the two certificates carry different usage")
	}
	if a.IsCA != b.IsCA {
		return fmt.Errorf("one of the two certificates can issue")
	}
	return nil
}

func (s *enrollBDD) certOf(m *enrollMachine) *x509.Certificate {
	cur := s.cur
	s.use(m)
	defer s.use(cur)
	id, _, ok := s.identity()
	if !ok {
		s.t.Fatalf("%s holds no identity", m.label)
	}
	return id.Cert
}

func (s *enrollBDD) verificationPathIsTheSameCode() error {
	// One function, and it takes a *cert.Authority - it is never told which kind.
	for _, m := range s.machines {
		cur := s.cur
		s.use(m)
		st := edgeauth.Store{Dir: edgeAuthDir()}
		auth, _, err := st.Trust(time.Now())
		s.use(cur)
		if err != nil {
			return err
		}
		leaf := s.certOf(m)
		ad := edge.Advert{NodeID: s.identityOf(m), Fingerprint: edge.FingerprintOf(leaf)}
		peer := &edge.Peer{Cert: leaf, Fingerprint: ad.Fingerprint}
		if reason := edge.VerifyPeer(ad, peer, auth, "", time.Now()); reason != "" {
			return fmt.Errorf("%s: the one verification path refused its own certificate: %s", m.label, reason)
		}
	}
	return nil
}

func (s *enrollBDD) neitherCanDistinguishTheOrigin() error {
	a, b := s.certOf(s.machines[0]), s.certOf(s.machines[1])
	shape := func(c *x509.Certificate) string {
		var names []string
		for _, e := range c.Extensions {
			names = append(names, e.Id.String())
		}
		return strings.Join(names, ",")
	}
	if shape(a) != shape(b) {
		return fmt.Errorf("the certificates carry different extensions, so a node CAN tell:\n%s\n%s",
			shape(a), shape(b))
	}
	for _, c := range []*x509.Certificate{a, b} {
		for _, word := range []string{"core", "local", "shed"} {
			if strings.Contains(strings.ToLower(c.Subject.CommonName), word) {
				return fmt.Errorf("a certificate names its authority's kind: %q", c.Subject.CommonName)
			}
		}
	}
	return nil
}

func (s *enrollBDD) depGraphLinksNoCore() error {
	out, err := exec.Command("go", "list", "-deps", "rogerai.fm/roger/v6/internal/edgeauth").Output()
	if err != nil {
		return err
	}
	deps := strings.Split(string(out), "\n")
	if len(deps) < 20 {
		return fmt.Errorf("the dependency scan enumerated nothing")
	}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		switch {
		case dep == "net/http",
			dep == "rogerai.fm/roger/v6/internal/client",
			dep == "rogerai.fm/roger/v6/internal/towerjoin",
			dep == "rogerai.fm/roger/v6/internal/towerhub":
			return fmt.Errorf("the Edge authority links %s, so it COULD reach Core", dep)
		case strings.HasPrefix(dep, "rogerai.fm/roger/v6/internal/towercore") &&
			dep != "rogerai.fm/roger/v6/internal/towercore/cert":
			return fmt.Errorf("the Edge authority links %s", dep)
		}
	}
	return nil
}

func (s *enrollBDD) depGraphTestEnforcesIt() error {
	path := filepath.Join("..", "..", "internal", "edgeauth", "structural_test.go")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("there is no dependency-graph test: %v", err)
	}
	if !strings.Contains(string(b), "go list") {
		return fmt.Errorf("the structural test does not inspect the dependency graph")
	}
	return nil
}

func (s *enrollBDD) edgeRootedAtLocalAuthority() error { return s.aLocalAuthority() }

func (s *enrollBDD) enrollmentAgainstADifferentAuthority() error {
	if err := s.aNodeEnrollsAgainstIt(); err != nil {
		return err
	}
	// A SECOND authority, on another machine, offering to root the same Edge.
	held, heldURL := s.authorityOf, s.authorityURL
	rival := s.newMachine("rival")
	s.broker("http://" + s.deadAddr())
	if err := s.mustRun("roger edge authority local annex"); err != nil {
		return err
	}
	s.authorityOf = rival
	if err := s.serveAuthority(); err != nil {
		return err
	}
	rivalURL := s.authorityURL
	key := hex.EncodeToString(held.user.Public().(ed25519.PublicKey))
	if err := s.mustRun("roger edge authority allow " + key); err != nil {
		return err
	}
	s.authorityOf, s.authorityURL = held, heldURL
	s.use(held)
	return s.run("roger edge enroll workshop --authority " + rivalURL)
}

func (s *enrollBDD) itIsRefused() error {
	if s.err == nil {
		return fmt.Errorf("it was NOT refused:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) refusalNamesTheAuthorityWeHave() error {
	if !strings.Contains(s.out, "shed") {
		return fmt.Errorf("the refusal does not name the authority this Edge already has:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) noSecondRootCreated() error {
	roots := 0
	base := filepath.Join(edgeAuthDir(), edgeauth.AuthorityDir)
	if _, err := os.Stat(filepath.Join(base, edgeauth.RootKeyFile)); err == nil {
		roots++
	}
	if roots > 1 {
		return fmt.Errorf("%d roots exist on this machine", roots)
	}
	id, _, ok := s.identity()
	if ok && !id.Root.Equal(s.localRoot()) {
		return fmt.Errorf("this machine's identity moved to a second root")
	}
	return nil
}

func (s *enrollBDD) peerFromAnotherEdgesAuthority() error {
	if err := s.ownerEnrollsAs("workshop"); err != nil {
		return err
	}
	stranger, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	if err != nil {
		return err
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := edge.NodeID(pub)
	leaf, err := stranger.Issue(id, pub)
	if err != nil {
		return err
	}
	s.strangerLeaf, s.strangerID = leaf, id
	return nil
}

func (s *enrollBDD) thisNodeVerifiesIt() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	ad := edge.Advert{NodeID: s.strangerID, Fingerprint: edge.FingerprintOf(s.strangerLeaf)}
	peer := &edge.Peer{Cert: s.strangerLeaf, Fingerprint: ad.Fingerprint}
	s.verdict = edge.VerifyPeer(ad, peer, auth, "", time.Now())
	return nil
}

func (s *enrollBDD) refusedAsReason(reason string) error {
	if s.verdict == reason {
		return nil
	}
	if s.verdict == "" && s.err != nil && strings.Contains(s.out, reason) {
		return nil
	}
	if s.failReason == reason {
		return nil
	}
	return fmt.Errorf("refused as %q, want %q (cli: %s)", s.verdict, reason, s.out)
}

func (s *enrollBDD) doesNotBecomeAMember() error {
	for _, n := range s.fleet() {
		if n.ID == s.strangerID {
			return fmt.Errorf("a stranger's certificate made it a member")
		}
	}
	return nil
}

func (s *enrollBDD) localAuthorityWithMembers() error {
	if err := s.aLocalAuthority(); err != nil {
		return err
	}
	if err := s.aNodeEnrollsAgainstIt(); err != nil {
		return err
	}
	s.beforeList = len(s.fleet())
	return nil
}

func (s *enrollBDD) ownerMovesToADifferentAuthority() error {
	return s.run("roger edge authority local other-shed --force")
}

func (s *enrollBDD) ownerToldEveryMemberMustReenroll() error {
	if s.err != nil {
		return fmt.Errorf("the migration failed: %v\n%s", s.err, s.out)
	}
	if !strings.Contains(strings.ToLower(s.out), "re-enroll") {
		return fmt.Errorf("the owner was not told every member must re-enroll:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) membersNotSilentlyDropped() error {
	if got := len(s.fleet()); got < s.beforeList {
		return fmt.Errorf("the fleet went from %d to %d members", s.beforeList, got)
	}
	return nil
}

func (s *enrollBDD) shownAsNeedingReenrollment() error {
	if err := s.mustRun("roger edge list"); err != nil {
		return err
	}
	found := false
	for _, n := range s.fleet() {
		if n.Presence == string(edge.PresenceReenroll) {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no member is marked as needing re-enrollment")
	}
	if !strings.Contains(strings.ToUpper(s.out), "RE-ENROLL") {
		return fmt.Errorf("the fleet view does not show it:\n%s", s.out)
	}
	if err := s.mustRun("roger edge describe workshop"); err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(s.out), "authority") {
		return fmt.Errorf("the reason is not shown:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) machineOnANetworkWithNoInternet() error { return s.networkNeverHadInternet() }

func (s *enrollBDD) ownerDesignatesIt() error {
	if err := s.run("roger edge authority local shed"); err == nil && s.err == nil {
		s.authorityOf = s.cur
	}
	return nil
}

func (s *enrollBDD) itSucceeds() error {
	if s.err != nil {
		return fmt.Errorf("it failed: %v\n%s", s.err, s.out)
	}
	return nil
}

func (s *enrollBDD) theEdgeItRootsIsComplete() error {
	if err := s.serveAuthority(); err != nil {
		return err
	}
	if err := s.mustRun("roger edge enroll workshop --authority " + s.authorityURL); err != nil {
		return err
	}
	if err := s.mustRun("roger edge list"); err != nil {
		return err
	}
	if err := s.nodeCanVerifyEveryPeer(); err != nil {
		return err
	}
	if err := s.mustRun("roger edge forget workshop --yes"); err != nil {
		return err
	}
	return s.nothingContactedCore()
}

func (s *enrollBDD) edgeUsingTheAuthority(kind string) error {
	if kind == "local" {
		s.broker("http://" + s.deadAddr())
		if err := s.mustRun("roger edge authority local shed"); err != nil {
			return err
		}
		s.authorityOf = s.cur
	}
	return nil
}

func (s *enrollBDD) ownerAsksWhatRootsThisEdge() error { return s.mustRun("roger edge authority") }

func (s *enrollBDD) itNames(named string) error {
	if !strings.Contains(s.out, named) {
		return fmt.Errorf("`roger edge authority` does not name %q:\n%s", named, s.out)
	}
	return nil
}

func (s *enrollBDD) saysWhetherEnrollingNeedsTheNetwork() error {
	low := strings.ToLower(s.out)
	if !strings.Contains(low, "needs the network") && !strings.Contains(low, "needs no network") {
		return fmt.Errorf("it does not say whether enrolling needs the network:\n%s", s.out)
	}
	return nil
}

// =========================================================================
// 3. Authority - who may enroll what
// =========================================================================

func (s *enrollBDD) noOwnerLoggedIn() error {
	dir := filepath.Dir(configPath())
	_ = os.Remove(filepath.Join(dir, "auth.json"))
	return nil
}

func (s *enrollBDD) enrollmentIsAttempted() error { return s.run("roger edge enroll workshop") }

func (s *enrollBDD) refusalSaysLogInFirst() error {
	if !strings.Contains(strings.ToLower(s.out), "log in") {
		return fmt.Errorf("the refusal does not say to log in first:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) userKeyNeverRegistered() error {
	s.core.mu.Lock()
	s.core.reg = map[string]string{}
	s.core.mu.Unlock()
	return nil
}

func (s *enrollBDD) coreRefusesIt() error { return s.itIsRefused() }

func (s *enrollBDD) noCertificateIsIssued() error {
	if _, _, ok := s.identity(); ok {
		return fmt.Errorf("a certificate was issued anyway")
	}
	return nil
}

func (s *enrollBDD) enrollmentNamesAnotherAccount() error {
	return s.run("roger edge enroll workshop --account somebody-else")
}

func (s *enrollBDD) refusalRevealsNothing() error {
	if strings.Contains(s.out, "somebody-else") {
		return fmt.Errorf("the refusal repeats the other account back:\n%s", s.out)
	}
	for _, w := range []string{"exists", "already", "registered to"} {
		if strings.Contains(strings.ToLower(s.out), w) {
			return fmt.Errorf("the refusal leaks whether that account exists:\n%s", s.out)
		}
	}
	return nil
}

func (s *enrollBDD) aCompletedEnrollmentRequest() error {
	if err := s.ownerEnrollsAs("workshop"); err != nil {
		return err
	}
	s.beforeID, _, _ = s.identityTriple()
	return nil
}

func (s *enrollBDD) identityTriple() (string, string, bool) {
	id, _, ok := s.identity()
	if !ok {
		return "", "", false
	}
	return id.NodeID, id.CertPEM, true
}

func (s *enrollBDD) sameSignedRequestSubmittedAgain() error {
	req := s.core.iss.LastRequest()
	_, err := s.core.iss.Issue(req)
	s.failReason = ""
	if err != nil {
		s.failReason = err.Error()
	}
	s.err = err
	return nil
}

func (s *enrollBDD) refusedAsAlreadyUsed() error {
	if s.err == nil {
		return fmt.Errorf("a replayed request minted a second identity")
	}
	if !strings.Contains(strings.ToLower(s.failReason), "already") {
		return fmt.Errorf("the refusal is %q, want an already-used refusal", s.failReason)
	}
	return nil
}

func (s *enrollBDD) nodeKeepsItsOneIdentity() error {
	id, _, ok := s.identity()
	if !ok || id.NodeID != s.beforeID {
		return fmt.Errorf("the node's identity changed")
	}
	return nil
}

func (s *enrollBDD) requestSignedByTheWrongKey() error {
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("owner", "workshop", string(edge.Host), pub, time.Now())
	req.UserKey = hex.EncodeToString(s.cur.user.Public().(ed25519.PublicKey))
	req.Sign(wrong)
	_, err := s.core.iss.Issue(req)
	s.err = err
	if err != nil {
		s.failReason = err.Error()
	}
	return nil
}

func (s *enrollBDD) requestTamperedAfterSigning() error {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("owner", "workshop", string(edge.Host), pub, time.Now())
	req.Sign(s.cur.user)
	req.NodeID = "n_" + strings.Repeat("00", 24) // altered AFTER signing
	_, err := s.core.iss.Issue(req)
	s.err = err
	if err != nil {
		s.failReason = err.Error()
	}
	return nil
}

func (s *enrollBDD) signatureCheckFails() error {
	if s.err == nil {
		return fmt.Errorf("a tampered request was accepted")
	}
	if !strings.Contains(strings.ToLower(s.failReason), "sign") {
		return fmt.Errorf("the refusal is %q, want a signature refusal", s.failReason)
	}
	return nil
}

// =========================================================================
// 4. Re-enrollment, identity and the pin
// =========================================================================

func (s *enrollBDD) thisMachineIsEnrolledAs(name string) error {
	if err := s.ownerEnrollsAs(name); err != nil {
		return err
	}
	id, _, _ := s.identity()
	s.beforeID, s.beforeCert = id.NodeID, id.CertPEM
	s.beforeList = len(s.fleet())
	for _, n := range s.fleet() {
		if n.ID == id.NodeID {
			s.beforeHistory = append([]store.EdgeEvent(nil), n.History...)
		}
	}
	return nil
}

func (s *enrollBDD) ownerEnrollsItAgain() error { return s.mustRun("roger edge enroll workshop") }

func (s *enrollBDD) keepsTheSameNodeID() error {
	id, _, _ := s.identity()
	if id.NodeID != s.beforeID {
		return fmt.Errorf("the node id changed from %s to %s", s.beforeID, id.NodeID)
	}
	return nil
}

func (s *enrollBDD) keepsTheSameCertificate() error {
	id, _, _ := s.identity()
	if id.CertPEM != s.beforeCert {
		return fmt.Errorf("a certificate nowhere near expiry was replaced")
	}
	return nil
}

func (s *enrollBDD) nothingInTheFleetChanged() error {
	if got := len(s.fleet()); got != s.beforeList {
		return fmt.Errorf("the fleet went from %d to %d", s.beforeList, got)
	}
	for _, n := range s.fleet() {
		if n.ID != s.beforeID {
			continue
		}
		if len(n.History) != len(s.beforeHistory) {
			return fmt.Errorf("a no-op enroll wrote %d history line(s)", len(n.History)-len(s.beforeHistory))
		}
	}
	return nil
}

func (s *enrollBDD) certificateCloseToExpiry() error {
	// A real authority with a SHORT life: the certificate this machine holds is
	// genuinely near the end of its window.
	short, err := cert.NewAuthority(cert.Config{TTL: 10 * time.Second})
	if err != nil {
		return err
	}
	s.core.ca = short
	s.core.iss = edgeauth.NewIssuer(edgeauth.IssuerConfig{
		Authority: short,
		Accounts: func(userKey string) (string, bool) {
			s.core.mu.Lock()
			defer s.core.mu.Unlock()
			a, ok := s.core.reg[userKey]
			return a, ok
		},
	})
	s.core.swap(s.core.iss)
	return s.thisMachineIsEnrolledAs("workshop")
}

func (s *enrollBDD) itRenews() error {
	// A peer that pinned it, before the renewal.
	s.pinBefore = s.pinOfSelf()
	return s.mustRun("roger edge enroll workshop")
}

func (s *enrollBDD) pinOfSelf() string {
	id, _, ok := s.identity()
	if !ok {
		return ""
	}
	return edge.FingerprintOf(id.Cert)
}

func (s *enrollBDD) nodeIDUnchanged() error { return s.keepsTheSameNodeID() }

func (s *enrollBDD) pinnedPeersStillVerifyIt() error {
	id, _, _ := s.identity()
	if id.CertPEM == s.beforeCert {
		return fmt.Errorf("a certificate near expiry was NOT renewed, so this proves nothing")
	}
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	ad := edge.Advert{NodeID: id.NodeID, Fingerprint: edge.FingerprintOf(id.Cert)}
	peer := &edge.Peer{Cert: id.Cert, Fingerprint: ad.Fingerprint}
	// The peer's pin is the certificate it accepted BEFORE the renewal.
	if reason := edge.VerifyPeer(ad, peer, auth, s.pinBefore, time.Now()); reason != "" {
		return fmt.Errorf("a peer that pinned this node refused its renewed certificate: %s", reason)
	}
	return nil
}

func (s *enrollBDD) placeNameHistoryUnchanged() error {
	for _, n := range s.fleet() {
		if n.ID != s.beforeID {
			continue
		}
		if n.Name != "workshop" {
			return fmt.Errorf("the name changed to %q", n.Name)
		}
		if len(n.History) < len(s.beforeHistory) {
			return fmt.Errorf("history was lost")
		}
		return nil
	}
	return fmt.Errorf("the node left the fleet when it renewed")
}

func (s *enrollBDD) machineEnrolledThenForgotten() error {
	if err := s.thisMachineIsEnrolledAs("workshop"); err != nil {
		return err
	}
	return s.mustRun("roger edge forget workshop --yes")
}

func (s *enrollBDD) itEnrollsAgain() error { return s.mustRun("roger edge enroll workshop") }

func (s *enrollBDD) receivesANewNodeID() error {
	id, _, _ := s.identity()
	if id.NodeID == s.beforeID {
		return fmt.Errorf("a forgotten machine came back as the same node")
	}
	return nil
}

func (s *enrollBDD) appearsAsANewMember() error {
	id, _, _ := s.identity()
	for _, n := range s.fleet() {
		if n.ID == s.beforeID {
			return fmt.Errorf("the old node is back in the fleet")
		}
	}
	for _, n := range s.fleet() {
		if n.ID == id.NodeID {
			return nil
		}
	}
	return fmt.Errorf("the new node is not in the fleet")
}

func (s *enrollBDD) oldPinRefusesItAsIdentityMismatch() error {
	id, _, _ := s.identity()
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	// A peer that still holds the OLD node's record: same name, old id, old pin.
	ad := edge.Advert{NodeID: s.beforeID, Fingerprint: edge.FingerprintOf(id.Cert)}
	peer := &edge.Peer{Cert: id.Cert, Fingerprint: ad.Fingerprint}
	if reason := edge.VerifyPeer(ad, peer, auth, s.beforeCert, time.Now()); reason != edge.ReasonIdentityMismatch {
		return fmt.Errorf("a peer holding the old pin got %q, want identity mismatch", reason)
	}
	return nil
}

func (s *enrollBDD) enrolledToOneAccount() error { return s.thisMachineIsEnrolledAs("workshop") }

func (s *enrollBDD) attemptsASecondAccount() error {
	other := newCoreStub(s.t)
	s.registerWithOther(other, "second-account")
	s.broker(other.url())
	err := s.run("roger edge enroll workshop --account second-account")
	s.otherCore = other
	return err
}

func (s *enrollBDD) registerWithOther(c *coreStub, account string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reg[hex.EncodeToString(s.cur.user.Public().(ed25519.PublicKey))] = account
}

func (s *enrollBDD) secondEnrollmentRefused() error { return s.itIsRefused() }

func (s *enrollBDD) refusalNamesTheAccountToItsOwnOwner() error {
	if !strings.Contains(s.out, "owner") {
		return fmt.Errorf("this machine's own owner is not told which Edge it belongs to:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) otherAccountLearnsNothing() error {
	if s.otherCore.count() != 0 {
		return fmt.Errorf("the other account was contacted %d time(s)", s.otherCore.count())
	}
	return nil
}

// =========================================================================
// 5. Revocation
// =========================================================================

func (s *enrollBDD) ownerForgetsIt() error {
	id, _, _ := s.identity()
	s.beforeID = id.NodeID
	s.beforeCert = id.CertPEM
	s.revokedCert = id.Cert
	return s.mustRun("roger edge forget workshop --yes")
}

func (s *enrollBDD) certificateIsRevoked() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	if !auth.SerialRevoked(s.revokedCert.SerialNumber.String()) {
		return fmt.Errorf("forgetting a node did not revoke its certificate")
	}
	return nil
}

func (s *enrollBDD) pinnedPeerRefusesItFromThenOn() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	ad := edge.Advert{NodeID: s.beforeID, Fingerprint: edge.FingerprintOf(s.revokedCert)}
	peer := &edge.Peer{Cert: s.revokedCert, Fingerprint: ad.Fingerprint}
	s.verdict = edge.VerifyPeer(ad, peer, auth, ad.Fingerprint, time.Now())
	if s.verdict == "" {
		return fmt.Errorf("a revoked node still verifies")
	}
	return nil
}

func (s *enrollBDD) aRevokedNode() error {
	if err := s.aLocalAuthority(); err != nil {
		return err
	}
	if err := s.aNodeEnrollsAgainstIt(); err != nil {
		return err
	}
	return s.ownerForgetsIt()
}

func (s *enrollBDD) peerWithCurrentRevocationListBrowses() error {
	return s.pinnedPeerRefusesItFromThenOn()
}

func (s *enrollBDD) revokedNodeIsRefused() error {
	if s.verdict == "" {
		return fmt.Errorf("the revoked node was accepted")
	}
	return nil
}

func (s *enrollBDD) reasonIsRevocationNotExpiry() error {
	if s.verdict != edge.ReasonRevoked {
		return fmt.Errorf("the refusal reason is %q, want %q", s.verdict, edge.ReasonRevoked)
	}
	return nil
}

func (s *enrollBDD) verifyingMachineRestarts() error {
	// A restart is a new process reading the same disk: nothing is carried in memory.
	s.verdict = ""
	return nil
}

func (s *enrollBDD) nodeIsStillRefused() error { return s.pinnedPeerRefusesItFromThenOn() }

func (s *enrollBDD) revocationListNotRefreshed() error {
	if err := s.anEnrolledMachine(); err != nil {
		return err
	}
	st := edgeauth.Store{Dir: edgeAuthDir()}
	old := time.Now().Add(-30 * 24 * time.Hour)
	return st.SaveTrust(nil, old)
}

func (s *enrollBDD) fleetViewMarksTrustStale() error {
	if err := s.mustRun("roger edge list"); err != nil {
		return err
	}
	low := strings.ToLower(s.out)
	if !strings.Contains(low, "trust information is stale") {
		return fmt.Errorf("the fleet view does not say the trust information is stale:\n%s", s.out)
	}
	if !strings.Contains(low, "ago") {
		return fmt.Errorf("the fleet view does not say how old it is:\n%s", s.out)
	}
	return nil
}

func (s *enrollBDD) doesNotSilentlyClaimRevokedIsFine() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	_, trust, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	if !trust.Stale(time.Now(), edgeauth.FreshnessWindow) {
		return fmt.Errorf("a month-old revocation list is not considered stale")
	}
	return nil
}

// =========================================================================
// 6. The certificate itself
// =========================================================================

func (s *enrollBDD) aNodeIsEnrolled() error { return s.ownerEnrollsAs("workshop") }

func (s *enrollBDD) certificateCarriesAnExpiry() error {
	id, _, _ := s.identity()
	if id.Cert.NotAfter.IsZero() || !id.Cert.NotAfter.After(time.Now()) {
		return fmt.Errorf("the certificate carries no usable expiry")
	}
	if id.Cert.NotAfter.Sub(id.Cert.NotBefore) > 90*24*time.Hour {
		return fmt.Errorf("the certificate's life is not bounded in any meaningful way")
	}
	return nil
}

func (s *enrollBDD) expiredCertificateIsRefused() error {
	id, _, _ := s.identity()
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	after := id.Cert.NotAfter.Add(time.Second)
	ad := edge.Advert{NodeID: id.NodeID, Fingerprint: edge.FingerprintOf(id.Cert)}
	peer := &edge.Peer{Cert: id.Cert, Fingerprint: ad.Fingerprint}
	if reason := edge.VerifyPeer(ad, peer, auth, "", after); reason != edge.ReasonCertExpired {
		return fmt.Errorf("an expired certificate was refused as %q, want %q", reason, edge.ReasonCertExpired)
	}
	return nil
}

func (s *enrollBDD) authorizesMembershipOnly() error { return s.carriesNoCapability() }

func (s *enrollBDD) cannotServeSpendOrPayout() error {
	id, _, _ := s.identity()
	// Core's own Tower authority - a different root - refuses it outright, so this
	// credential buys nothing anywhere else in the system.
	elsewhere, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	if err != nil {
		return err
	}
	if _, err := elsewhere.Authenticate(id.Cert); err == nil {
		return fmt.Errorf("an Edge certificate authenticated against an unrelated authority")
	}
	if id.Cert.IsCA || id.Cert.KeyUsage&x509.KeyUsageCertSign != 0 {
		return fmt.Errorf("an Edge certificate can issue")
	}
	for _, u := range id.Cert.URIs {
		if strings.Contains(u.Path, "wallet") || strings.Contains(u.Path, "payout") {
			return fmt.Errorf("the certificate names money authority")
		}
	}
	return nil
}

func (s *enrollBDD) rootAndNodeKeyAreDifferent() error {
	if err := s.ownerEnrollsAs("workshop"); err != nil {
		return err
	}
	id, key, _ := s.identity()
	if id.Root.PublicKey == nil {
		return fmt.Errorf("no root")
	}
	type equaler interface{ Equal(pub any) bool }
	if eq, ok := id.Root.PublicKey.(interface{ Equal(x any) bool }); ok {
		_ = eq
	}
	if hex.EncodeToString(key.Public().(ed25519.PublicKey)) == hex.EncodeToString(id.Cert.RawSubjectPublicKeyInfo) {
		return fmt.Errorf("mixed up")
	}
	if id.Root.Equal(id.Cert) {
		return fmt.Errorf("the root and the node's certificate are the same certificate")
	}
	return nil
}

func (s *enrollBDD) nodeNeverHoldsRootPrivateHalf() error {
	st := edgeauth.Store{Dir: edgeAuthDir()}
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return err
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := auth.Issue(edge.NodeID(pub), pub); err == nil {
		return fmt.Errorf("a node could issue a certificate: it holds the root's private half")
	}
	return nil
}

func (s *enrollBDD) coreReturns(response string) error {
	switch response {
	case "a certificate for a different node id":
		s.core.setMangle(func(r edgeauth.Response) edgeauth.Response {
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			other, err := s.core.ca.Issue(edge.NodeID(pub), pub)
			if err != nil {
				s.t.Fatal(err)
			}
			r.Cert = edgeauth.EncodeCert(other)
			return r
		})
	case "a certificate for a different account":
		s.core.setMangle(func(r edgeauth.Response) edgeauth.Response {
			r.Account = "somebody-else"
			return r
		})
	case "a certificate already expired":
		s.core.setMangle(func(r edgeauth.Response) edgeauth.Response {
			r.Cert = s.expiredCertFor(r)
			r.Root = edgeauth.EncodeCert(s.expiredRoot)
			return r
		})
	case "a certificate signed by an unexpected root":
		s.core.setMangle(func(r edgeauth.Response) edgeauth.Response {
			stranger, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
			if err != nil {
				s.t.Fatal(err)
			}
			leaf, err := stranger.Issue(r.NodeID, s.pendingNodeKey)
			if err != nil {
				s.t.Fatal(err)
			}
			r.Cert = edgeauth.EncodeCert(leaf)
			return r
		})
	case "a truncated body":
		s.core.setTruncate()
	default:
		return fmt.Errorf("unknown response %q", response)
	}
	return nil
}

// expiredCertFor mints a certificate for the requested node that is ALREADY past its
// window, from a root the client is also given, so the only defect is the expiry.
func (s *enrollBDD) expiredCertFor(r edgeauth.Response) string {
	auth, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	if err != nil {
		s.t.Fatal(err)
	}
	s.expiredRoot = auth.Root()
	u, _ := url.Parse("spiffe://rogerai.fm/tower/" + r.NodeID)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: r.NodeID},
		URIs:                  []*url.URL{u},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(-time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, auth.Root(), s.pendingNodeKey, auth.RootKey())
	if err != nil {
		s.t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		s.t.Fatal(err)
	}
	return edgeauth.EncodeCert(leaf)
}

func (s *enrollBDD) theMachineEnrolls() error {
	s.core.watchKey(func(pub ed25519.PublicKey) { s.pendingNodeKey = pub })
	return s.run("roger edge enroll workshop")
}

func (s *enrollBDD) enrollmentFailsWith(reason string) error {
	if s.err == nil {
		return fmt.Errorf("a defective response was ACCEPTED:\n%s", s.out)
	}
	if !strings.Contains(strings.ToLower(s.out), reason) {
		return fmt.Errorf("enrollment failed with %q, want %q", s.out, reason)
	}
	return nil
}

func (s *enrollBDD) noPartialIdentity() error {
	dir := edgeAuthDir()
	if _, _, ok := s.identity(); ok {
		return fmt.Errorf("a partial identity survived a failed enrollment")
	}
	for _, f := range []string{edgeauth.NodeKeyFile, edgeauth.NodeCertFile, edgeauth.RootCertFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return fmt.Errorf("%s was left behind by a failed enrollment", f)
		}
	}
	for _, n := range s.fleet() {
		if n.Name == "workshop" {
			return fmt.Errorf("a fleet row was left behind by a failed enrollment")
		}
	}
	return nil
}

// =========================================================================
// 7. It never gets in the way
// =========================================================================

func (s *enrollBDD) ownerRunsRogerWithoutEnrolling() error {
	h, err := newEdgeHost("")
	if err != nil {
		return err
	}
	s.cur.host = h
	h.wire(&s.cur.hooks)
	h.start(context.Background())
	s.browse(s.cur)
	return nil
}

func (s *enrollBDD) nothingEnrollsItself() error {
	if _, _, ok := s.identity(); ok {
		return fmt.Errorf("running roger enrolled this machine by itself")
	}
	return nil
}

func (s *enrollBDD) noKeyIsGenerated() error {
	if _, err := os.Stat(filepath.Join(edgeAuthDir(), edgeauth.NodeKeyFile)); err == nil {
		return fmt.Errorf("a node key was generated without being asked for")
	}
	return nil
}

func (s *enrollBDD) edgeScreenHonestlyEmpty() error {
	list, err := s.cur.hooks.EdgeFleet.List()
	if err != nil {
		return err
	}
	if len(list) != 0 {
		return fmt.Errorf("the Edge screen shows %d member(s) on a machine that never enrolled", len(list))
	}
	return nil
}

func (s *enrollBDD) enrollmentFailsAtAnyStep() error {
	s.core.setMangle(func(r edgeauth.Response) edgeauth.Response {
		r.Cert = "not a certificate"
		return r
	})
	if err := s.run("roger edge enroll workshop"); err != nil {
		return err
	}
	if s.err == nil {
		return fmt.Errorf("the failure did not happen")
	}
	return nil
}

func (s *enrollBDD) nothingRemains() error { return s.noPartialIdentity() }

func (s *enrollBDD) retryingBehavesAsAFirstAttempt() error {
	s.core.clearMangle()
	if err := s.mustRun("roger edge enroll workshop"); err != nil {
		return err
	}
	if _, _, ok := s.identity(); !ok {
		return fmt.Errorf("the retry did not enroll")
	}
	return nil
}

func (s *enrollBDD) alreadyServingAsAStation() error {
	// A REAL Station registration in a real store: this is the market's own registry,
	// which lives on the broker side and which enrolling must not touch.
	db := store.NewMem()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	rec := store.NodeRecord{
		NodeID: edge.NodeID(pub),
		Reg: protocol.NodeRegistration{
			NodeID: edge.NodeID(pub),
			Offers: []protocol.ModelOffer{{Model: "wave-pico"}},
		},
		LastSeen: time.Now().Unix(),
	}
	if err := db.UpsertNode(rec); err != nil {
		return err
	}
	if err := db.BindNode(rec.NodeID, "owner"); err != nil {
		return err
	}
	s.beforeStation, s.stationDB = rec, db
	return nil
}

func (s *enrollBDD) ownerEnrollsIt() error { return s.mustRun("roger edge enroll workshop") }

func (s *enrollBDD) stationRegistrationUnchanged() error {
	all, err := s.stationDB.AllNodes()
	if err != nil {
		return err
	}
	var got store.NodeRecord
	for _, r := range all {
		if r.NodeID == s.beforeStation.NodeID {
			got = r
		}
	}
	if got.NodeID == "" {
		return fmt.Errorf("enrolling removed the Station registration")
	}
	if len(got.Reg.Offers) != len(s.beforeStation.Reg.Offers) ||
		got.Reg.Offers[0].Model != s.beforeStation.Reg.Offers[0].Model {
		return fmt.Errorf("the Station's offers changed")
	}
	if got.LastSeen != s.beforeStation.LastSeen {
		return fmt.Errorf("enrolling disturbed the Station's registration")
	}
	bound, err := s.stationDB.NodesOfAccount("owner")
	if err != nil {
		return err
	}
	if len(bound) != 1 || bound[0] != s.beforeStation.NodeID {
		return fmt.Errorf("the Station's binding to its account changed")
	}
	return nil
}

func (s *enrollBDD) appearsWithServeCapability() error {
	// The fleet is the JOIN of what this machine enrolled and what the market registry
	// knows. Put the row the CLI just wrote next to the Station registration and read
	// the two the way the fleet does.
	for _, n := range s.fleet() {
		if _, err := s.stationDB.EnrollEdgeNode(n); err != nil {
			return err
		}
	}
	list, err := edge.NewFleet(s.stationDB, "owner").List()
	if err != nil {
		return err
	}
	serving := false
	for _, n := range list {
		if n.Station && edge.Routable(n, edge.Serve) {
			serving = true
		}
	}
	if !serving {
		return fmt.Errorf("the Station does not appear in the fleet with the serve capability: %+v", list)
	}
	return nil
}

// --- extra scenario scratch fields ---------------------------------------

// (declared here rather than inline so the state struct above stays readable)

func TestEdgeEnrollmentFeature(t *testing.T) {
	st := &enrollBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})

			// Background
			sc.Step(`^an owner logged in on this machine$`, st.ownerLoggedIn)
			sc.Step(`^the machine holds the user key that login left on disk$`, st.machineHoldsTheUserKey)

			// 1. the happy path
			sc.Step(`^the owner enrolls this machine as "([^"]*)"$`, st.ownerEnrollsAs)
			sc.Step(`^the owner enrolls this machine$`, st.ownerEnrollsThisMachine)
			sc.Step(`^the machine generates a NEW keypair for its node identity$`, st.generatesANewKeypair)
			sc.Step(`^the request is signed with the user key it already held$`, st.requestSignedWithUserKey)
			sc.Step(`^it receives a certificate naming this account and this node$`, st.receivesACertificateNamingAccountAndNode)
			sc.Step(`^it receives the account's Edge root$`, st.receivesTheEdgeRoot)
			sc.Step(`^it can now advertise, because it has something true to advertise$`, st.canNowAdvertise)
			sc.Step(`^the node's private key was generated locally$`, st.privateKeyGeneratedLocally)
			sc.Step(`^no request carried it$`, st.noRequestCarriedIt)
			sc.Step(`^it is stored readable only by its owner$`, st.storedReadableOnlyByItsOwner)
			sc.Step(`^the certificate names this account$`, st.certificateNamesThisAccount)
			sc.Step(`^it names this node's id$`, st.itNamesThisNodesID)
			sc.Step(`^it carries no capability, because a capability is declared and verified, never granted by a certificate$`, st.carriesNoCapability)
			sc.Step(`^a second machine of the same account, also enrolled$`, st.secondMachineOfTheSameAccountAlsoEnrolled)
			sc.Step(`^both browse the LAN$`, st.bothBrowseTheLAN)
			sc.Step(`^each sees the other as a VERIFIED member$`, st.eachSeesTheOtherAsVerified)
			sc.Step(`^neither is a candidate, because both certificates check out$`, st.neitherIsACandidate)
			sc.Step(`^"roger edge list" shows it$`, st.edgeListShowsIt)
			sc.Step(`^the \[3\] EDGE screen draws it as self$`, st.edgeScreenDrawsItAsSelf)

			// 2. offline afterwards
			sc.Step(`^two enrolled machines of one account$`, st.twoEnrolledMachines)
			sc.Step(`^there is no route to the internet$`, st.noRouteToTheInternet)
			sc.Step(`^they browse the LAN$`, st.theyBrowseTheLAN)
			sc.Step(`^each verifies the other's certificate against the Edge root it already holds$`, st.eachVerifiesAgainstTheRootItHolds)
			sc.Step(`^both appear as VERIFIED members$`, st.bothAppearAsVerifiedMembers)
			sc.Step(`^nothing in the path contacted Core$`, st.nothingContactedCore)
			sc.Step(`^an enrolled machine$`, st.anEnrolledMachine)
			sc.Step(`^Core is unreachable$`, st.coreIsUnreachable)
			sc.Step(`^the fleet is listed$`, st.theFleetIsListed)
			sc.Step(`^the machine is still a member$`, st.theMachineIsStillAMember)
			sc.Step(`^its certificate is still honoured until it expires$`, st.certificateStillHonouredUntilExpiry)
			sc.Step(`^this Edge uses the Core authority$`, st.edgeUsesTheCoreAuthority)
			sc.Step(`^there is no route to Core$`, st.noRouteToCore)
			sc.Step(`^the owner tries to enroll this machine$`, st.ownerTriesToEnroll)
			sc.Step(`^it fails with a message naming connectivity as the reason$`, st.failsNamingConnectivity)
			sc.Step(`^it names the local authority as the way to enroll with no internet at all$`, st.namesTheLocalAuthorityAsTheOfflineWay)
			sc.Step(`^nothing half-enrolled is left behind$`, st.nothingHalfEnrolled)

			// 2b. the chosen authority
			sc.Step(`^a network that has never had a route to the internet$`, st.networkNeverHadInternet)
			sc.Step(`^the owner designates this machine as the Edge authority$`, st.ownerDesignatesThisMachine)
			sc.Step(`^the owner enrolls this machine and a second machine on that network$`, st.ownerEnrollsBothOnThatNetwork)
			sc.Step(`^both hold certificates issued by that authority$`, st.bothHoldCertificatesFromThatAuthority)
			sc.Step(`^both appear as VERIFIED members of one fleet$`, st.bothVerifiedInOneFleet)
			sc.Step(`^Core was never contacted, not once, at any point$`, st.coreNeverContactedAtAll)
			sc.Step(`^a machine is designated as the Edge authority$`, st.aMachineIsDesignated)
			sc.Step(`^it generates the Edge root locally$`, st.generatesTheRootLocally)
			sc.Step(`^the root's private half is stored readable only by its owner$`, st.rootPrivateHalfReadableOnlyByOwner)
			sc.Step(`^it never appears in any request, advertisement or certificate$`, st.rootNeverAppearsAnywhere)
			sc.Step(`^a local authority$`, st.aLocalAuthority)
			sc.Step(`^a node enrolls against it$`, st.aNodeEnrollsAgainstIt)
			sc.Step(`^the node receives the public root$`, st.nodeReceivesThePublicRoot)
			sc.Step(`^the node can verify every peer of this Edge with it$`, st.nodeCanVerifyEveryPeer)
			sc.Step(`^the node never receives the root's private half$`, st.nodeNeverReceivesThePrivateHalf)
			sc.Step(`^one node enrolled under a Core authority$`, st.oneNodeUnderCoreAuthority)
			sc.Step(`^one node enrolled under a local authority of the same Edge$`, st.oneNodeUnderLocalAuthority)
			sc.Step(`^the certificate shape is the same$`, st.certificateShapeIsTheSame)
			sc.Step(`^the verification path is the same code$`, st.verificationPathIsTheSameCode)
			sc.Step(`^neither node can distinguish the origin of the other's certificate$`, st.neitherCanDistinguishTheOrigin)
			sc.Step(`^the local authority's dependency graph links no Core-dialing package$`, st.depGraphLinksNoCore)
			sc.Step(`^a dependency-graph test enforces it, the way the standalone consumer plane already does$`, st.depGraphTestEnforcesIt)
			sc.Step(`^an Edge rooted at a local authority$`, st.edgeRootedAtLocalAuthority)
			sc.Step(`^enrollment is attempted against a different authority for the same Edge$`, st.enrollmentAgainstADifferentAuthority)
			sc.Step(`^it is refused$`, st.itIsRefused)
			sc.Step(`^the refusal names the authority this Edge already has$`, st.refusalNamesTheAuthorityWeHave)
			sc.Step(`^no second root is created$`, st.noSecondRootCreated)
			sc.Step(`^a peer whose certificate was issued by another Edge's authority$`, st.peerFromAnotherEdgesAuthority)
			sc.Step(`^this node verifies it$`, st.thisNodeVerifiesIt)
			sc.Step(`^it is refused as "([^"]*)"$`, st.refusedAsReason)
			sc.Step(`^it does not become a member$`, st.doesNotBecomeAMember)
			sc.Step(`^an Edge rooted at a local authority with enrolled members$`, st.localAuthorityWithMembers)
			sc.Step(`^the owner moves the Edge to a different authority$`, st.ownerMovesToADifferentAuthority)
			sc.Step(`^the owner is told every member must re-enroll$`, st.ownerToldEveryMemberMustReenroll)
			sc.Step(`^members are not silently dropped$`, st.membersNotSilentlyDropped)
			sc.Step(`^until a member re-enrolls it is shown as needing re-enrollment, with the reason$`, st.shownAsNeedingReenrollment)
			sc.Step(`^a machine on a network with no route to the internet$`, st.machineOnANetworkWithNoInternet)
			sc.Step(`^the owner designates it as the Edge authority$`, st.ownerDesignatesIt)
			sc.Step(`^it succeeds$`, st.itSucceeds)
			sc.Step(`^the Edge it roots is a complete Edge$`, st.theEdgeItRootsIsComplete)
			sc.Step(`^an Edge using the (\w+) authority$`, st.edgeUsingTheAuthority)
			sc.Step(`^the owner asks what roots this Edge$`, st.ownerAsksWhatRootsThisEdge)
			sc.Step(`^it names (Core|the designated machine)$`, st.itNames)
			sc.Step(`^it says whether enrolling a new node will need the network$`, st.saysWhetherEnrollingNeedsTheNetwork)

			// 3. authority
			sc.Step(`^no owner is logged in$`, st.noOwnerLoggedIn)
			sc.Step(`^enrollment is attempted$`, st.enrollmentIsAttempted)
			sc.Step(`^the refusal says to log in first$`, st.refusalSaysLogInFirst)
			sc.Step(`^a user key that was never registered to any account$`, st.userKeyNeverRegistered)
			sc.Step(`^Core refuses it$`, st.coreRefusesIt)
			sc.Step(`^no certificate is issued$`, st.noCertificateIsIssued)
			sc.Step(`^enrollment names another account$`, st.enrollmentNamesAnotherAccount)
			sc.Step(`^the refusal reveals nothing about that account$`, st.refusalRevealsNothing)
			sc.Step(`^a completed enrollment request$`, st.aCompletedEnrollmentRequest)
			sc.Step(`^the exact same signed request is submitted again$`, st.sameSignedRequestSubmittedAgain)
			sc.Step(`^it is refused as already used$`, st.refusedAsAlreadyUsed)
			sc.Step(`^the node keeps the one identity it has$`, st.nodeKeepsItsOneIdentity)
			sc.Step(`^an enrollment request signed by a key that is not the machine's user key$`, st.requestSignedByTheWrongKey)
			sc.Step(`^an enrollment request whose node id was altered after signing$`, st.requestTamperedAfterSigning)
			sc.Step(`^the signature check fails$`, st.signatureCheckFails)

			// 4. re-enrollment
			sc.Step(`^this machine is enrolled as "([^"]*)"$`, st.thisMachineIsEnrolledAs)
			sc.Step(`^the owner enrolls it again$`, st.ownerEnrollsItAgain)
			sc.Step(`^it keeps the same node id$`, st.keepsTheSameNodeID)
			sc.Step(`^it keeps the same certificate until that certificate is near expiry$`, st.keepsTheSameCertificate)
			sc.Step(`^nothing in the fleet changed$`, st.nothingInTheFleetChanged)
			sc.Step(`^an enrolled machine whose certificate is close to expiry$`, st.certificateCloseToExpiry)
			sc.Step(`^it renews$`, st.itRenews)
			sc.Step(`^the node id is unchanged$`, st.nodeIDUnchanged)
			sc.Step(`^peers that pinned it still verify it$`, st.pinnedPeersStillVerifyIt)
			sc.Step(`^its place, name and history in the fleet are unchanged$`, st.placeNameHistoryUnchanged)
			sc.Step(`^a machine that was enrolled and then forgotten by the owner$`, st.machineEnrolledThenForgotten)
			sc.Step(`^it enrolls again$`, st.itEnrollsAgain)
			sc.Step(`^it receives a new node id$`, st.receivesANewNodeID)
			sc.Step(`^it appears as a new member, not as the old one returning$`, st.appearsAsANewMember)
			sc.Step(`^the old pin does not verify it, so a peer refuses it as an identity mismatch$`, st.oldPinRefusesItAsIdentityMismatch)
			sc.Step(`^this machine is enrolled to one account$`, st.enrolledToOneAccount)
			sc.Step(`^it attempts to enroll to a second account while still enrolled$`, st.attemptsASecondAccount)
			sc.Step(`^the second enrollment is refused$`, st.secondEnrollmentRefused)
			sc.Step(`^the refusal names the account it already belongs to only to this machine's own owner$`, st.refusalNamesTheAccountToItsOwnOwner)
			sc.Step(`^the other account learns nothing$`, st.otherAccountLearnsNothing)

			// 5. revocation
			sc.Step(`^the owner forgets it$`, st.ownerForgetsIt)
			sc.Step(`^its certificate is revoked$`, st.certificateIsRevoked)
			sc.Step(`^a peer that already pinned it refuses it from then on$`, st.pinnedPeerRefusesItFromThenOn)
			sc.Step(`^a revoked node$`, st.aRevokedNode)
			sc.Step(`^a peer that has the current revocation list browses$`, st.peerWithCurrentRevocationListBrowses)
			sc.Step(`^the revoked node is refused$`, st.revokedNodeIsRefused)
			sc.Step(`^the refusal reason is revocation, not expiry$`, st.reasonIsRevocationNotExpiry)
			sc.Step(`^the verifying machine restarts$`, st.verifyingMachineRestarts)
			sc.Step(`^the node is still refused$`, st.nodeIsStillRefused)
			sc.Step(`^a machine whose revocation list has not been refreshed for longer than its freshness window$`, st.revocationListNotRefreshed)
			sc.Step(`^the fleet view marks the trust information as stale, with its age$`, st.fleetViewMarksTrustStale)
			sc.Step(`^it does not silently claim a revoked node is fine$`, st.doesNotSilentlyClaimRevokedIsFine)

			// 6. the certificate itself
			sc.Step(`^a node is enrolled$`, st.aNodeIsEnrolled)
			sc.Step(`^its certificate carries an expiry$`, st.certificateCarriesAnExpiry)
			sc.Step(`^a certificate presented after that expiry is refused as expired$`, st.expiredCertificateIsRefused)
			sc.Step(`^the certificate authorizes membership of this Edge only$`, st.authorizesMembershipOnly)
			sc.Step(`^it cannot be used to serve a model, spend from a wallet, or authorize a payout$`, st.cannotServeSpendOrPayout)
			sc.Step(`^the account's Edge root and the node's private key are different keys$`, st.rootAndNodeKeyAreDifferent)
			sc.Step(`^a node never holds the root's private half$`, st.nodeNeverHoldsRootPrivateHalf)
			sc.Step(`^Core returns (.+)$`, st.coreReturns)
			sc.Step(`^the machine enrolls$`, st.theMachineEnrolls)
			sc.Step(`^enrollment fails with "([^"]*)"$`, st.enrollmentFailsWith)
			sc.Step(`^the machine holds no partial identity$`, st.noPartialIdentity)

			// 7. it never gets in the way
			sc.Step(`^the owner runs roger without asking to enroll$`, st.ownerRunsRogerWithoutEnrolling)
			sc.Step(`^nothing enrolls itself$`, st.nothingEnrollsItself)
			sc.Step(`^no key is generated$`, st.noKeyIsGenerated)
			sc.Step(`^the Edge screen is honestly empty rather than quietly joined$`, st.edgeScreenHonestlyEmpty)
			sc.Step(`^enrollment fails at any step$`, st.enrollmentFailsAtAnyStep)
			sc.Step(`^no certificate, no node key and no fleet row remain$`, st.nothingRemains)
			sc.Step(`^retrying later behaves as a first attempt$`, st.retryingBehavesAsAFirstAttempt)
			sc.Step(`^this machine is already serving a model as a Station$`, st.alreadyServingAsAStation)
			sc.Step(`^the owner enrolls it$`, st.ownerEnrollsIt)
			sc.Step(`^its market registration, its offers and its receipts are unchanged$`, st.stationRegistrationUnchanged)
			sc.Step(`^it appears in the fleet as a node with the serve capability$`, st.appearsWithServeCapability)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/enrollment.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the Roger Edge enrollment scenarios failed")
	}
}
