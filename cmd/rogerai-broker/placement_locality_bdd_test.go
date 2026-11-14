package main

// placement_locality_bdd_test.go makes features/tower/placement_locality.feature EXECUTABLE.
//
// Every scenario drives the real broker: the production route table (b.routes()), the real
// tower subsystem, the real hub resolver seam (withHubResolver), the real link mirror and
// station-facts store (PostgreSQL when ROGERAI_TEST_DATABASE_URL is set, in-process otherwise),
// real self-attach requests signed by real operator keys, and real towerhub servers.
//
// The host proxy. Production sits behind a front proxy that OVERWRITES the client address
// header with the TCP peer it saw. A test cannot dial from a public address, so plcFront is a
// real reverse proxy in front of the broker that overwrites CF-Connecting-IP with the peer the
// scenario says the request came from (its own TCP peer when none is set). Everything behind
// it is the production path: clientIP, addrLocality, coarseNetBucket and the operator table.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/pgtest"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// plcTableText is the operator's network-to-continent table for these scenarios: two NA and
// two EU /24s (so "different buckets, same continent" exists), one AF /24 and one IPv6 /32.
const plcTableText = "203.0.113.0/24 NA\n203.0.112.0/24 NA\n198.51.100.0/24 EU\n198.51.101.0/24 EU\n" +
	"192.0.2.0/24 AF\n2001:db8::/32 EU\n"

// plcAddrFragments are every address prefix these scenarios use. None may appear in a log
// line, an admin view or a public answer.
var plcAddrFragments = []string{"203.0.113.", "203.0.112.", "198.51.100.", "198.51.101.", "192.0.2.", "2001:db8"}

// plcLog is a race-safe log sink (log.Logger serializes writes; reads need the same lock).
type plcLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *plcLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *plcLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// plcFront is the host proxy: it overwrites the client address header with the peer it saw.
type plcFront struct {
	srv  *httptest.Server
	mu   sync.Mutex
	peer string // "" = the real TCP peer
}

func (f *plcFront) setPeer(p string) { f.mu.Lock(); f.peer = p; f.mu.Unlock() }

func plcNewFront(target *httptest.Server) *plcFront {
	u, _ := url.Parse(target.URL)
	f := &plcFront{}
	rp := httputil.NewSingleHostReverseProxy(u)
	inner := rp.Director
	rp.Director = func(r *http.Request) {
		inner(r)
		f.mu.Lock()
		peer := f.peer
		f.mu.Unlock()
		if peer == "" {
			peer, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		r.Header.Set("CF-Connecting-IP", peer) // overwritten, never appended
	}
	f.srv = httptest.NewServer(rp)
	return f
}

// plcAttachOut is the self-attach answer a node reads.
type plcAttachOut struct {
	StationID       string `json:"station_id"`
	TowerID         string `json:"tower_id"`
	Endpoint        string `json:"endpoint"`
	EndpointTLSSPKI string `json:"endpoint_tls_spki"`
	TowerKeyHash    string `json:"tower_key_hash"`
	Note            string `json:"note"`
}

// plcNode is one node about to self-attach (or attached).
type plcNode struct {
	op       operator
	body     map[string]any
	nodeID   string
	peer     string // the address the host proxy saw; "" with direct = true is loopback
	direct   bool   // straight to the broker, no proxy (a loopback caller)
	headers  map[string]string
	out      plcAttachOut
	placedOn string
}

type plcState struct {
	t      *testing.T
	pgDB   *sql.DB
	pgMir  link.Mirror
	pgMob  mobility.Store
	mirror link.Mirror

	b     *broker
	srv   *httptest.Server
	front *plcFront
	b2    *broker
	extra []*httptest.Server
	logs  *plcLog
	undo  func()

	mu       sync.Mutex
	asked    []string
	answered int
	lastErr  error
	answer   func(ctx context.Context, host string) ([]string, error)

	towers     map[string]linkTower
	sess       map[string]string
	endpoint   string
	pin        string
	helloExtra map[string]any
	lastOpen   time.Duration
	opCount    int

	nodes   []*plcNode
	stash   map[string]any
	brokers []*broker // brokers started from a failing table
	brokerS []*httptest.Server
	outline string
	slow    bool
}

// --- wiring -----------------------------------------------------------------------------

func (s *plcState) newBroker(table *netTable, mirror link.Mirror) (*broker, *httptest.Server) {
	_, priv, _ := plcKey()
	b := buildBroker(store.NewMem(), priv, 0.1, 0, time.Minute)
	return s.wire(b, table, mirror)
}

func (s *plcState) wire(b *broker, table *netTable, mirror link.Mirror) (*broker, *httptest.Server) {
	deps := linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore(), mirror: mirror}
	if s.pgMob != nil {
		deps.mobility = s.pgMob
	}
	ts, err := newTowerSubsystem(b, admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour}, deps)
	if err != nil {
		s.t.Fatalf("tower subsystem: %v", err)
	}
	b.tower = ts
	b.adminKey = "admin-secret"
	b.netTable = table
	srv := httptest.NewServer(b.routes())
	s.extra = append(s.extra, srv)
	return b, srv
}

func plcKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func (s *plcState) table() *netTable {
	nt, err := parseNetTable([]byte(plcTableText))
	if err != nil {
		s.t.Fatalf("table: %v", err)
	}
	return nt
}

func (s *plcState) reset() {
	s.mirror = s.pgMir
	if s.mirror == nil {
		s.mirror = link.NewMemMirror()
	}
	s.b, s.srv = s.newBroker(s.table(), s.mirror)
	s.front = plcNewFront(s.srv)
	s.extra = append(s.extra, s.front.srv)
	s.b2 = nil
	s.logs = &plcLog{}
	prevOut, prevFl := log.Writer(), log.Flags()
	log.SetOutput(s.logs)
	log.SetFlags(0)
	s.undo = func() { log.SetOutput(prevOut); log.SetFlags(prevFl) }
	s.mu.Lock()
	s.asked, s.answered, s.lastErr, s.answer = nil, 0, nil, nil
	s.mu.Unlock()
	withHubResolver(s.t, s.resolve)
	s.towers, s.sess = map[string]linkTower{}, map[string]string{}
	s.endpoint, s.pin, s.helloExtra, s.lastOpen = "hub.example:7443", "", nil, 0
	s.nodes, s.stash, s.brokers, s.brokerS, s.outline, s.slow = nil, map[string]any{}, nil, nil, "", false
}

func (s *plcState) teardown() {
	if s.undo != nil {
		s.undo()
		s.undo = nil
	}
	for _, srv := range s.extra {
		srv.Close()
	}
	s.extra = nil
}

// resolve is the hub resolver Core calls (installed through withHubResolver).
func (s *plcState) resolve(ctx context.Context, host string) ([]string, error) {
	s.mu.Lock()
	s.asked = append(s.asked, host)
	a := s.answer
	s.mu.Unlock()
	var out []string
	var err error
	if a == nil {
		err = &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	} else {
		out, err = a(ctx, host)
	}
	s.mu.Lock()
	s.answered++
	s.lastErr = err
	s.mu.Unlock()
	return out, err
}

func (s *plcState) setAnswer(addrs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = func(context.Context, string) ([]string, error) { return addrs, nil }
}

func (s *plcState) setNoAnswer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = func(_ context.Context, host string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
}

func (s *plcState) counts() (asked, answered int, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked), s.answered, s.lastErr
}

// newTower enrolls an active, established Tower (no session yet).
func (s *plcState) newTower(name string) linkTower {
	s.opCount++
	op := signedInOperator(s.t, s.b, fmt.Sprintf("plc-tower-%d", s.opCount))
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		s.t.Fatalf("activate: %v", err)
	}
	setStage(s.t, s.b, lt.id, stageEstablished)
	s.towers[name] = lt
	return lt
}

// open sends the Tower's signed Hello (plus any extra fields it chooses to send) through
// the host proxy and returns the session id.
func (s *plcState) open(name, endpoint, pin string, extra map[string]any) (string, error) {
	lt := s.towers[name]
	hello := map[string]any{
		"network": link.PublicNetwork, "versions": []int{1}, "tower_id": lt.id,
		"capabilities": mandatoryCaps(), "relay_endpoint": endpoint,
	}
	if pin != "" {
		hello["relay_tls_spki"] = pin
	}
	for k, v := range extra {
		hello[k] = v
	}
	var acc link.Accepted
	start := time.Now()
	code, raw := lt.call(s.t, s.front.srv, "/tower/session", jsonOf(s.t, hello), &acc)
	s.lastOpen = time.Since(start)
	if code != http.StatusOK || acc.SessionID == "" {
		return "", fmt.Errorf("session open answered %d: %s", code, raw)
	}
	s.sess[name] = acc.SessionID
	return acc.SessionID, nil
}

// settle waits for the measurement a session open started to finish: the locality is known,
// or the lookup it made has failed (which records nothing).
func (s *plcState) settle(name string, before int, endpoint string) error {
	lt := s.towers[name]
	host, _, _ := net.SplitHostPort(endpoint)
	if endpoint != "" && net.ParseIP(host) == nil {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, answered, _ := s.counts(); answered > before {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("Core never looked up the hub host %q", host)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	if _, _, lastErr := s.counts(); lastErr != nil && net.ParseIP(host) == nil {
		deadline = time.Now().Add(200 * time.Millisecond) // a failed lookup records nothing
	}
	for time.Now().Before(deadline) {
		if _, ok := s.b.tower.link.Locality(lt.id); ok {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

// computeTower opens a fresh session for the Tower and waits for its measurement.
func (s *plcState) computeTower(name string) error {
	if _, ok := s.towers[name]; !ok {
		s.newTower(name)
	}
	_, before, _ := s.counts()
	if _, err := s.open(name, s.endpoint, s.pin, s.helloExtra); err != nil {
		return err
	}
	return s.settle(name, before, s.endpoint)
}

func (s *plcState) loc(name string) (placement.Locality, bool) {
	return s.b.tower.link.Locality(s.towers[name].id)
}

// --- nodes ------------------------------------------------------------------------------

// newNode is a signed-in operator with a heartbeating share node and a self-attach body.
func (s *plcState) newNode(peer string, direct bool) *plcNode {
	s.opCount++
	op := signedInOperator(s.t, s.b, fmt.Sprintf("plc-node-%d", s.opCount))
	body, _ := selfAttachBodyFor(s.t, s.b, op)
	n := &plcNode{op: op, body: body, nodeID: body["node_id"].(string), peer: peer, direct: direct}
	s.nodes = append(s.nodes, n)
	return n
}

// declareRegion is the node operator's typed region, on the node's own registration.
func (s *plcState) declareRegion(n *plcNode, region string) {
	s.b.mu.Lock()
	reg := s.b.nodes[n.nodeID]
	reg.Region = region
	s.b.nodes[n.nodeID] = reg
	s.b.mu.Unlock()
}

// attachNode sends the node's self-attach, through the host proxy unless it is direct.
func (s *plcState) attachNode(n *plcNode) (int, string) {
	via := s.front.srv
	if n.direct {
		via = s.srv
	} else {
		s.front.setPeer(n.peer)
	}
	var out plcAttachOut
	var code int
	var raw string
	if len(n.headers) > 0 {
		code, raw = s.attachWithHeaders(n, via, &out)
	} else {
		code, raw = n.op.attach(s.t, via, n.body, &out)
	}
	if code == http.StatusOK {
		n.out = out
		n.placedOn = out.TowerID
	}
	return code, raw
}

// attachWithHeaders is operator.attach plus headers of the node's own choosing. The proof is
// built through the production type exactly as attach does.
func (s *plcState) attachWithHeaders(n *plcNode, via *httptest.Server, out any) (int, string) {
	body := jsonOf(s.t, n.body)
	apub, _ := n.body["assertion_key"].(string)
	skey, _ := n.body["session_key"].(string)
	priv, ok := assertionKeyOf(apub)
	if !ok {
		s.t.Fatalf("no assertion key for %s", apub)
	}
	const path = "/tower/edge/attach"
	req, _ := http.NewRequest(http.MethodPost, via.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(n.op.priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderAttachProof, protocol.AttachProof{
		Network: link.PublicNetwork, CallerPubkey: pub, TS: ts,
		AssertionKey: apub, SessionKey: skey, Body: body,
	}.Sign(priv))
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, out)
	return resp.StatusCode, string(raw)
}

func (s *plcState) facts(n *plcNode) (placement.Locality, error) {
	f, ok, err := s.b.tower.mobility.Facts(n.out.StationID)
	if err != nil || !ok {
		return placement.Locality{}, fmt.Errorf("no station facts for %s (err %v)", n.out.StationID, err)
	}
	return f.Locality, nil
}

// placedEvent is the station_placed event Core wrote for the node's Station.
func (s *plcState) placedEvent(n *plcNode) (admit.Event, error) {
	for _, ev := range towerEventsOf(s.t, s.b, n.placedOn, evStationPlaced) {
		if fmt.Sprint(ev.Detail["station"]) == n.out.StationID {
			return ev, nil
		}
	}
	return admit.Event{}, fmt.Errorf("no station_placed event for %s on %s", n.out.StationID, n.placedOn)
}

func (s *plcState) placedClass(n *plcNode) (string, error) {
	ev, err := s.placedEvent(n)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(ev.Detail["class"]), nil
}

func (s *plcState) placeAll() error {
	for _, n := range s.nodes {
		if n.out.StationID != "" {
			continue
		}
		if code, raw := s.attachNode(n); code != http.StatusOK {
			return fmt.Errorf("self-attach answered %d: %s", code, raw)
		}
	}
	return nil
}

func (s *plcState) liveTower(name, endpoint string) linkTower {
	s.opCount++
	lt := liveEdgeTower(s.t, s.b, s.srv, fmt.Sprintf("plc-live-%d", s.opCount), endpoint)
	s.towers[name] = lt
	_ = s.waitKnown(name)
	return lt
}

func (s *plcState) waitKnown(name string) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.loc(name); ok {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("Tower %s was never measured", name)
}

func plcNoAddr(where, text string) error {
	for _, f := range plcAddrFragments {
		if strings.Contains(text, f) {
			return fmt.Errorf("%s carries an address or bucket (%q): %.400s", where, f, text)
		}
	}
	return nil
}

func plcWant(got, want placement.Locality, what string) error {
	if got != want {
		return fmt.Errorf("%s: got %+v, want %+v", what, got, want)
	}
	return nil
}

func (s *plcState) get(srv *httptest.Server, path string) (int, string) {
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (s *plcState) adminLiveTable(srv *httptest.Server) map[string]any {
	var out map[string]any
	adminGetJSON(s.t, srv.URL, "/admin/live", &out)
	rt, _ := out["region_table"].(map[string]any)
	return rt
}

// registerNode registers a classic node through the host proxy (so Core stamps the
// continent from the address it observes) with an operator-typed region and one offer.
func (s *plcState) registerNode(peer, region string) (string, error) {
	pub, priv, err := plcKey()
	if err != nil {
		return "", err
	}
	s.opCount++
	id := fmt.Sprintf("plc-reg-%d", s.opCount)
	reg := protocol.NodeRegistration{NodeID: id, PubKey: hexOf(pub), TS: time.Now().Unix(), Region: region,
		Offers: []protocol.ModelOffer{{Model: "plc-model", PriceIn: 0.1, PriceOut: 0.2}}}
	reg.SignRegistration(priv)
	body := jsonOf(s.t, reg)
	owner := signedInOperator(s.t, s.b, fmt.Sprintf("plc-owner-%d", s.opCount)) // a priced node needs a signed-in owner
	s.front.setPeer(peer)
	req, _ := http.NewRequest(http.MethodPost, s.front.srv.URL+"/nodes/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	opub, ots, osig := protocol.SignRequest(owner.priv, http.MethodPost, "/nodes/register", body)
	req.Header.Set(protocol.HeaderPubkey, opub)
	req.Header.Set(protocol.HeaderTS, itoa64(ots))
	req.Header.Set(protocol.HeaderSig, osig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register answered %d: %s", resp.StatusCode, raw)
	}
	return id, nil
}

func (s *plcState) discoverOffer(nodeID string) (map[string]any, string, error) {
	code, raw := s.get(s.srv, "/discover")
	if code != http.StatusOK {
		return nil, raw, fmt.Errorf("/discover answered %d", code)
	}
	var offers []map[string]any
	if err := json.Unmarshal([]byte(raw), &offers); err != nil {
		var wrapped map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &wrapped) == nil {
			for _, k := range []string{"offers", "models", "data"} {
				if v, ok := wrapped[k]; ok && json.Unmarshal(v, &offers) == nil {
					break
				}
			}
		}
	}
	for _, o := range offers {
		if o["node_id"] == nodeID {
			return o, raw, nil
		}
	}
	return nil, raw, fmt.Errorf("/discover has no offer for %s: %.300s", nodeID, raw)
}

// --- TLS hubs ---------------------------------------------------------------------------

// plcHub serves a real towerhub poll route over TLS, claiming towerID and proving with key.
// cert nil uses httptest's built-in certificate.
func (s *plcState) plcHub(towerID string, key ed25519.PrivateKey, own *tls.Certificate) (*httptest.Server, string) {
	hs := towerhub.NewServer(towerhub.New(), nil, towerhub.ServerOptions{TowerID: towerID, EpochKey: key,
		SubmitTTL: time.Second, PollTTL: 50 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, hs.Poll)
	srv := httptest.NewUnstartedServer(mux)
	if own != nil {
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{*own}}
	}
	srv.StartTLS()
	s.extra = append(s.extra, srv)
	return srv, towerhub.CertPin(srv.Certificate())
}

// plcCert is a fresh self-signed certificate for 127.0.0.1: another host's certificate.
func plcCert() (*tls.Certificate, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "elsewhere"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, nil
}

func plcHostPort(srv *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
}

// nodeCheck is the check a node runs on the hub it was sent to: the pinned transport and the
// identity proof, from the attach answer.
func plcNodeCheck(endpoint, pin, towerID, keyHash string) string {
	base, hc, err := towerhub.Reach(endpoint, pin, nil)
	if err != nil {
		return "unreachable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, reason := towerhub.CheckHub(ctx, hc, base, towerID, keyHash)
	return reason
}

// --- the scenarios ----------------------------------------------------------------------

func (s *plcState) towerOpensAdvertising(endpoint string) error {
	s.setAnswer("203.0.113.7")
	s.endpoint = endpoint
	s.newTower("T")
	// Fields a Tower might type or claim; the Hello has no place for them and Core reads none.
	s.helloExtra = map[string]any{"region": "af-south-1", "display_name": "Reunion Africa hub",
		"continent": "AF", "bucket": "192.0.2.0/24"}
	return nil
}

func (s *plcState) coreAcceptsSession() error {
	_, before, _ := s.counts()
	if _, err := s.open("T", s.endpoint, "", s.helloExtra); err != nil {
		return err
	}
	return s.settle("T", before, s.endpoint)
}

func (s *plcState) coreResolvesHubItself() error {
	s.mu.Lock()
	asked := append([]string(nil), s.asked...)
	s.mu.Unlock()
	if len(asked) != 1 || asked[0] != "hub.example" {
		return fmt.Errorf("Core resolved %v, want exactly the hub host [hub.example]", asked)
	}
	got, ok := s.loc("T")
	if !ok {
		return fmt.Errorf("the Tower's locality was never recorded")
	}
	return plcWant(got, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "hub locality")
}

func (s *plcState) storedOnLiveLinkRecord() error {
	lt := s.towers["T"]
	rec, there, err := s.mirror.Get(lt.id)
	if err != nil || !there {
		return fmt.Errorf("no live link record in the shared mirror (err %v)", err)
	}
	if !rec.LocalityKnown || rec.SessionID != s.sess["T"] {
		return fmt.Errorf("the live link record is not measured for the current session: %+v", rec)
	}
	want := placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}
	if err := plcWant(rec.Locality, want, "shared link record"); err != nil {
		return err
	}
	// A second broker instance over the same shared mirror, holding no session of its own.
	b2, _ := s.newBroker(s.table(), s.mirror)
	got, ok := b2.tower.link.Locality(lt.id)
	if !ok {
		return fmt.Errorf("instance B cannot read the Tower's locality")
	}
	return plcWant(got, want, "instance B's read")
}

func (s *plcState) typedFieldsPlayNoPart() error {
	got, _ := s.loc("T")
	if got.Continent == "AF" || got.Bucket == "192.0.2.0/24" {
		return fmt.Errorf("the Hello's claimed region/continent/bucket leaked into the locality: %+v", got)
	}
	// The same hub, advertised by a Tower that says nothing else, measures the same.
	s.newTower("plain")
	_, before, _ := s.counts()
	if _, err := s.open("plain", s.endpoint, "", nil); err != nil {
		return err
	}
	if err := s.settle("plain", before, s.endpoint); err != nil {
		return err
	}
	plain, _ := s.loc("plain")
	return plcWant(got, plain, "a Tower that typed and claimed things vs one that did not")
}

func (s *plcState) linkFromOneContinent() error {
	s.front.setPeer("198.51.100.9") // EU
	s.newTower("T")
	return nil
}

func (s *plcState) hubResolvesElsewhere() error { s.setAnswer("203.0.113.7"); return nil } // NA

func (s *plcState) computesTowerLocation() error { return s.computeTower("T") }

func (s *plcState) towerContinentUnknown() error {
	got, _ := s.loc("T")
	if got.Continent != "" {
		return fmt.Errorf("the Tower's continent is %q, want unknown", got.Continent)
	}
	return nil
}

func (s *plcState) logsOneConflictLine() error {
	var lines []string
	for _, l := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(l, "tower-locality conflict") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		return fmt.Errorf("want one tower-locality conflict line, got %d: %q", len(lines), lines)
	}
	l := lines[0]
	for _, want := range []string{"tower=" + s.towers["T"].id, "hub=NA", "link=EU"} {
		if !strings.Contains(l, want) {
			return fmt.Errorf("the conflict line %q does not name %q", l, want)
		}
	}
	if err := plcNoAddr("the conflict line", l); err != nil {
		return err
	}
	if strings.Contains(l, "/24") || strings.Contains(l, "/32") {
		return fmt.Errorf("the conflict line names a bucket: %q", l)
	}
	return nil
}

func (s *plcState) stillEligibleRankedUnknown() error {
	n := s.newNode("203.0.113.50", false) // a Station in NA: a known continent of its own
	if code, raw := s.attachNode(n); code != http.StatusOK {
		return fmt.Errorf("the Tower was not eligible: self-attach answered %d: %s", code, raw)
	}
	if n.placedOn != s.towers["T"].id {
		return fmt.Errorf("placed on %s, want the conflicted Tower", n.placedOn)
	}
	class, err := s.placedClass(n)
	if err != nil {
		return err
	}
	if class != "unknown" {
		return fmt.Errorf("the conflicted Tower ranked %q, want unknown", class)
	}
	return nil
}

func (s *plcState) hubResolvesToSeveral() error { s.newTower("T"); return nil }

func (s *plcState) sameBucket() error {
	s.setAnswer("203.0.113.7", "203.0.113.99", "203.0.113.200")
	return s.computeTower("T")
}

func (s *plcState) thatBucketIsTheTowers() error {
	got, _ := s.loc("T")
	return plcWant(got, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "one bucket")
}

func (s *plcState) spanBucketsOneContinent() error {
	s.setAnswer("203.0.113.7", "203.0.112.7")
	return s.computeTower("T")
}

func (s *plcState) continentNoBucket() error {
	got, _ := s.loc("T")
	return plcWant(got, placement.Locality{Continent: "NA"}, "buckets within one continent")
}

func (s *plcState) spanContinents() error {
	s.setAnswer("203.0.113.7", "198.51.100.7")
	return s.computeTower("T")
}

var plcEndpoints = map[string]string{
	"a loopback address":           "127.0.0.1",
	"a private RFC 1918 address":   "10.1.2.3",
	"an IPv6 unique local address": "fd00::1",
	"a link-local address":         "fe80::1",
	"the unspecified address":      "0.0.0.0",
	"an address in no table range": "8.8.8.8",
}

func (s *plcState) hubEndpointIs(kind string) error {
	s.outline = kind
	s.newTower("T")
	return nil
}

// computeOutline measures the outline's endpoint both ways a Tower can give it: as the literal
// address and as a host name that resolves to it ("a host that does not resolve" has only the
// second).
func (s *plcState) computeOutline() error {
	kind := s.outline
	if kind == "a host that does not resolve" {
		s.setNoAnswer()
		s.endpoint = "nowhere.invalid:7443"
		if err := s.computeTower("T"); err != nil {
			return err
		}
		s.stash["outline"] = []placement.Locality{s.locZero("T")}
		return nil
	}
	addr, ok := plcEndpoints[kind]
	if !ok {
		return fmt.Errorf("unknown endpoint kind %q", kind)
	}
	var got []placement.Locality
	s.endpoint = net.JoinHostPort(addr, "7443")
	if err := s.computeTower("T"); err != nil {
		return err
	}
	got = append(got, s.locZero("T"))
	s.setAnswer(addr)
	s.endpoint = "hub.example:7443"
	if err := s.computeTower("T"); err != nil {
		return err
	}
	got = append(got, s.locZero("T"))
	s.stash["outline"] = got
	return nil
}

func (s *plcState) locZero(name string) placement.Locality {
	l, _ := s.loc(name)
	return l
}

func (s *plcState) bucketAndContinentUnknown() error {
	for i, l := range s.stash["outline"].([]placement.Locality) {
		if l != (placement.Locality{}) {
			return fmt.Errorf("%s (measurement %d): bucket %q continent %q, want both unknown", s.outline, i+1, l.Bucket, l.Continent)
		}
	}
	return nil
}

// bucketKnownContinentUnknown is the amended row (founder ruling 2026-11-13): a public address
// outside the table keeps its network and has no continent.
func (s *plcState) bucketKnownContinentUnknown() error {
	for i, l := range s.stash["outline"].([]placement.Locality) {
		if l.Bucket == "" || l.Continent != "" {
			return fmt.Errorf("%s (measurement %d): bucket %q continent %q, want a bucket and no continent", s.outline, i+1, l.Bucket, l.Continent)
		}
	}
	return nil
}

func (s *plcState) locationComputedAtOpen() error {
	s.setAnswer("203.0.113.7")
	if err := s.computeTower("T"); err != nil {
		return err
	}
	got, _ := s.loc("T")
	if err := plcWant(got, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "at open"); err != nil {
		return err
	}
	asked, _, _ := s.counts()
	s.stash["asked"] = asked
	return nil
}

func (s *plcState) dnsChangesWhileOpen() error {
	s.setAnswer("198.51.100.7") // the hub's name now points at EU
	lt := s.towers["T"]
	for i := 0; i < 2; i++ {
		code, raw := lt.call(s.t, s.front.srv, "/tower/session/heartbeat", jsonOf(s.t, frameFor(lt, s.sess["T"])), nil)
		if code != http.StatusOK {
			return fmt.Errorf("heartbeat answered %d: %s", code, raw)
		}
		s.b.towerGovSweepOnce(time.Now())
	}
	return nil
}

func (s *plcState) unchangedUntilNextSession() error {
	want := placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}
	got, _ := s.loc("T")
	if err := plcWant(got, want, "while the session stays open"); err != nil {
		return err
	}
	rec, _, err := s.mirror.Get(s.towers["T"].id)
	if err != nil {
		return err
	}
	if err := plcWant(rec.Locality, want, "the shared record while the session stays open"); err != nil {
		return err
	}
	if asked, _, _ := s.counts(); asked != s.stash["asked"].(int) {
		return fmt.Errorf("Core looked the hub up again mid-session (%d lookups, want %d)", asked, s.stash["asked"])
	}
	return nil
}

func (s *plcState) newSessionRecomputes() error {
	// First a new session whose lookup fails: nothing from the old session may survive.
	s.setNoAnswer()
	if err := s.computeTower("T"); err != nil {
		return err
	}
	if l, ok := s.loc("T"); ok {
		return fmt.Errorf("the old session's locality survived a new session: %+v", l)
	}
	rec, there, err := s.mirror.Get(s.towers["T"].id)
	if err != nil || !there {
		return fmt.Errorf("no shared record (err %v)", err)
	}
	if rec.LocalityKnown || rec.Locality != (placement.Locality{}) {
		return fmt.Errorf("the shared record kept a stale locality: %+v", rec)
	}
	// Then a session whose lookup answers: computed from scratch, from today's DNS.
	s.setAnswer("198.51.100.7")
	if err := s.computeTower("T"); err != nil {
		return err
	}
	got, _ := s.loc("T")
	return plcWant(got, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, "the new session")
}

func (s *plcState) lookupTakesLong() error {
	s.mu.Lock()
	s.answer = func(ctx context.Context, _ string) ([]string, error) {
		select { // longer than 2 seconds unless Core gives up first
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return []string{"203.0.113.7"}, nil
		}
	}
	s.mu.Unlock()
	s.slow = true
	s.newTower("T")
	return nil
}

func (s *plcState) coreAcceptsSlowSession() error {
	_, err := s.open("T", s.endpoint, "", nil)
	return err
}

func (s *plcState) opensOnTimeUnknown() error {
	if s.lastOpen >= time.Second {
		return fmt.Errorf("the session took %v to open, waiting on DNS", s.lastOpen)
	}
	if l, ok := s.loc("T"); ok {
		return fmt.Errorf("the session opened with a location already: %+v", l)
	}
	// The lookup gives up at 2 seconds and records nothing.
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, answered, lastErr := s.counts(); answered > 0 {
			if lastErr == nil {
				return fmt.Errorf("the slow lookup was answered rather than abandoned")
			}
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Core never abandoned the slow lookup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if l, ok := s.loc("T"); ok {
		return fmt.Errorf("an abandoned lookup recorded a location: %+v", l)
	}
	return nil
}

func (s *plcState) retriesOncePerSweep() error {
	s.setNoAnswer()
	for i := 1; i <= 2; i++ {
		before, _, _ := s.counts()
		s.b.towerGovSweepOnce(time.Now())
		after, _, _ := s.counts()
		if after-before != 1 {
			return fmt.Errorf("sweep %d made %d lookups for an unmeasured Tower, want 1", i, after-before)
		}
	}
	s.setAnswer("198.51.100.7")
	before, _, _ := s.counts()
	s.b.towerGovSweepOnce(time.Now())
	if after, _, _ := s.counts(); after-before != 1 {
		return fmt.Errorf("the sweep that measured made %d lookups, want 1", after-before)
	}
	got, ok := s.loc("T")
	if !ok {
		return fmt.Errorf("the sweep's retry did not record a location")
	}
	if err := plcWant(got, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, "after the retry"); err != nil {
		return err
	}
	before, _, _ = s.counts()
	s.b.towerGovSweepOnce(time.Now())
	if after, _, _ := s.counts(); after != before {
		return fmt.Errorf("a measured Tower was looked up again by the sweep")
	}
	return nil
}

func (s *plcState) nodeFromPublic() error {
	s.liveTower("EU-tower", "198.51.100.10:7443")
	n := s.newNode("203.0.113.50", false)
	s.declareRegion(n, "af") // what the node typed: Africa
	n.body["region"] = "AF"  // and what it says in its own attach
	return nil
}

func (s *plcState) corePlacesStation() error { return s.placeAll() }

func (s *plcState) stationFromRequestAddress() error {
	n := s.nodes[0]
	got, err := s.facts(n)
	if err != nil {
		return err
	}
	want := s.b.addrLocality("203.0.113.50")
	if err := plcWant(got, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "the Station's locality"); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the Station's locality %+v is not its request address's %+v", got, want)
	}
	class, err := s.placedClass(n)
	if err != nil {
		return err
	}
	tloc, _ := s.loc("EU-tower")
	if want := placement.ClassOf(got, tloc).String(); class != want {
		return fmt.Errorf("placed with class %q, want %q from the request address", class, want)
	}
	return nil
}

func (s *plcState) typedRegionNoPart() error {
	got, _ := s.facts(s.nodes[0])
	if got.Continent == "AF" {
		return fmt.Errorf("the node's typed region decided its continent: %+v", got)
	}
	return nil
}

func (s *plcState) behindHostProxy() error {
	s.liveTower("NA-tower", "203.0.113.10:7443")
	s.newNode("198.51.100.20", false) // the peer the host proxy saw: EU
	return nil
}

func (s *plcState) nodeSendsOwnHeader() error {
	n := s.nodes[0]
	n.headers = map[string]string{"CF-Connecting-IP": "203.0.113.66", "X-Forwarded-For": "203.0.113.66"}
	if code, raw := s.attachNode(n); code != http.StatusOK {
		return fmt.Errorf("self-attach answered %d: %s", code, raw)
	}
	return nil
}

func (s *plcState) usesProxyAddress() error {
	got, err := s.facts(s.nodes[0])
	if err != nil {
		return err
	}
	return plcWant(got, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, "the Station behind the proxy")
}

func (s *plcState) nodesCopyIgnored() error {
	n := s.nodes[0]
	got, _ := s.facts(n)
	if got.Bucket == "203.0.113.0/24" || got.Continent == "NA" {
		return fmt.Errorf("the node's own header decided its locality: %+v", got)
	}
	class, err := s.placedClass(n)
	if err != nil {
		return err
	}
	if class == "same_network" || class == "same_continent" {
		return fmt.Errorf("the node's header made it look near the NA Tower (class %q)", class)
	}
	return nil
}

func (s *plcState) nodeFromPrivate() error {
	s.liveTower("EU-tower", "198.51.100.10:7443")
	s.newNode("", true) // straight from loopback
	for _, p := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.5", "172.16.4.4", "fd00::1", "fe80::1"} {
		s.newNode(p, false)
	}
	return nil
}

func (s *plcState) stationUnknown() error {
	for _, n := range s.nodes {
		got, err := s.facts(n)
		if err != nil {
			return err
		}
		if got != (placement.Locality{}) {
			return fmt.Errorf("a Station attaching from %q has locality %+v, want unknown", n.peer, got)
		}
		if class, err := s.placedClass(n); err != nil || class != "unknown" {
			return fmt.Errorf("a Station attaching from %q placed as %q (err %v), want unknown", n.peer, class, err)
		}
	}
	return nil
}

func (s *plcState) attachedThenElsewhere() error {
	s.liveTower("EU-tower", "198.51.100.10:7443")
	n := s.newNode("198.51.100.20", false)
	if code, raw := s.attachNode(n); code != http.StatusOK {
		return fmt.Errorf("first attach answered %d: %s", code, raw)
	}
	got, _ := s.facts(n)
	return plcWant(got, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, "the first attach")
}

func (s *plcState) handlesReattach() error {
	n := s.nodes[0]
	n.peer = "203.0.113.50"
	var out plcAttachOut
	s.front.setPeer(n.peer)
	code, raw := n.op.attach(s.t, s.front.srv, n.body, &out)
	if code != http.StatusOK {
		return fmt.Errorf("re-attach answered %d: %s", code, raw)
	}
	if out.StationID != n.out.StationID {
		return fmt.Errorf("the re-attach is a different Station (%s, want %s)", out.StationID, n.out.StationID)
	}
	return nil
}

func (s *plcState) locationIsNewNetworks() error {
	got, err := s.facts(s.nodes[0])
	if err != nil {
		return err
	}
	return plcWant(got, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "after the re-attach")
}

func (s *plcState) nothingOldSurvives() error {
	n := s.nodes[0]
	got, _ := s.facts(n)
	if got.Bucket == "198.51.100.0/24" || got.Continent == "EU" {
		return fmt.Errorf("a value from the old network survived: %+v", got)
	}
	// Again from a network with a bucket and no continent: the NA continent must not survive.
	s.front.setPeer("8.8.8.8")
	var out plcAttachOut
	if code, raw := n.op.attach(s.t, s.front.srv, n.body, &out); code != http.StatusOK {
		return fmt.Errorf("third attach answered %d: %s", code, raw)
	}
	got, _ = s.facts(n)
	if err := plcWant(got, placement.Locality{Bucket: "8.8.8.0/24"}, "after a re-attach from outside the table"); err != nil {
		return err
	}
	// And from a private address: neither value survives.
	n.direct = true
	if code, raw := n.op.attach(s.t, s.srv, n.body, &out); code != http.StatusOK {
		return fmt.Errorf("fourth attach answered %d: %s", code, raw)
	}
	got, _ = s.facts(n)
	return plcWant(got, placement.Locality{}, "after a re-attach from loopback")
}

func (s *plcState) stationWith(bucket, cont string) error {
	s.stash["s"] = placement.Locality{Bucket: bucket, Continent: cont}
	return nil
}

func (s *plcState) towerWith(bucket, cont string) error {
	s.stash["t"] = placement.Locality{Bucket: bucket, Continent: cont}
	return nil
}

func (s *plcState) computeClass() error {
	st, tw := s.stash["s"].(placement.Locality), s.stash["t"].(placement.Locality)
	class := placement.ClassOf(st, tw).String()
	// The broker's own use: a one-Tower placement records the class it ranked by.
	snap := []towerFacts{{cand: placement.Candidate{TowerID: "tw-class"}, loc: tw, stage: stageEstablished,
		plane: link.RelayPlane{Endpoint: "hub.example:7443"}}}
	p, ok := s.b.chooseFrom(snap, "plc-owner", st, nil, "", nil)
	if !ok {
		return fmt.Errorf("the placement found no Tower")
	}
	if got := fmt.Sprint(p.detail["class"]); got != class {
		return fmt.Errorf("placement recorded class %q, ClassOf says %q", got, class)
	}
	s.stash["class"] = class
	return nil
}

func (s *plcState) classIs(want string) error {
	if got := s.stash["class"]; got != want {
		return fmt.Errorf("class %q, want %q", got, want)
	}
	return nil
}

func (s *plcState) noTableConfigured() error {
	s.b.netTable = nil
	s.liveTower("T", "203.0.113.10:7443")
	return nil
}

func (s *plcState) computeClasses() error {
	for _, p := range []string{"203.0.113.50", "198.51.100.20", "192.0.2.50", "8.8.8.8"} {
		s.newNode(p, false)
	}
	return s.placeAll()
}

func (s *plcState) unknownUnlessSameBucket() error {
	tloc, _ := s.loc("T")
	if err := plcWant(tloc, placement.Locality{Bucket: "203.0.113.0/24"}, "the Tower with no table"); err != nil {
		return err
	}
	for _, n := range s.nodes {
		class, err := s.placedClass(n)
		if err != nil {
			return err
		}
		want := "unknown"
		if n.peer == "203.0.113.50" {
			want = "same_network"
		}
		if class != want {
			return fmt.Errorf("a Station from %s ranked %q with no table, want %q", n.peer, class, want)
		}
		if f, _ := s.facts(n); f.Continent != "" {
			return fmt.Errorf("a continent %q appeared with no table", f.Continent)
		}
	}
	return nil
}

func (s *plcState) adminLiveNotConfigured() error {
	rt := s.adminLiveTable(s.srv)
	if rt["configured"] != false || fmt.Sprint(rt["ranges"]) != "0" {
		return fmt.Errorf("/admin/live region_table = %v, want not configured with 0 ranges", rt)
	}
	if _, has := rt["error"]; has {
		return fmt.Errorf("/admin/live reports an error with no table configured: %v", rt)
	}
	return nil
}

func (s *plcState) tableUnusable() error {
	dir := s.t.TempDir()
	bad := filepath.Join(dir, "bad-table.txt")
	if err := os.WriteFile(bad, []byte("203.0.113.0/24 NA\nthis is not a cidr line\n"), 0o600); err != nil {
		return err
	}
	s.stash["tables"] = []string{filepath.Join(dir, "missing-table.txt"), bad}
	return nil
}

func (s *plcState) brokerStarts() error {
	prev, had := os.LookupEnv("ROGERAI_NET_CONTINENTS")
	defer func() {
		if had {
			os.Setenv("ROGERAI_NET_CONTINENTS", prev)
		} else {
			os.Unsetenv("ROGERAI_NET_CONTINENTS")
		}
	}()
	for _, path := range s.stash["tables"].([]string) {
		os.Setenv("ROGERAI_NET_CONTINENTS", path)
		_, priv, _ := plcKey()
		nb := buildBroker(store.NewMem(), priv, 0.1, 0, time.Minute) // the startup path
		if nb == nil {
			return fmt.Errorf("the broker did not start with table %s", path)
		}
		table, terr := nb.netTable, nb.netTableErr
		nb, srv := s.wire(nb, table, link.NewMemMirror())
		nb.netTableErr = terr
		s.brokers = append(s.brokers, nb)
		s.brokerS = append(s.brokerS, srv)
	}
	return nil
}

func (s *plcState) placementNoContinents() error {
	noTable := &broker{}
	for i, nb := range s.brokers {
		if nb.netTable != nil {
			return fmt.Errorf("broker %d loaded a table that cannot be used", i)
		}
		for _, a := range []string{"203.0.113.7", "198.51.100.20", "192.0.2.5", "2001:db8::5", "10.0.0.1"} {
			if got, want := nb.addrLocality(a), noTable.addrLocality(a); got != want {
				return fmt.Errorf("broker %d measures %s as %+v, with no table %+v", i, a, got, want)
			}
		}
		// The real path: a Tower measured, a Station placed on it.
		s.opCount++
		lt := liveEdgeTower(s.t, nb, s.brokerS[i], fmt.Sprintf("plc-ft-%d", s.opCount), "203.0.113.10:7443")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := nb.tower.link.Locality(lt.id); ok {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		tloc, _ := nb.tower.link.Locality(lt.id)
		if err := plcWant(tloc, placement.Locality{Bucket: "203.0.113.0/24"}, "a Tower under an unusable table"); err != nil {
			return err
		}
		front := plcNewFront(s.brokerS[i])
		s.extra = append(s.extra, front.srv)
		front.setPeer("198.51.100.20")
		s.opCount++
		op := signedInOperator(s.t, nb, fmt.Sprintf("plc-fn-%d", s.opCount))
		body, _ := selfAttachBodyFor(s.t, nb, op)
		var out plcAttachOut
		if code, raw := op.attach(s.t, front.srv, body, &out); code != http.StatusOK {
			return fmt.Errorf("placement under an unusable table: %d %s", code, raw)
		}
		f, _, _ := nb.tower.mobility.Facts(out.StationID)
		if err := plcWant(f.Locality, placement.Locality{Bucket: "198.51.100.0/24"}, "a Station under an unusable table"); err != nil {
			return err
		}
		evs := towerEventsOf(s.t, nb, lt.id, evStationPlaced)
		if len(evs) != 1 || fmt.Sprint(evs[0].Detail["class"]) != "unknown" {
			return fmt.Errorf("placement under an unusable table ranked %v, want unknown", evs)
		}
	}
	return nil
}

func (s *plcState) adminLiveShowsError() error {
	for i, srv := range s.brokerS {
		rt := s.adminLiveTable(srv)
		msg, _ := rt["error"].(string)
		if msg == "" || msg != s.brokers[i].netTableErr {
			return fmt.Errorf("/admin/live region_table = %v, want the load error %q", rt, s.brokers[i].netTableErr)
		}
		if rt["configured"] != false {
			return fmt.Errorf("/admin/live says an unusable table is configured: %v", rt)
		}
	}
	return nil
}

func (s *plcState) storesOrUsesLocation() error {
	// T: link from NA, hub in NA (agrees). C: link from EU, hub in NA (a conflict line).
	s.setAnswer("203.0.113.7")
	s.front.setPeer("203.0.113.200")
	if err := s.computeTower("T"); err != nil {
		return err
	}
	s.front.setPeer("198.51.100.9")
	if err := s.computeTower("C"); err != nil {
		return err
	}
	id, err := s.registerNode("198.51.100.30", "")
	if err != nil {
		return err
	}
	s.stash["reg"] = id
	n := s.newNode("198.51.100.20", false)
	if code, raw := s.attachNode(n); code != http.StatusOK {
		return fmt.Errorf("self-attach answered %d: %s", code, raw)
	}
	return nil
}

func (s *plcState) keepsOnlyCoarse() error {
	for _, typ := range []reflect.Type{reflect.TypeOf(placement.Locality{})} {
		if typ.NumField() != 2 || typ.Field(0).Name != "Bucket" || typ.Field(1).Name != "Continent" {
			return fmt.Errorf("a locality holds more than a bucket and a continent: %v", typ)
		}
	}
	rec, _, err := s.mirror.Get(s.towers["T"].id)
	if err != nil {
		return err
	}
	if err := plcWant(rec.Locality, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, "stored Tower locality"); err != nil {
		return err
	}
	if rec.LinkContinent != "NA" {
		return fmt.Errorf("the stored link cross-check is %q, want the continent NA", rec.LinkContinent)
	}
	for _, full := range []string{"203.0.113.7", "203.0.113.200"} {
		if strings.Contains(fmt.Sprintf("%+v %s", rec.Locality, rec.LinkContinent), full) {
			return fmt.Errorf("a full address %s is stored as the Tower's location", full)
		}
	}
	n := s.nodes[0]
	f, ok, err := s.b.tower.mobility.Facts(n.out.StationID)
	if err != nil || !ok {
		return fmt.Errorf("no station facts (err %v)", err)
	}
	if err := plcWant(f.Locality, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, "stored Station locality"); err != nil {
		return err
	}
	if strings.Contains(fmt.Sprintf("%+v", f), "198.51.100.20") {
		return fmt.Errorf("the Station's full address is stored: %+v", f)
	}
	s.b.mu.Lock()
	reg := s.b.nodes[s.stash["reg"].(string)]
	s.b.mu.Unlock()
	if reg.NetContinent != "EU" {
		return fmt.Errorf("the classic node's stamped continent is %q, want EU", reg.NetContinent)
	}
	return nil
}

func (s *plcState) neverPublished() error {
	for _, path := range []string{"/discover", "/market", "/v1/models", "/voices", "/categories", "/version", "/promo"} {
		code, raw := s.get(s.srv, path)
		if code == 0 {
			return fmt.Errorf("%s: %s", path, raw)
		}
		if err := plcNoAddr(path, raw); err != nil {
			return err
		}
		for _, key := range []string{"continent", "bucket", "locality", `"NA"`, `"EU"`} {
			if strings.Contains(raw, key) {
				return fmt.Errorf("%s carries %s: %.400s", path, key, raw)
			}
		}
	}
	// The classic node is listed, and its listing does not carry its measured continent.
	if _, raw, err := s.discoverOffer(s.stash["reg"].(string)); err != nil {
		return err
	} else if strings.Contains(raw, "net_continent") {
		return fmt.Errorf("/discover carries net_continent")
	}
	// A receipt has no field that could carry either value.
	rt := reflect.TypeOf(protocol.UsageReceipt{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.ToLower(rt.Field(i).Tag.Get("json") + rt.Field(i).Name)
		for _, w := range []string{"continent", "bucket", "locality", "region", "addr"} {
			if strings.Contains(tag, w) {
				return fmt.Errorf("the receipt field %s could carry a location", rt.Field(i).Name)
			}
		}
	}
	return nil
}

func (s *plcState) adminDetailCoarse() error {
	d := adminTowerDetailOf(s.t, s.srv, s.towers["T"].id)
	want := map[string]any{"measured": true, "continent": "NA", "has_bucket": true}
	if !reflect.DeepEqual(d["locality"], want) {
		return fmt.Errorf("admin locality = %v, want %v", d["locality"], want)
	}
	raw := string(jsonOf(s.t, d))
	if err := plcNoAddr("the admin Tower detail", raw); err != nil {
		return err
	}
	if strings.Contains(raw, "/24") {
		return fmt.Errorf("the admin Tower detail shows a bucket: %.400s", raw)
	}
	return nil
}

func (s *plcState) logsCoarse() error {
	out := s.logs.String()
	if !strings.Contains(out, "hub=NA link=EU") {
		return fmt.Errorf("no log line names the continents: %.600s", out)
	}
	if !strings.Contains(out, "kind=station_placed") || !strings.Contains(out, "class=") {
		return fmt.Errorf("no log line names the placement class: %.600s", out)
	}
	if err := plcNoAddr("the log", out); err != nil {
		return err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "/24") || strings.Contains(l, "/32") {
			return fmt.Errorf("a log line names a bucket: %q", l)
		}
	}
	return nil
}

func (s *plcState) operatorTypedRegion() error {
	id, err := s.registerNode("198.51.100.30", "ap-southeast-2") // typed; measured EU
	if err != nil {
		return err
	}
	s.stash["reg"] = id
	s.setAnswer("203.0.113.7")
	s.front.setPeer("") // the Tower's link arrives from its own (loopback) peer
	return s.computeTower("T")
}

func (s *plcState) regionDisplayed() error {
	o, _, err := s.discoverOffer(s.stash["reg"].(string))
	if err != nil {
		return err
	}
	s.stash["offer"] = o
	s.stash["detail"] = adminTowerDetailOf(s.t, s.srv, s.towers["T"].id)
	_, raw := s.get(s.srv, "/market")
	s.stash["market"] = raw
	return nil
}

func (s *plcState) labeledDeclared() error {
	o := s.stash["offer"].(map[string]any)
	if o["region"] != "ap-southeast-2" {
		return fmt.Errorf("the offer shows region %v, want the operator's typed ap-southeast-2", o["region"])
	}
	src, _ := o["attribute_sources"].(map[string]any)
	if src["region"] != "declared" {
		return fmt.Errorf("the region is labeled %v, want declared", src["region"])
	}
	return nil
}

func (s *plcState) neverConfused() error {
	o := s.stash["offer"].(map[string]any)
	if o["region"] == "EU" {
		return fmt.Errorf("Core's measured continent is shown as the operator's region")
	}
	for k, v := range o {
		if v == "EU" || strings.Contains(k, "continent") {
			return fmt.Errorf("the offer presents the measured continent (%s=%v)", k, v)
		}
	}
	if src, _ := o["attribute_sources"].(map[string]any); src["region"] == "measured" {
		return fmt.Errorf("a typed region is labeled measured")
	}
	if strings.Contains(s.stash["market"].(string), `"EU"`) {
		return fmt.Errorf("/market presents the measured continent")
	}
	d := s.stash["detail"].(map[string]any)
	if _, has := d["region"]; has {
		return fmt.Errorf("the admin Tower detail presents a region: %v", d["region"])
	}
	loc, _ := d["locality"].(map[string]any)
	if loc["measured"] != true || loc["continent"] != "NA" {
		return fmt.Errorf("the admin Tower detail does not label its continent as measured: %v", loc)
	}
	return nil
}

func (s *plcState) towerClaimsNearness() error {
	s.setAnswer("203.0.113.7")
	s.newTower("T")
	s.helloExtra = map[string]any{"region": "af-south-1", "continent": "AF", "net_continent": "AF",
		"coordinates": map[string]any{"lat": -20.88, "lon": 55.45}, "lat": -20.88, "lon": 55.45,
		"bucket": "192.0.2.0/24", "loc_bucket": "192.0.2.0/24", "locality": map[string]any{"Bucket": "192.0.2.0/24", "Continent": "AF"}}
	return nil
}

func (s *plcState) noneRead() error {
	want := placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}
	got, _ := s.loc("T")
	if err := plcWant(got, want, "after a Hello full of claims"); err != nil {
		return err
	}
	// The claims again on every other message it controls: heartbeat and close-free renewal.
	lt := s.towers["T"]
	frame := map[string]any{}
	_ = json.Unmarshal(jsonOf(s.t, frameFor(lt, s.sess["T"])), &frame)
	for k, v := range s.helloExtra {
		frame[k] = v
	}
	if code, raw := lt.call(s.t, s.front.srv, "/tower/session/heartbeat", jsonOf(s.t, frame), nil); code != http.StatusOK {
		return fmt.Errorf("heartbeat answered %d: %s", code, raw)
	}
	s.b.towerGovSweepOnce(time.Now())
	got, _ = s.loc("T")
	if err := plcWant(got, want, "after a heartbeat full of claims"); err != nil {
		return err
	}
	d := adminTowerDetailOf(s.t, s.srv, lt.id)
	if loc, _ := d["locality"].(map[string]any); loc["continent"] != "NA" {
		return fmt.Errorf("the admin view shows a claimed continent: %v", loc)
	}
	return nil
}

func (s *plcState) hubIntoOthersNetwork() error {
	// The test hubs live on loopback; the canary-vet seam exists for exactly this (production
	// keeps vetPublicIP, which would refuse to dial them at all).
	s.b.canaryVet = nil
	lt := s.newTower("T")
	// The admitted Tower's own hub certificate (the pin it advertises) ...
	_, honestPin := s.plcHub(lt.id, lt.priv, nil)
	// ... and the host its name actually reaches: someone else's machine, with its own
	// certificate and its own identity key, claiming to be this Tower's hub.
	other, err := plcCert()
	if err != nil {
		return err
	}
	_, stranger, _ := plcKey()
	imp, impPin := s.plcHub(lt.id, stranger, other)
	s.stash["impostor"], s.stash["impostorPin"], s.stash["honestPin"] = plcHostPort(imp), impPin, honestPin
	_, err = s.open("T", plcHostPort(imp), honestPin, nil)
	if err != nil {
		return err
	}
	return s.waitKnown("T")
}

func (s *plcState) stationsDialHub() error {
	for i := 0; i < 2; i++ {
		n := s.newNode("198.51.100.20", false)
		if code, raw := s.attachNode(n); code != http.StatusOK {
			return fmt.Errorf("self-attach answered %d: %s", code, raw)
		}
		if n.placedOn != s.towers["T"].id {
			return fmt.Errorf("placed on %s, want the Tower", n.placedOn)
		}
		s.stash[fmt.Sprintf("node%d", i)] = plcNodeCheck(n.out.Endpoint, n.out.EndpointTLSSPKI, n.out.TowerID, n.out.TowerKeyHash)
	}
	now := time.Now()
	s.stash["now"] = now
	s.b.towerHubCheckOnce(now)
	return nil
}

func (s *plcState) pinAndProofFail() error {
	lt := s.towers["T"]
	for i := 0; i < 2; i++ {
		if r := s.stash[fmt.Sprintf("node%d", i)]; r != "pin_mismatch" {
			return fmt.Errorf("Station %d's own hub check answered %q, want pin_mismatch", i, r)
		}
	}
	evs := towerEventsOf(s.t, s.b, lt.id, evHubCheckFailed)
	if len(evs) != 1 || evs[0].Reason != "pin_mismatch" {
		return fmt.Errorf("Core's hub check events = %v, want one pin_mismatch", evs)
	}
	tw, _ := s.b.tower.registry.Get(lt.id)
	// Even a host that satisfies its own pin cannot prove it is the admitted Tower.
	if r := plcNodeCheck(s.stash["impostor"].(string), s.stash["impostorPin"].(string), lt.id, tw.KeyHash); r != "wrong_key" {
		return fmt.Errorf("the impostor's identity proof answered %q, want wrong_key", r)
	}
	// The check is not failing everything: the admitted Tower's own hub passes it.
	honest, _ := s.plcHub(lt.id, lt.priv, nil)
	if r := plcNodeCheck(plcHostPort(honest), s.stash["honestPin"].(string), lt.id, tw.KeyHash); r != "" {
		return fmt.Errorf("the admitted Tower's own hub failed the same check: %q", r)
	}
	if got, _ := s.loc("T"); got != (placement.Locality{}) {
		return fmt.Errorf("a failed hub check moved the Tower's locality to %+v", got)
	}
	return nil
}

func (s *plcState) movedOffByTrigger(trigger string) error {
	lt := s.towers["T"]
	later := s.stash["now"].(time.Time).Add(canaryInterval)
	s.b.towerHubCheckOnce(later)
	s.b.mobilityPassOnce(later)
	moved := map[string]string{}
	for _, ev := range towerEventsOf(s.t, s.b, lt.id, evStationMoveStarted) {
		moved[fmt.Sprint(ev.Detail["station"])] = ev.Reason
	}
	for _, n := range s.nodes {
		reason, ok := moved[n.out.StationID]
		if !ok {
			return fmt.Errorf("Station %s was not moved off (moves: %v)", n.out.StationID, moved)
		}
		if reason != trigger {
			return fmt.Errorf("Station %s was moved off by trigger %q, the spec says %q", n.out.StationID, reason, trigger)
		}
	}
	return nil
}

func (s *plcState) canaryFailuresCount() error {
	lt := s.towers["T"]
	later := s.stash["now"].(time.Time).Add(canaryInterval)
	sum, err := s.b.tower.health.Summary(lt.id, later.Add(-time.Hour))
	if err != nil {
		return err
	}
	if sum.Failed != 2 || sum.Passed != 0 || sum.FailStreak != 2 || !sum.BothLastFailed() {
		return fmt.Errorf("health summary %+v, want 2 failures and no passes", sum)
	}
	if tier := s.b.towerTier(lt.id, later, s.b.fleetTally(later)); tier != placement.TierB {
		return fmt.Errorf("the failing Tower is still tier %v for placement", tier)
	}
	return nil
}

func (s *plcState) throughVPN() error {
	s.liveTower("AF-tower", "192.0.2.10:7443")
	s.liveTower("EU-tower", "198.51.100.10:7443")
	other := s.newNode("192.0.2.77", false) // another Station, in AF
	if code, raw := s.attachNode(other); code != http.StatusOK {
		return fmt.Errorf("the other Station's attach answered %d: %s", code, raw)
	}
	of, _ := s.facts(other)
	s.stash["otherFacts"], s.stash["otherTower"] = of, other.placedOn
	af, _ := s.loc("AF-tower")
	eu, _ := s.loc("EU-tower")
	s.stash["towerLocs"] = []placement.Locality{af, eu}
	vpn := s.newNode("192.0.2.50", false) // the VPN exit, in AF
	s.declareRegion(vpn, "eu")            // where it really runs
	return nil
}

func (s *plcState) placedAsAtExit() error {
	vpn := s.nodes[len(s.nodes)-1]
	got, err := s.facts(vpn)
	if err != nil {
		return err
	}
	if err := plcWant(got, s.b.addrLocality("192.0.2.50"), "the VPN Station"); err != nil {
		return err
	}
	if vpn.placedOn != s.towers["AF-tower"].id {
		return fmt.Errorf("the VPN Station was placed on %s, want the Tower near its exit", vpn.placedOn)
	}
	class, err := s.placedClass(vpn)
	if err != nil {
		return err
	}
	af, _ := s.loc("AF-tower")
	if want := placement.ClassOf(got, af).String(); class != want {
		return fmt.Errorf("placed with class %q, want %q (as at the exit)", class, want)
	}
	return nil
}

func (s *plcState) nothingElseChanges() error {
	other := s.nodes[0]
	of, _ := s.facts(other)
	if of != s.stash["otherFacts"].(placement.Locality) {
		return fmt.Errorf("the other Station's locality changed: %+v", of)
	}
	at, ok, err := s.b.tower.stationStore.ByStation(other.out.StationID)
	if err != nil || !ok || at.Origin.TowerID != s.stash["otherTower"].(string) {
		return fmt.Errorf("the other Station's placement changed: %+v (err %v)", at.Origin, err)
	}
	af, _ := s.loc("AF-tower")
	eu, _ := s.loc("EU-tower")
	if !reflect.DeepEqual([]placement.Locality{af, eu}, s.stash["towerLocs"]) {
		return fmt.Errorf("a Tower's location changed: %+v %+v", af, eu)
	}
	return nil
}

// --- runner -----------------------------------------------------------------------------

func TestPlacementLocalitySpec(t *testing.T) {
	base := &plcState{t: t}
	if dsn, err := pgtest.Private(); err != nil {
		t.Fatalf("test database: %v", err)
	} else if dsn != "" {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
		mir, err := link.NewPGMirror(db)
		if err != nil {
			t.Fatal(err)
		}
		mob, err := mobility.NewPGStore(db)
		if err != nil {
			t.Fatal(err)
		}
		base.pgDB, base.pgMir, base.pgMob = db, mir, mob
	}

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s := &plcState{t: t, pgDB: base.pgDB, pgMir: base.pgMir, pgMob: base.pgMob}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.teardown()
				return ctx, nil
			})

			sc.Step(`^a Tower opens a link session advertising hub endpoint "([^"]*)"$`, s.towerOpensAdvertising)
			sc.Step(`^Core accepts the session$`, func() error {
				if s.slow {
					return s.coreAcceptsSlowSession()
				}
				return s.coreAcceptsSession()
			})
			sc.Step(`^Core resolves the hub host itself and computes the bucket and continent of that address$`, s.coreResolvesHubItself)
			sc.Step(`^stores both on the Tower's live link record, so every broker instance reads the same values$`, s.storedOnLiveLinkRecord)
			sc.Step(`^the Tower's typed region, display name and any Hello field play no part$`, s.typedFieldsPlayNoPart)

			sc.Step(`^a Tower's link connects from an address in one continent$`, s.linkFromOneContinent)
			sc.Step(`^its hub endpoint resolves to an address in another continent$`, s.hubResolvesElsewhere)
			sc.Step(`^Core computes the Tower's location$`, func() error {
				if s.outline != "" {
					return s.computeOutline()
				}
				return s.computesTowerLocation()
			})
			sc.Step(`^the Tower's continent is unknown$`, s.towerContinentUnknown)
			sc.Step(`^Core logs one "tower-locality conflict" line naming the Tower and both continents, never the addresses$`, s.logsOneConflictLine)
			sc.Step(`^the Tower is still eligible for placement, ranked as unknown$`, s.stillEligibleRankedUnknown)

			sc.Step(`^a Tower's hub host resolves to several addresses$`, s.hubResolvesToSeveral)
			sc.Step(`^every address maps to the same bucket$`, s.sameBucket)
			sc.Step(`^that bucket is the Tower's$`, s.thatBucketIsTheTowers)
			sc.Step(`^the addresses span buckets in one continent$`, s.spanBucketsOneContinent)
			sc.Step(`^the Tower has that continent and no bucket$`, s.continentNoBucket)
			sc.Step(`^the addresses span continents$`, s.spanContinents)

			sc.Step(`^a Tower's hub endpoint is "([^"]*)"$`, s.hubEndpointIs)
			sc.Step(`^the Tower's bucket and continent are both unknown$`, s.bucketAndContinentUnknown)
			sc.Step(`^the Tower's bucket is known and its continent is unknown$`, s.bucketKnownContinentUnknown)

			sc.Step(`^a Tower's location was computed when its session opened$`, s.locationComputedAtOpen)
			sc.Step(`^the Tower's hub DNS changes while the session stays open$`, s.dnsChangesWhileOpen)
			sc.Step(`^the stored location is unchanged until the next session opens$`, s.unchangedUntilNextSession)
			sc.Step(`^a new session recomputes it from scratch, leaving no stale value behind$`, s.newSessionRecomputes)

			sc.Step(`^resolving a Tower's hub host takes longer than 2 seconds$`, s.lookupTakesLong)
			sc.Step(`^the session opens on time with an unknown location$`, s.opensOnTimeUnknown)
			sc.Step(`^Core retries the lookup on the next governance sweep, at most once per sweep$`, s.retriesOncePerSweep)

			sc.Step(`^a node self-attaches from a public address$`, s.nodeFromPublic)
			sc.Step(`^Core places the Station$`, s.corePlacesStation)
			sc.Step(`^the Station's bucket and continent are computed from that request's own address$`, s.stationFromRequestAddress)
			sc.Step(`^the node's typed region plays no part$`, s.typedRegionNoPart)

			sc.Step(`^the broker runs behind the host proxy that overwrites the client address header$`, s.behindHostProxy)
			sc.Step(`^a node self-attaches and sends its own copy of that header$`, s.nodeSendsOwnHeader)
			sc.Step(`^Core uses the address the trusted proxy supplied$`, s.usesProxyAddress)
			sc.Step(`^the node's copy is ignored$`, s.nodesCopyIgnored)

			sc.Step(`^a node self-attaches from a loopback or private address$`, s.nodeFromPrivate)
			sc.Step(`^the Station's bucket and continent are unknown$`, s.stationUnknown)

			sc.Step(`^a Station attached from one network and later re-attaches from another$`, s.attachedThenElsewhere)
			sc.Step(`^Core handles the re-attach$`, s.handlesReattach)
			sc.Step(`^the Station's location is the new network's$`, s.locationIsNewNetworks)
			sc.Step(`^no value from the old network survives$`, s.nothingOldSurvives)

			sc.Step(`^a Station with bucket "([^"]*)" and continent "([^"]*)"$`, s.stationWith)
			sc.Step(`^a Tower with bucket "([^"]*)" and continent "([^"]*)"$`, s.towerWith)
			sc.Step(`^Core computes their locality class$`, s.computeClass)
			sc.Step(`^the class is "([^"]*)"$`, s.classIs)

			sc.Step(`^no network-to-continent table is configured$`, s.noTableConfigured)
			sc.Step(`^Core computes locality classes$`, s.computeClasses)
			sc.Step(`^every pair is "unknown" unless the two share a network bucket$`, s.unknownUnlessSameBucket)
			sc.Step(`^/admin/live keeps reporting the table as not configured, as it does today$`, s.adminLiveNotConfigured)

			sc.Step(`^the configured table cannot be read or parsed$`, s.tableUnusable)
			sc.Step(`^the broker starts$`, s.brokerStarts)
			sc.Step(`^placement runs with no continents, exactly as with no table$`, s.placementNoContinents)
			sc.Step(`^/admin/live shows the table's error, as it does today$`, s.adminLiveShowsError)

			sc.Step(`^Core stores or uses a Tower's or a Station's location$`, s.storesOrUsesLocation)
			sc.Step(`^it keeps only the bucket and the continent, never a full address$`, s.keepsOnlyCoarse)
			sc.Step(`^no public endpoint, market listing, receipt or discovery answer carries either value$`, s.neverPublished)
			sc.Step(`^the admin Tower detail shows the Tower's continent and whether it has a bucket, never the bucket itself$`, s.adminDetailCoarse)
			sc.Step(`^log lines name continents and classes, never buckets or addresses$`, s.logsCoarse)

			sc.Step(`^an operator typed a region for their Tower or Station$`, s.operatorTypedRegion)
			sc.Step(`^the region is displayed anywhere$`, s.regionDisplayed)
			sc.Step(`^it is labeled as declared by the operator$`, s.labeledDeclared)
			sc.Step(`^no screen presents Core's measured continent as the operator's region or the other way round$`, s.neverConfused)

			sc.Step(`^a Tower sends a region, a continent, coordinates or a bucket in any message it controls$`, s.towerClaimsNearness)
			sc.Step(`^none of those values are read$`, s.noneRead)

			sc.Step(`^a Tower advertises a hub host that resolves into a network the operator does not run$`, s.hubIntoOthersNetwork)
			sc.Step(`^Stations are placed on that Tower and dial the hub$`, s.stationsDialHub)
			sc.Step(`^the hub's certificate pin and identity proof fail for any host that is not the admitted Tower$`, s.pinAndProofFail)
			sc.Step(`^those Stations fail their re-attach checks and are moved off by the "([^"]*)" trigger \(amended 2026-11-13, founder ruling\)$`, s.movedOffByTrigger)
			sc.Step(`^the Tower's canary failures count against it as they do today$`, s.canaryFailuresCount)

			sc.Step(`^a node self-attaches through a VPN exit far from where it runs$`, s.throughVPN)
			sc.Step(`^the Station is placed as if it were at the exit$`, s.placedAsAtExit)
			sc.Step(`^no other Station's placement and no Tower's location changes$`, s.nothingElseChanges)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/tower/placement_locality.feature"},
			Strict: true, TestingT: t,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the placement locality spec is not satisfied")
	}
}
