package main

// persist_state_harness_test.go is the shared fixture of the five persistent-state runners
// (shared_store_readiness, monthly_cap_concurrency, edge_attempt_cap_shared,
// price_lock_durable, cooling_alert_shared). It builds on the upstream_failover harness
// (foState: a real relay broker over the real store - Postgres when
// ROGERAI_TEST_DATABASE_URL is set, else the in-memory reference - a miniredis-backed
// shared store, and real stations with scripted upstreams behind real tunnels) and adds:
// a second broker instance over the SAME stores, a "restart" (a fresh broker over the same
// stores, with empty process memory), an unreachable shared store, and a body builder that
// carries max_tokens so a request's hold is an exact, chosen amount.

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/towercore/enroll"
	"rogerai.fm/roger/v6/internal/towercore/head"
)

// psState is one scenario of a persistent-state runner.
type psState struct {
	*foState
	maxTokens int // when > 0 the relay body carries max_tokens (sizes the hold exactly)

	gate     chan struct{} // closed to release stations that hold their requests in flight
	gateOnce sync.Once
	holding  bool // stations hold requests on the gate while true

	towerSrv *httptest.Server // the Tower routes server, when a scenario stands up the fabric
}

func newPSState(fo *foState) *psState { return &psState{foState: fo} }

func (s *psState) psReset() error {
	if err := s.foState.reset(); err != nil {
		return err
	}
	s.maxTokens, s.holding, s.towerSrv = 0, false, nil
	s.gate = make(chan struct{})
	s.gateOnce = sync.Once{}
	return nil
}

func (s *psState) release() { s.gateOnce.Do(func() { close(s.gate) }) }

// instB returns the scenario's second broker instance over the same durable and shared
// stores, creating it on first use (foState.instanceB copies the station registry and
// tunnels, as the shared registry mirror does in production).
func (s *psState) instB() *broker {
	if s.b2 == nil {
		s.instanceB()
	}
	return s.b2
}

// restart replaces instance A with a fresh broker over the same stores: everything kept in
// process memory (maps, counters, timers) is gone, everything durable or shared survives.
func (s *psState) restart() *broker {
	fresh := s.newBroker()
	fresh.recount = s.b.recount
	for id, n := range s.b.nodes {
		fresh.nodes[id] = n
		fresh.lastSeen[id] = time.Now()
		fresh.tunnels[id] = s.b.tunnels[id]
	}
	fresh.multiInstance = s.b.multiInstance
	s.b = fresh
	return fresh
}

// ensureTowerOn gives broker b a real Tower subsystem (in-memory admission, custody,
// enrollment and attach stores, as towerTestBrokerOn does) with its routes served.
func (s *psState) ensureTowerOn(b *broker) error {
	if b.tower != nil {
		return nil
	}
	ts, err := newTowerSubsystem(b,
		admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour},
		linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore()})
	if err != nil {
		return err
	}
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	s.towerSrv = httptest.NewServer(mux)
	return nil
}

// sharedDown makes the shared store unreachable for every instance.
func (s *psState) sharedDown() {
	if s.mr != nil {
		s.mr.Close()
	}
}

// bodyFor builds a chat body for model; with maxTokens set the hold is
// maxTokens x out price (+ prompt x in price), so a station priced out $1/1M and in $0
// with max_tokens 100000 holds exactly $0.10.
func (s *psState) bodyFor(model string, stream bool) []byte {
	m := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": utPrompt(s.tokens)}},
	}
	if s.maxTokens > 0 {
		m["max_tokens"] = s.maxTokens
	}
	if stream {
		m["stream"] = true
	}
	b, _ := json.Marshal(m)
	return b
}

// psResult is one relay's outcome.
type psResult struct {
	code int
	body []byte
	hdr  http.Header
}

// relayOn fires one signed relay for model on broker b as priv.
func (s *psState) relayOn(b *broker, priv ed25519.PrivateKey, model string, stream bool) psResult {
	body := s.bodyFor(model, stream)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, priv, body)
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	b.relay(w, r)
	return psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
}

// holdingStation stands up a station whose upstream keeps every request in flight until
// the scenario releases the gate, then answers a real completion.
func (s *psState) holdingStation(name, model string, in, out float64) *fstation {
	st := s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out})
	gate := s.gate
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		<-gate
		utRealCompletion(w)
	})
	return st
}

// recordSpend writes a captured spend of amount for wallet this month through the real
// hold + settle path of the store (a hold, then Finalize at that cost).
func (s *psState) recordSpend(wallet string, amount float64) error {
	if amount <= 0 {
		return nil
	}
	if _, err := s.db.AddCredits(wallet, amount); err != nil {
		return err
	}
	req := fmt.Sprintf("seed-spend-%d", time.Now().UnixNano())
	ok, err := s.db.HoldFor(wallet, req, amount)
	if err != nil || !ok {
		return fmt.Errorf("seed spend hold for %s: ok=%v err=%v", wallet, ok, err)
	}
	rec := protocol.UsageReceipt{RequestID: req, NodeID: "seed-node", Model: "seed"}
	if _, err := s.db.Finalize(wallet, "seed-node", amount, amount, 0, rec); err != nil {
		return fmt.Errorf("seed spend settle: %w", err)
	}
	return nil
}

// psGitHubConsumer binds a fresh GitHub-linked, email-verified account, funds its account
// wallet, and returns the wallet and the device key that signs for it.
func psGitHubConsumer(s *foState) (string, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", nil, err
	}
	gid := time.Now().UnixNano()%1_000_000_000 + 1_000_000
	pubHex := hexOf(pub)
	o := store.Owner{GitHubID: gid, Login: "gh-consumer-" + pubHex[:8], Pubkey: pubHex,
		Email: pubHex[:8] + "@gh.test", EmailVerifiedAt: time.Now().Unix()}
	if err := s.db.BindOwner(o); err != nil {
		return "", nil, err
	}
	w, ok := accountWalletForOwner(o)
	if !ok {
		return "", nil, fmt.Errorf("no account wallet for the GitHub consumer")
	}
	if _, err := s.db.AddCredits(w, 1000); err != nil {
		return "", nil, err
	}
	return w, priv, nil
}

// errCode reads error.code from a JSON error body ("" when absent).
func errCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func bodyHas(body []byte, s string) bool { return strings.Contains(string(body), s) }

// psRetireFabricOperator lets liveSealedFabric run once per scenario on the shared test
// Postgres. That helper binds fixed, email-verified operators ("fanout-tower-op", "fanout-node-op") and was
// written for a fresh store per test; on the Postgres run every scenario shares one database,
// so the previous scenario's operators would collide on the verified-email unique index. This
// marks earlier operators' email unverified (it changes nothing a scenario asserts). A no-op
// on the in-memory store, which is fresh per scenario.
func psRetireFabricOperator(s *foState) error {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if s.pg == nil || dsn == "" {
		return nil
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE rogerai.owners SET email_verified_at = NULL WHERE login IN ('fanout-tower-op', 'fanout-node-op')`)
	return err
}
