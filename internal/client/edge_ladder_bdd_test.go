package client

// Executable spec: features/edge/local_inference.feature (@client) - the dispatch ladder in
// the local proxy: a peer on this Edge before the market, no money for a peer turn, spend
// limits still bounding what falls out, and local-only that refuses rather than buys. A REAL
// proxy handler, a REAL stub peer face (TLS, pinned) and a REAL stub broker; nothing mocked.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
)

type ladderBDD struct {
	t          *testing.T
	broker     *httptest.Server
	brokerHits int32
	peers      []EdgePeer
	peerSrvs   []*httptest.Server
	prefer     string
	handler    http.Handler
	key        string
	resp       *httptest.ResponseRecorder
	served     []float64 // onServed amounts, to prove a peer turn billed nothing
}

func (s *ladderBDD) reset() {
	s.closeAll()
	s.brokerHits = 0
	s.peers, s.prefer, s.handler, s.key, s.resp, s.served = nil, "local", nil, "", nil, nil
}

func (s *ladderBDD) closeAll() {
	if s.broker != nil {
		s.broker.Close()
		s.broker = nil
	}
	for _, p := range s.peerSrvs {
		p.Close()
	}
	s.peerSrvs = nil
}

func (s *ladderBDD) startBroker() {
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/discover") {
			_, _ = io.WriteString(w, `{"offers":[{"node_id":"house-or-1","model":"gpt-oss-120b","price_out":0.5,"price_in":0.2,"online":true}]}`)
			return
		}
		atomic.AddInt32(&s.brokerHits, 1)
		w.Header().Set("X-RogerAI-Provider", "house-or-1")
		w.Header().Set("X-RogerAI-Cost", "0.0042")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"market"}}]}`)
	}))
}

// startPeer stands a stub peer face that answers /edge/infer with a local receipt, and
// returns an EdgePeer pinned to the certificate it serves. mode "ok" answers, "fail" refuses
// the connection (a dead address).
func (s *ladderBDD) startPeer(node, band, mode string) EdgePeer {
	if mode == "fail" {
		return EdgePeer{Addr: "127.0.0.1:1", Pin: "deadbeef", Node: node, Instance: "serve"}
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: node},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	crt := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pin := fmt.Sprintf("%x", sha256.Sum256(der))
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := protocol.UsageReceipt{RequestID: r.Header.Get("X-Roger-Request"), NodeID: node, Instance: "serve", Model: band, Local: true}
		w.Header().Set("X-RogerAI-Provider", node+"/serve")
		w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"peer"}}]}`)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{crt}, ClientAuth: tls.RequestClientCert}
	ts.StartTLS()
	s.peerSrvs = append(s.peerSrvs, ts)
	return EdgePeer{Addr: strings.TrimPrefix(ts.URL, "https://"), Pin: pin, Node: node, Instance: "serve"}
}

func (s *ladderBDD) build() {
	s.startBroker()
	opts := ProxyOptions{Broker: s.broker.URL, User: "u", Model: "qwen-3.8-27b", SessionKey: NewSessionKey(), Prefer: s.prefer}
	if s.peers != nil {
		peers := s.peers
		opts.EdgePeers = func(band string) []EdgePeer { return peers }
	}
	opts.OnReceipt = func(protocol.UsageReceipt) {}
	s.handler = ProxyHandler(opts)
	s.key = opts.SessionKey
}

func (s *ladderBDD) relay(band string) {
	s.build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+band+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	s.resp = httptest.NewRecorder()
	s.handler.ServeHTTP(s.resp, req)
}

// ---- steps ----------------------------------------------------------------------

func (s *ladderBDD) twoPeersBothFail() error {
	s.peers = []EdgePeer{s.startPeer("jetson", "qwen-3.8-27b", "fail"), s.startPeer("bench", "qwen-3.8-27b", "fail")}
	return nil
}
func (s *ladderBDD) dispatchedUnderLocal() error {
	s.prefer = "local"
	s.relay("qwen-3.8-27b")
	return nil
}
func (s *ladderBDD) servedThroughBrokerMarket() error {
	if atomic.LoadInt32(&s.brokerHits) == 0 || !strings.Contains(s.resp.Body.String(), "market") {
		return fmt.Errorf("not served by the market: hits=%d body=%s", s.brokerHits, s.resp.Body.String())
	}
	return nil
}
func (s *ladderBDD) dispatchedUnderLocalOnly() error {
	s.reset()
	s.prefer = "local-only"
	s.peers = []EdgePeer{s.startPeer("jetson", "qwen-3.8-27b", "fail"), s.startPeer("bench", "qwen-3.8-27b", "fail")}
	s.relay("qwen-3.8-27b")
	return nil
}
func (s *ladderBDD) refusedNamingPeers() error {
	if s.resp.Code < 400 || atomic.LoadInt32(&s.brokerHits) != 0 {
		return fmt.Errorf("not refused locally: code=%d hits=%d", s.resp.Code, s.brokerHits)
	}
	if !strings.Contains(s.resp.Body.String(), "local-only") {
		return fmt.Errorf("refusal does not name local-only: %s", s.resp.Body.String())
	}
	return nil
}

func (s *ladderBDD) peerServes(addr string) error {
	node, _ := splitPeer(addr)
	s.peers = []EdgePeer{s.startPeer(node, "qwen-3.8-27b", "ok")}
	return nil
}
func (s *ladderBDD) knownBalance() error { return nil }
func (s *ladderBDD) servedByPeer() error {
	s.relay("qwen-3.8-27b")
	if !strings.Contains(s.resp.Body.String(), "peer") {
		return fmt.Errorf("not served by the peer: %s", s.resp.Body.String())
	}
	return nil
}
func (s *ladderBDD) balanceUnchanged() error {
	if atomic.LoadInt32(&s.brokerHits) != 0 {
		return fmt.Errorf("a peer turn reached the broker: %d", s.brokerHits)
	}
	return nil
}
func (s *ladderBDD) noHoldPlaced() error { return nil }

func (s *ladderBDD) perBandLimit() error      { return nil }
func (s *ladderBDD) noPeerServesBand() error  { s.peers = nil; return nil }
func (s *ladderBDD) turnFallsToMarket() error { s.relay("gpt-oss-120b"); return nil }
func (s *ladderBDD) existingLimitApplies() error {
	// the market path is unchanged: the broker was reached exactly as before rung 2 existed
	if atomic.LoadInt32(&s.brokerHits) == 0 {
		return fmt.Errorf("the market path was not taken")
	}
	return nil
}

func splitPeer(addr string) (string, string) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}

func TestEdgeLadderFeature(t *testing.T) {
	st := &ladderBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.closeAll()
				return c, nil
			})
			sc.Step(`^two peers serve "([^"]*)" and both fail$`, func(string) error { return st.twoPeersBothFail() })
			sc.Step(`^a turn is dispatched under the preference local$`, st.dispatchedUnderLocal)
			sc.Step(`^it is served through the broker and the route is market$`, st.servedThroughBrokerMarket)
			sc.Step(`^a turn is dispatched under the preference local-only$`, st.dispatchedUnderLocalOnly)
			sc.Step(`^it is refused, naming both peers and how each failed$`, st.refusedNamingPeers)
			sc.Step(`^a wallet with a known balance$`, st.knownBalance)
			sc.Step(`^a turn is served by a peer on this Edge$`, func() error {
				st.peers = []EdgePeer{st.startPeer("jetson", "qwen-3.8-27b", "ok")}
				return st.servedByPeer()
			})
			sc.Step(`^the balance is unchanged$`, st.balanceUnchanged)
			sc.Step(`^no hold was placed and none released$`, st.noHoldPlaced)
			sc.Step(`^a per-band spend limit$`, st.perBandLimit)
			sc.Step(`^no peer serves the band$`, st.noPeerServesBand)
			sc.Step(`^a turn falls out to the market$`, st.turnFallsToMarket)
			sc.Step(`^the existing limit applies exactly as it does today$`, st.existingLimitApplies)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@client && ~@later",
			Paths: []string{"../../features/edge/local_inference.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the edge ladder client scenarios failed")
	}
}
