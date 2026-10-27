package main

// routing_regression_pins_bdd_test.go makes the @broker scenarios of
// features/routing/regression_pins.feature EXECUTABLE against the REAL broker: the
// upstream_failover harness (foState: a real relayBroker over a real store + a valkey shared
// store, N real stations each with a scripted httptest upstream and a real station loop through
// /agent/result and /agent/stream), the real per-relay routing pass (pickFor) for candidacy,
// the real tool-call verdict store (recordToolProbe), and - for the edge-coin pins - the REAL
// sealed edge fabric (an in-process hub, a share node attached through agent.ServeTower, an
// approved Tower) behind the same broker, so the 50 % fan-out coin in relay() is the production
// coin. Nothing is mocked. Behaviour that does not exist yet (the roger.pref body key, the
// provider.max_price.completion body key, the routing_pref_* counters, the no_match error code,
// capability gating, the stream usage chunk, the bridge honouring min-tps / exclude / pref) is
// asserted honestly against the real broker so the scenario FAILS for the right reason.
//
// Candidacy for a HEADER-shaped request is observed exactly as routing_bdd_test.go observes it:
// pickFor over a registry reduced to {X} with the request's constraints. Candidacy for a
// BODY-shaped request (tools / image_url / provider.*) has no pickFor seam today, so it is
// observed through the real relay: N relays where the node under test is the BETTER-scored
// station, so "never served" can only mean "not a candidate" (a candidate that scores higher
// wins the two-member P2C draw every time).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/enroll"
	"rogerai.fm/roger/v6/internal/towercore/head"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// rpBatch is how many real relays one "routes" step fires. The fan-out coin is a fair 50 %
// per request, so 24 relays landing entirely on one fabric by chance is a 2^-23 event.
const rpBatch = 24

type rpTower struct {
	name, id string
	nodeID   string // the node id the routable row carries (what edge eligibility is judged on)
	closers  []func()
}

type rpResult struct {
	code int
	hdr  http.Header
	body []byte
}

type rpState struct {
	*foState

	prevHubBackoff time.Duration // towerhub.PollBackoff before this suite shortened it

	// the request under test
	pref, minTPS, excludeHdr, maxOutHdr, maxPriceHdr string
	bodyShape                                        string // "", "tools", "image", "text"
	extraBody                                        map[string]any
	callerPriv                                       ed25519.PrivateKey

	// edge fabric
	towerSrv     *httptest.Server
	towers       map[string]*rpTower
	edgeConsumer ed25519.PrivateKey

	// outcomes
	batch       []rpResult
	regCode     int
	regBody     string
	lastHdrCap  string // the X-Roger-Max-Price-Out the last request carried ("" = none)
	lastBodyCap *float64
	logMark     int // the captured-log offset when the last batch started
}

func (s *rpState) resetPins() error {
	if err := s.foState.reset(); err != nil {
		return err
	}
	// A Tower's share node starts polling the hub before the fixture registers its station
	// there, so its first poll is refused and the worker backs off. In production that costs
	// one PollBackoff (2 s) once per node start-up; here it was paid by every scenario's first
	// bridged relay and again at teardown. Restored in teardownPins.
	if s.prevHubBackoff == 0 {
		s.prevHubBackoff = towerhub.PollBackoff
	}
	towerhub.PollBackoff = 50 * time.Millisecond
	s.pref, s.minTPS, s.excludeHdr, s.maxOutHdr, s.maxPriceHdr = "", "", "", "", ""
	s.bodyShape, s.extraBody, s.callerPriv = "", nil, nil
	s.towerSrv, s.towers, s.edgeConsumer = nil, map[string]*rpTower{}, nil
	s.batch, s.regCode, s.regBody = nil, 0, ""
	s.lastHdrCap, s.lastBodyCap = "", nil
	s.model = "qwen3-32b"
	return nil
}

func (s *rpState) teardownPins() {
	defer func() {
		if s.prevHubBackoff != 0 {
			towerhub.PollBackoff = s.prevHubBackoff
		}
	}()
	for _, tw := range s.towers {
		for _, c := range tw.closers {
			c()
		}
	}
	if s.towerSrv != nil {
		s.towerSrv.Close()
		s.towerSrv = nil
	}
	s.foState.teardown()
}

// --- Background ------------------------------------------------------------------

func (s *rpState) emptyRegistry() error { return nil } // built by resetPins
func (s *rpState) feeRate30() error     { s.b.feeRate = 0.30; return nil }

func (s *rpState) defaultOutCap10() error {
	_ = os.Unsetenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT")
	if got := consumerDefaultMaxOut(); got != 10 {
		return fmt.Errorf("consumer default out-cap = %v, want 10", got)
	}
	return nil
}

func (s *rpState) ceiling100() error {
	_ = os.Unsetenv("ROGERAI_MAX_PRICE_OUT")
	if got := maxPriceOutCeiling(); got != 100 {
		return fmt.Errorf("register out-price ceiling = %v, want 100", got)
	}
	return nil
}

// --- Given: direct stations ------------------------------------------------------

// station stands up a REAL station (owner, tunnel, loop, scripted upstream) for the scenario
// model, Tier A (probed + canary-passed) so scoring, not health, decides among them.
func (s *rpState) station(name string, priceIn, priceOut float64) *fstation {
	st := s.standUp(name, stationOpts{model: s.model, priceIn: priceIn, priceOut: priceOut})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, probeFails: 0, ttftMs: 200}
	s.b.mu.Unlock()
	return st
}

func (s *rpState) setOffer(id string, f func(o *protocol.ModelOffer)) {
	s.b.mu.Lock()
	reg := s.b.nodes[id]
	for i := range reg.Offers {
		f(&reg.Offers[i])
	}
	s.b.nodes[id] = reg
	s.b.mu.Unlock()
}

func (s *rpState) setTPS(id string, tps float64) {
	s.b.metricsMu.Lock()
	s.b.tps[id] = tps
	s.b.metricsMu.Unlock()
}

func (s *rpState) setSuccess(id string, rate float64) {
	s.b.metricsMu.Lock()
	s.b.success[id] = rate
	s.b.metricsMu.Unlock()
}

func (s *rpState) onAirPriceTPS(name, model string, out float64, tps int) error {
	s.model = model
	st := s.station(name, out/10, out)
	s.setTPS(st.id, float64(tps))
	if tps < 20 {
		tq := s.b.trust[st.id]
		tq.ttftMs = 2500 // a weak rig: slow first token too
		s.b.trust[st.id] = tq
	}
	return nil
}

func (s *rpState) onAirPrice(name, model string, out float64) error {
	s.model = model
	s.station(name, 0.01, out)
	return nil
}

func (s *rpState) onAirTPS(name, model string, tps int) error {
	s.model = model
	st := s.station(name, 0.10, 0.30)
	s.setTPS(st.id, float64(tps))
	return nil
}

func (s *rpState) onAir(name, model string) error {
	s.model = model
	s.station(name, 0.08, 1.0) // 5000 prompt + 20 completion tokens settle at exactly $0.000420
	return nil
}

func (s *rpState) onAirNoCaps(name, model string) error {
	s.model = model
	st := s.station(name, 0.10, 0.30)
	// The BETTER-scored station: if it still serves a tools/vision request, gating is absent.
	s.setTPS(st.id, 200)
	s.setSuccess(st.id, 0.95)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Capabilities = nil })
	return nil
}

func (s *rpState) onAirVerifiedTools(name, model string) error {
	s.model = model
	st := s.station(name, 0.10, 0.30)
	s.setTPS(st.id, 50)
	s.setSuccess(st.id, 0.60)
	// The SOLE writer of a verified "tools" is the canary verdict (toolcall.go).
	s.b.recordToolProbe(st.id, model, true, false, true)
	return nil
}

func (s *rpState) onAirDeclaredVision(name, model string) error {
	s.model = model
	st := s.station(name, 0.10, 0.30)
	s.setTPS(st.id, 50)
	s.setSuccess(st.id, 0.60)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Capabilities = []string{protocol.CapVision} })
	return nil
}

func (s *rpState) callerOwnsNode(name, model string) error {
	s.model = model
	st := s.station(name, 0.10, 0.30)
	s.callerPriv = st.ownerPriv // the caller IS the operator: self-use, $0
	// A realistic owner has a wallet row: it is created the first time they read /balance or
	// /me (dashboards.go, the same BalanceOf call). Without one, a $0 self-use settle on the
	// POSTGRES store finds no row to update and the relay is served unreceipted - a separate,
	// pre-existing gap (free/self relays skip ensureSeeded) that this coin invariant is not
	// about and must not depend on. The in-memory store never showed it.
	wallet := protocol.UserIDFromPubkey(st.acct)                       // the payer walletOf() resolves: the unified
	if o, ok, err := s.b.db.OwnerByPubkey(st.acct); err == nil && ok { // account wallet when linked
		if w, ok := accountWalletForOwner(o); ok {
			wallet = w
		}
	}
	if _, err := s.b.db.BalanceOf(wallet, s.b.seedFunds); err != nil {
		return fmt.Errorf("seed the owner's wallet row: %w", err)
	}
	return nil
}

func (s *rpState) noDirectNode(model string) error { s.model = model; return nil }

func (s *rpState) noDirectSatisfiesMinTPS(minTPS, model string) error {
	s.model = model
	st := s.station("n-slow", 0.10, 0.30)
	s.setTPS(st.id, 10)
	return nil
}

func (s *rpState) noTowerSatisfiesEither() error {
	_, err := s.standUpTower("tw-slow", s.model, 0, 0, 10)
	return err
}

// --- Given: the edge fabric ------------------------------------------------------

func (s *rpState) ensureTower() error {
	if s.b.tower != nil {
		return nil
	}
	ts, err := newTowerSubsystem(s.b,
		admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour},
		linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore()})
	if err != nil {
		return err
	}
	s.b.tower = ts
	mux := http.NewServeMux()
	s.b.registerTowerRoutes(mux)
	s.towerSrv = httptest.NewServer(mux)
	// The edge consumer is the harness's GitHub-bound, funded buyer: the bridge needs a
	// signed-in account whose wallet matches the one relay resolved, and the DIRECT path
	// needs a logged-in ACCOUNT wallet to spend on a paid station (anonCannotPay). The
	// shared signedInConsumer fixture is an email-only owner, whose u_email_ wallet the
	// direct spend gate (isAccountWallet) does not recognise - see the report.
	if err := s.ensureFunded(); err != nil {
		return err
	}
	// A bridged attempt holds the bridge's own grant ceiling (edgeGrantCeiling, ~$2 at $1/1M)
	// until the Tower's settlement captures it - which this harness never runs - so a batch of
	// bridged relays needs a ceiling per relay. Top the buyer up so the budget, not the
	// scenario, is never what refuses a relay.
	if err := s.fund(190); err != nil {
		return err
	}
	s.edgeConsumer = s.consumerPriv
	return nil
}

// standUpTower is liveSealedFabric (edge_fanout_test.go) parameterised by name, price and a
// measured tps: a real hub, an approved Tower advertising it, a share node self-attached through
// agent.ServeTower serving the model through it, and - when priced - the routable row
// re-published with the price (routableEdgePriced).
func (s *rpState) standUpTower(name, model string, priceIn, priceOut float64, tps float64) (*rpTower, error) {
	if err := s.ensureTower(); err != nil {
		return nil, err
	}
	if tw, ok := s.towers[name]; ok {
		return tw, nil
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &rpTower{name: name}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		if bytes.Contains(reqBody, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong from `+name+`"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	tw.closers = append(tw.closers, upstream.Close)

	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tw.closers = append(tw.closers, func() { _ = hubLn.Close() })
	lt := liveEdgeTower(t, b, srv, name+"-op-"+s.nonce, hubLn.Addr().String())
	tw.id = lt.id

	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	nodeOp := signedInOperator(t, b, name+"-node-"+s.nonce)
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	tw.closers = append(tw.closers, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var stationID string
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			stationID = ats[0].StationID
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tower %s: the share node never attached", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if priceIn > 0 || priceOut > 0 {
		routableEdgePriced(t, b, lt.id, stationID, model, liveEndpointOf(t, b, lt.id), int64(priceIn*1e6), int64(priceOut*1e6))
	}
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("tower %s: no routable row (%v)", name, err)
	}
	tw.nodeID = rows[0].NodeID
	if tps > 0 {
		s.setTPS(tw.nodeID, tps)
	}
	s.towers[name] = tw
	return tw, nil
}

func (s *rpState) towerServes(name, model string) error {
	s.model = model
	_, err := s.standUpTower(name, model, 0, 0, 0)
	return err
}

func (s *rpState) towerServesTPS(name, model string, tps int) error {
	s.model = model
	_, err := s.standUpTower(name, model, 0, 0, float64(tps))
	return err
}

func (s *rpState) towerServesPriced(name, model string, out float64) error {
	s.model = model
	_, err := s.standUpTower(name, model, out, out, 0)
	return err
}

func (s *rpState) towerServesUnmeasured(name, model string) error {
	s.model = model
	tw, err := s.standUpTower(name, model, 0, 0, 0)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	delete(s.b.tps, tw.nodeID)
	s.b.metricsMu.Unlock()
	return nil
}

// deadPricedTower is an approved Tower hosting the model on a routable row that advertises
// a price, a measured tps, and a data plane nothing serves (port 1: connection refused).
// Core places a share node on ONE Tower per model, so two LIVE fabrics for one model cannot
// be stood up; two dead ones let the production bridge loop (hard mode, tower-to-tower
// fallback) reveal its ranking through the order it tries them - the "tower ... failed ...
// trying the next relay" line edgebridge.go writes per attempt.
func (s *rpState) deadPricedTower(name string, price float64, tps float64) error {
	if err := s.ensureTower(); err != nil {
		return err
	}
	if _, ok := s.towers[name]; ok {
		return nil
	}
	op := signedInOperator(s.t, s.b, name+"-op-"+s.nonce)
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		return err
	}
	stationID := "st-" + hexOf([]byte(name+"|"+s.nonce)) // st-<hex>, unique per scenario
	attachStation(s.t, s.b, stationID, lt.id, op.login)
	routableEdgePriced(s.t, s.b, lt.id, stationID, s.model, "127.0.0.1:1", int64(price*1e6), int64(price*1e6))
	tw := &rpTower{name: name, id: lt.id, nodeID: "n-" + stationID}
	if tps > 0 {
		s.setTPS(tw.nodeID, tps)
	}
	s.towers[name] = tw
	return nil
}

func (s *rpState) twoTowersCheapFast(cheap string, cheapPrice float64, fast string, fastPrice float64, tps int) error {
	if err := s.deadPricedTower(cheap, cheapPrice, 20); err != nil {
		return err
	}
	return s.deadPricedTower(fast, fastPrice, float64(tps))
}

// coinWouldSendToEdge: the coin is the production coin, seeded from the request id relay()
// mints - it cannot be forced, so every "routes" step fires rpBatch relays and the Then steps
// read the whole batch. With both fabrics eligible, about half of them ride the Tower.
func (s *rpState) coinWouldSendToEdge() error { return nil }

// --- When: the request routes -----------------------------------------------------

func (s *rpState) requestBody(stream bool) []byte {
	var messages []map[string]any
	switch s.bodyShape {
	case "image":
		messages = []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "what is in this picture?"},
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}}
	default:
		messages = []map[string]any{{"role": "user", "content": utPrompt(s.tokens)}}
	}
	m := map[string]any{"model": s.model, "messages": messages}
	if s.bodyShape == "tools" {
		m["tools"] = []map[string]any{{"type": "function", "function": map[string]any{
			"name": "get_weather", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}}}
	}
	if stream {
		m["stream"] = true
	}
	for k, v := range s.extraBody {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *rpState) caller() (ed25519.PrivateKey, error) {
	if s.callerPriv != nil {
		return s.callerPriv, nil
	}
	if s.edgeConsumer != nil {
		return s.edgeConsumer, nil // funded + signed-in, as the bridge requires
	}
	if err := s.ensureFunded(); err != nil {
		return nil, err
	}
	return s.consumerPriv, nil
}

// relayOnce fires one REAL relay with the scenario's shaping and records the outcome.
func (s *rpState) relayOnce(stream bool) error {
	priv, err := s.caller()
	if err != nil {
		return err
	}
	body := s.requestBody(stream)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	signReq(r, priv, body)
	if s.pref != "" {
		r.Header.Set("X-Roger-Pref", s.pref)
	}
	if s.minTPS != "" {
		r.Header.Set("X-Roger-Min-TPS", s.minTPS)
	}
	if s.excludeHdr != "" {
		r.Header.Set("X-Roger-Exclude-Nodes", s.excludeHdr)
	}
	if s.maxOutHdr != "" {
		r.Header.Set("X-Roger-Max-Price-Out", s.maxOutHdr)
	}
	if s.maxPriceHdr != "" {
		r.Header.Set("X-Roger-Max-Price", s.maxPriceHdr)
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	res := rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()}
	s.batch = append(s.batch, res)
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = w.Code, w.Body.Bytes(), w.Header(), w
	return nil
}

func (s *rpState) relayBatch() error {
	s.batch = nil
	s.logMark = len(s.logs.String())
	s.heartbeat()
	for i := 0; i < rpBatch; i++ {
		if err := s.relayOnce(false); err != nil {
			return err
		}
	}
	return nil
}

// heartbeat re-stamps every scenario station's lastSeen, as a real station's heartbeat loop
// does every few seconds. The harness stamps lastSeen once at stand-up; under a loaded
// full-suite run the Tower fabric stand-up between the Givens and the When can take long
// enough for a station to cross nodeTTL and go stale, which is a liveness story these pins
// do not test (eligibility.feature does).
func (s *rpState) heartbeat() {
	s.b.mu.Lock()
	for _, st := range s.stations {
		s.b.lastSeen[st.id] = time.Now()
	}
	s.b.mu.Unlock()
}

// keepLive returns a refresh that holds every node live NOW (inside nodeTTL) live for the
// rest of a long relay batch: on a loaded machine a batch of hundreds of relays can outlast
// nodeTTL, and the last relays would find no node. A node a scenario aged out on purpose is
// not live now, so it is never revived.
func (s *rpState) keepLive() func() {
	s.b.mu.Lock()
	var live []string
	for id, t := range s.b.lastSeen {
		if time.Since(t) < nodeTTL {
			live = append(live, id)
		}
	}
	s.b.mu.Unlock()
	return func() {
		s.b.mu.Lock()
		for _, id := range live {
			s.b.lastSeen[id] = time.Now()
		}
		s.b.mu.Unlock()
	}
}

// idOf resolves a scenario name to the id the broker knows: a station's node id, a Tower's id,
// or the name itself when neither exists (a foreign id).
func (s *rpState) idOf(name string) string {
	if st, ok := s.stations[name]; ok {
		return st.id
	}
	if tw, ok := s.towers[name]; ok {
		return tw.id
	}
	return name
}

func (s *rpState) routesWithMinTPS(model, min string) error {
	s.model, s.minTPS = model, min
	return s.relayBatch()
}

func (s *rpState) routesExcluding(model, name string) error {
	s.model, s.excludeHdr = model, s.idOf(name)
	return s.relayBatch()
}

func (s *rpState) routesWithPref(model, pref string) error {
	s.model, s.pref = model, pref
	return s.relayBatch()
}

func (s *rpState) callerRoutes(model string) error {
	s.model = model
	return s.relayBatch()
}

func (s *rpState) carriesTools(model string) error {
	s.model, s.bodyShape = model, "tools"
	return s.relayBatch()
}

func (s *rpState) carriesImage(model string) error {
	s.model, s.bodyShape = model, "image"
	return s.relayBatch()
}

func (s *rpState) carriesTextOnly(model string) error {
	s.model, s.bodyShape = model, "text"
	return s.relayBatch()
}

func (s *rpState) headerPrefAndBodyPref(model, hdr, body string) error {
	s.model, s.pref = model, hdr
	s.extraBody = map[string]any{"roger": map[string]any{"pref": body}}
	return s.relayOnce(false)
}

func (s *rpState) carriesMaxOutHeader(model, sent string) error {
	s.model, s.maxOutHdr, s.lastHdrCap, s.lastBodyCap = model, sent, sent, nil
	return s.relayOnce(false)
}

func (s *rpState) carriesBodyMaxOut(model string, completion float64) error {
	s.model, s.maxOutHdr, s.lastHdrCap = model, "", ""
	v := completion
	s.lastBodyCap = &v
	s.extraBody = map[string]any{"provider": map[string]any{"max_price": map[string]any{"completion": completion}}}
	return s.relayOnce(false)
}

// carriesHeaderAndBodyMaxOut sends BOTH carriers of the out cap; the observation is the same
// as the body-only form (does n-dear, priced $12/1M, serve or not).
func (s *rpState) carriesHeaderAndBodyMaxOut(model, hdr string, completion float64) error {
	s.model, s.maxOutHdr, s.lastHdrCap = model, hdr, hdr
	v := completion
	s.lastBodyCap = &v
	s.extraBody = map[string]any{"provider": map[string]any{"max_price": map[string]any{"completion": completion}}}
	return s.relayOnce(false)
}

func (s *rpState) carriesHeaderAndBodyMinTPS(model, hdr string, body float64) error {
	s.model, s.minTPS = model, hdr
	s.extraBody = map[string]any{"roger": map[string]any{"min_tps": body}}
	s.batch = nil
	return s.relayOnce(false)
}

func (s *rpState) handRolledNoCap(model string) error {
	s.model, s.maxOutHdr, s.lastHdrCap, s.lastBodyCap = model, "", "", nil
	s.callerPriv = nil
	return s.relayOnce(false)
}

func (s *rpState) streamServedSettles(model, name string, cost float64) error {
	s.model = model
	st := s.st(name)
	// The scripted usage frame claims 5000 prompt tokens; the broker bills min(claim,
	// re-count), so the request must carry a prompt that large for the claim to be the bill.
	s.tokens = 5000
	st.scriptStream(3, false)
	if err := s.relayOnce(true); err != nil {
		return err
	}
	if s.lastCode != 200 || !bytes.Contains(s.lastBody, []byte("from "+name)) {
		return fmt.Errorf("stream = %d, not %s's: %s", s.lastCode, name, s.lastBody)
	}
	if s.lastRec != nil && !bytes.Contains(s.lastBody, []byte("rogerai-cost=")) {
		// The station's receipt lands after its last frame; the broker's end-of-stream
		// bookkeeping is what the Then steps read, so wait for the settle to appear.
		return fmt.Errorf("the stream ended without the broker's settle (no cost marker): %s", s.lastBody)
	}
	return nil
}

func (s *rpState) nonStreamServed(model, name string) error {
	s.model = model
	if err := s.relayOnce(false); err != nil {
		return err
	}
	if s.lastCode != 200 || s.lastHdr.Get("X-RogerAI-Provider") != s.st(name).id {
		return fmt.Errorf("relay = %d provider=%q, want 200 from %s: %s", s.lastCode, s.lastHdr.Get("X-RogerAI-Provider"), name, s.lastBody)
	}
	return nil
}

func (s *rpState) nodeRegistersAt(model string, out float64) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{
		NodeID: "n-dear-reg-" + s.nonce, PubKey: hexOf(pub), BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: 1, PriceOut: out}},
	}
	reg.SignRegistration(priv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.consumerPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode, s.regBody = w.Code, w.Body.String()
	return nil
}

// --- pickFor-level observation (header-shaped requests) --------------------------

func (s *rpState) pickReqFor(seed int64) pickReq {
	var rng *rand.Rand
	if seed > 0 {
		rng = rand.New(rand.NewSource(seed))
	}
	return pickReq{pref: parsePref(s.pref), promptTokens: 4000, rng: rng}
}

func (s *rpState) exclude() map[string]bool {
	if s.excludeHdr == "" {
		return nil
	}
	return map[string]bool{s.excludeHdr: true}
}

// isCandidateByPick mirrors relay(): the same effective caps (the $10 default when no
// out-cap header was sent, exactly as effectiveRelayMaxOut fills it) and the same header
// constraints, over a registry reduced to {id}.
func (s *rpState) isCandidateByPick(id string) bool {
	minTPS, _ := strconv.ParseFloat(s.minTPS, 64)
	maxIn, _ := strconv.ParseFloat(s.maxPriceHdr, 64)
	sentOut, _ := strconv.ParseFloat(s.maxOutHdr, 64)
	maxOut := effectiveRelayMaxOut(sentOut)
	s.b.mu.Lock()
	saved := s.b.nodes
	s.b.nodes = map[string]protocol.NodeRegistration{id: saved[id]}
	n, _, ok := s.b.pickFor(s.model, false, minTPS, maxIn, maxOut, "", s.exclude(), nil, nil, s.pickReqFor(0))
	s.b.nodes = saved
	s.b.mu.Unlock()
	return ok && n.NodeID == id
}

func (s *rpState) servedBy(id string) (served, total200 int) {
	for _, r := range s.batch {
		if r.code == 200 {
			total200++
			if r.hdr.Get("X-RogerAI-Provider") == id {
				served++
			}
		}
	}
	return served, total200
}

// --- Then --------------------------------------------------------------------------

func (s *rpState) bothCandidates(a, b string) error {
	for _, n := range []string{a, b} {
		if !s.isCandidateByPick(s.idOf(n)) {
			return fmt.Errorf("%q should be a candidate (pref is a scoring knob, never a filter) but pickFor excluded it", n)
		}
	}
	return nil
}

func (s *rpState) outscores(a, b string) error {
	ida, idb := s.idOf(a), s.idOf(b)
	wins := map[string]int{}
	const n = 400
	for i := 0; i < n; i++ {
		s.b.mu.Lock()
		node, _, ok := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, nil, s.pickReqFor(int64(i+1)))
		s.b.mu.Unlock()
		if ok {
			wins[node.NodeID]++
		}
	}
	if wins[ida]*2 <= n {
		return fmt.Errorf("%q won %d/%d picks under pref %q, want the majority over %q (%d)", a, wins[ida], n, s.pref, b, wins[idb])
	}
	return nil
}

func (s *rpState) routingCounter(name string) (float64, bool) {
	if err := s.readAdminLive(); err != nil {
		return 0, false
	}
	if v, ok := s.adminResp["routing_"+name].(float64); ok {
		return v, true
	}
	return 0, false
}

func (s *rpState) routedWithProfile(pref string) error {
	v, ok := s.routingCounter("pref_" + pref)
	if !ok {
		return fmt.Errorf("/admin/live carries no routing_pref_%s counter: the routing pass does not report the profile it ran with", pref)
	}
	if v < 1 {
		return fmt.Errorf("routing_pref_%s = %v after a request carrying pref %q; want >= 1", pref, v, pref)
	}
	for _, other := range []string{"cheap", "balanced", "fast", "reliable"} {
		if other == pref {
			continue
		}
		if ov, ok := s.routingCounter("pref_" + other); ok && ov > 0 {
			return fmt.Errorf("routing_pref_%s = %v, but the only request asked for %q", other, ov, pref)
		}
	}
	return nil
}

func (s *rpState) bridgeDeclined() error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	for i, r := range s.batch {
		if relay := r.hdr.Get("X-RogerAI-Relay"); relay != "" {
			return fmt.Errorf("relay %d/%d rode Tower %q (X-RogerAI-Relay set); the bridge should have declined it", i+1, len(s.batch), relay)
		}
	}
	return nil
}

func (s *rpState) nodeServes(name string) error {
	id := s.idOf(name)
	served, total := s.servedBy(id)
	if total == 0 {
		last := s.batch[len(s.batch)-1]
		return fmt.Errorf("no relay in the batch was served (last: %d %s)", last.code, last.body)
	}
	if served != total {
		seen := map[string]int{}
		for _, r := range s.batch {
			if r.code == 200 {
				seen[r.hdr.Get("X-RogerAI-Provider")+" relay="+r.hdr.Get("X-RogerAI-Relay")+" cost="+r.hdr.Get("X-RogerAI-Cost")]++
			}
		}
		sample := ""
		for _, r := range s.batch {
			if r.code == 200 && r.hdr.Get("X-RogerAI-Provider") != id {
				sample = fmt.Sprintf("%.300s | headers: %v", r.body, r.hdr)
				break
			}
		}
		logs := s.logs.String()
		if len(logs) > 900 {
			logs = logs[len(logs)-900:]
		}
		return fmt.Errorf("%q (%s) served %d of %d successful relays; the others went elsewhere: %v; sample: %s; broker log tail: %s", name, id, served, total, seen, sample, logs)
	}
	return nil
}

func (s *rpState) nodeServesAtZero(name string) error {
	if err := s.nodeServes(name); err != nil {
		return err
	}
	for _, r := range s.batch {
		if c := r.hdr.Get("X-RogerAI-Cost"); r.code == 200 && c != "0" {
			return fmt.Errorf("self-use relay billed X-RogerAI-Cost=%q, want 0", c)
		}
	}
	return nil
}

// towerTriedFirst reads the production bridge's attempt order: every relay in the batch
// must have tried `name` BEFORE the other Tower. With every Tower dead, each relay writes one
// "tower <id> failed for <model>" line per attempt, in attempt order (edgebridge.go).
func (s *rpState) towerTriedFirst(name string) error {
	tw, ok := s.towers[name]
	if !ok {
		return fmt.Errorf("no Tower %q in this scenario", name)
	}
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	all := s.logs.String()
	if s.logMark > len(all) {
		s.logMark = 0
	}
	var firsts []string
	seen := 0
	for _, line := range strings.Split(all[s.logMark:], "\n") {
		i := strings.Index(line, "edge bridge: tower ")
		if i < 0 || !strings.Contains(line, " failed for ") {
			continue
		}
		id := strings.Fields(line[i+len("edge bridge: tower "):])[0]
		if seen%len(s.towers) == 0 {
			firsts = append(firsts, id)
		}
		seen++
	}
	if len(firsts) == 0 {
		return fmt.Errorf("no bridge attempt was logged for the batch (relays: %d %s)", s.batch[0].code, s.batch[0].body)
	}
	for i, id := range firsts {
		if id != tw.id {
			return fmt.Errorf("relay %d tried Tower %s first under pref %q, want %s (%s)", i+1, id, s.pref, name, tw.id)
		}
	}
	return nil
}

func (s *rpState) bridgeMayServeVia(name string) error {
	tw, ok := s.towers[name]
	if !ok {
		return fmt.Errorf("no Tower %q in this scenario", name)
	}
	for _, r := range s.batch {
		if r.code == 200 && r.hdr.Get("X-RogerAI-Relay") == tw.id {
			return nil
		}
	}
	last := s.batch[len(s.batch)-1]
	return fmt.Errorf("no relay in the batch rode Tower %s (last: %d %s)", name, last.code, last.body)
}

func (s *rpState) isCand(name string) error {
	id := s.idOf(name)
	if s.bodyShape != "" || s.lastBodyCap != nil {
		// Body-shaped request: no pickFor seam carries the body, so candidacy is what the real
		// relay did with it (see the file header).
		if served, _ := s.servedBy(id); served == 0 {
			last := s.batch[len(s.batch)-1]
			return fmt.Errorf("%q should be a candidate but no relay was served by it (last: %d %s)", name, last.code, last.body)
		}
		return nil
	}
	if !s.isCandidateByPick(id) {
		return fmt.Errorf("%q should be a candidate but pickFor excluded it", name)
	}
	return nil
}

func (s *rpState) isNotCand(name string) error {
	id := s.idOf(name)
	if s.bodyShape != "" {
		if served, total := s.servedBy(id); served > 0 {
			return fmt.Errorf("%q served %d of %d relays carrying a %s body; it must not be a candidate", name, served, total, s.bodyShape)
		}
		return nil
	}
	if s.isCandidateByPick(id) {
		return fmt.Errorf("%q should NOT be a candidate but pickFor kept it eligible", name)
	}
	return nil
}

func (s *rpState) is503NoMatch() error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	for i, r := range s.batch {
		if r.code != 503 {
			return fmt.Errorf("relay %d = %d, want 503 (nothing satisfies the constraint on either fabric): %s", i+1, r.code, r.body)
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(r.body, &env)
		if env.Error.Code != "no_match" {
			return fmt.Errorf("relay %d: 503 with error.code %q, want \"no_match\": %s", i+1, env.Error.Code, r.body)
		}
	}
	return nil
}

func (s *rpState) effectiveOutCap(want float64) error {
	if s.lastBodyCap != nil {
		// No parser for the body cap exists to ask; the pick is the evidence. n-dear is priced
		// at $12/1M: a cap >= 12 must let it serve, a cap below must refuse it.
		dear, ok := s.stations["n-dear"]
		if !ok {
			return fmt.Errorf("scenario needs n-dear on air to observe a body-carried cap")
		}
		if want >= 12 {
			if s.lastCode != 200 || s.lastHdr.Get("X-RogerAI-Provider") != dear.id {
				return fmt.Errorf("body provider.max_price.completion %v should yield an effective cap of $%v/1M and let n-dear ($12) serve; got %d provider=%q: %s", *s.lastBodyCap, want, s.lastCode, s.lastHdr.Get("X-RogerAI-Provider"), s.lastBody)
			}
			return nil
		}
		if s.lastCode == 200 {
			return fmt.Errorf("body provider.max_price.completion %v should yield an effective cap of $%v/1M and refuse n-dear ($12); it served", *s.lastBodyCap, want)
		}
		return nil
	}
	sent, _ := strconv.ParseFloat(s.lastHdrCap, 64)
	if got := effectiveRelayMaxOut(sent); math.Abs(got-want) > 1e-9 {
		return fmt.Errorf("effectiveRelayMaxOut(%v) = %v, want %v", sent, got, want)
	}
	return nil
}

func (s *rpState) notA4xxForCap() error {
	if s.lastCode >= 400 && s.lastCode < 500 {
		return fmt.Errorf("relay answered %d for a cap above the ceiling; the cap must clamp, not reject: %s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *rpState) noStationServing() error {
	if s.lastCode != 503 || !bytes.Contains(s.lastBody, []byte("no node offers")) {
		return fmt.Errorf("want the clean 503 \"no node offers\", got %d: %s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *rpState) registrationRejected() error {
	if s.regCode == 200 {
		return fmt.Errorf("a $101/1M registration was accepted: %s", s.regBody)
	}
	return nil
}

func (s *rpState) noOfferAboveCeiling(cap float64) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for id, reg := range s.b.nodes {
		for _, o := range reg.Offers {
			if o.PriceOut > cap {
				return fmt.Errorf("node %s offers %s at out $%v/1M, above the $%v ceiling", id, o.Model, o.PriceOut, cap)
			}
		}
	}
	return nil
}

// sseFrames splits the recorded stream into its data frames (the payload after "data: ").
func sseFrames(body []byte) []string {
	var out []string
	for _, frame := range bytes.Split(body, []byte("\n\n")) {
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				out = append(out, strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:")))))
			}
		}
	}
	return out
}

func (s *rpState) usageChunk() (map[string]any, error) {
	frames := sseFrames(s.lastBody)
	if len(frames) == 0 {
		return nil, fmt.Errorf("no SSE data frames in the response: %s", s.lastBody)
	}
	done := -1
	for i, f := range frames {
		if f == "[DONE]" {
			done = i
		}
	}
	if done < 1 {
		return nil, fmt.Errorf("no [DONE] frame (or nothing before it) in the stream: %s", s.lastBody)
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(frames[done-1]), &chunk); err != nil {
		return nil, fmt.Errorf("the frame before [DONE] is not JSON: %q", frames[done-1])
	}
	return chunk, nil
}

func (s *rpState) lastFrameIsUsageChunk() error {
	chunk, err := s.usageChunk()
	if err != nil {
		return err
	}
	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) != 0 {
		return fmt.Errorf("the frame before [DONE] is not a usage chunk with empty choices: %v", chunk)
	}
	usage, _ := chunk["usage"].(map[string]any)
	if usage == nil {
		return fmt.Errorf("the frame before [DONE] carries no usage object: %v", chunk)
	}
	if _, ok := usage["rogerai"].(map[string]any); !ok {
		return fmt.Errorf("the usage chunk carries no usage.rogerai object (the broker's receipt): %v", chunk)
	}
	return nil
}

func (s *rpState) usageReceiptNames(name, model string) error {
	chunk, err := s.usageChunk()
	if err != nil {
		return err
	}
	usage, _ := chunk["usage"].(map[string]any)
	rog, _ := usage["rogerai"].(map[string]any)
	enc, _ := rog["receipt"].(string)
	if enc == "" {
		return fmt.Errorf("usage.rogerai.receipt is absent: %v", chunk)
	}
	rec, err := protocol.DecodeReceipt(enc)
	if err != nil {
		return fmt.Errorf("usage.rogerai.receipt does not decode: %v", err)
	}
	if rec.NodeID != s.st(name).id || rec.Model != model {
		return fmt.Errorf("receipt names node %q model %q, want %s / %s", rec.NodeID, rec.Model, s.st(name).id, model)
	}
	return nil
}

func (s *rpState) usageCostEquals(want float64) error {
	chunk, err := s.usageChunk()
	if err != nil {
		return err
	}
	usage, _ := chunk["usage"].(map[string]any)
	cost, ok := usage["cost"].(float64)
	if !ok {
		return fmt.Errorf("usage.cost is absent: %v", chunk)
	}
	if math.Abs(cost-want) > 1e-9 {
		return fmt.Errorf("usage.cost = %v, want %v", cost, want)
	}
	return nil
}

func (s *rpState) providerHeaderIs(name string) error {
	if p := s.lastHdr.Get("X-RogerAI-Provider"); p != s.st(name).id {
		return fmt.Errorf("X-RogerAI-Provider=%q, want %s", p, s.st(name).id)
	}
	return nil
}

func (s *rpState) nonStreamHeaders() error {
	for _, h := range []string{"X-RogerAI-Receipt", "X-RogerAI-Cost", "X-RogerAI-Tokens-In", "X-RogerAI-Tokens-Out", "X-RogerAI-Balance", "X-RogerAI-Price", "X-RogerAI-TPS", "X-RogerAI-Quality"} {
		if s.lastHdr.Get(h) == "" {
			return fmt.Errorf("non-stream response lacks %s", h)
		}
	}
	return nil
}

func TestRoutingRegressionPinsBDD(t *testing.T) {
	st := &rpState{foState: &foState{t: t, logs: &utLog{}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.resetPins()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardownPins()
				return ctx, nil
			})
			// Background
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)
			sc.Step(`^the consumer default out-cap is \$10/1M$`, st.defaultOutCap10)
			sc.Step(`^the register out-price ceiling is \$100/1M$`, st.ceiling100)
			// Given: direct stations
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price \$([0-9.]+)/1M with tps (\d+)$`, st.onAirPriceTPS)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price \$([0-9.]+)/1M$`, st.onAirPrice)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with measured tps (\d+)$`, st.onAirTPS)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)"$`, st.onAir)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no (?:verified|declared) capabilities$`, st.onAirNoCaps)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no capabilities$`, st.onAirNoCaps)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with canary-verified "tools"$`, st.onAirVerifiedTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with declared "vision"$`, st.onAirDeclaredVision)
			sc.Step(`^the caller owns node "([^"]*)" on air for "([^"]*)"$`, st.callerOwnsNode)
			sc.Step(`^no direct node is on air for "([^"]*)"$`, st.noDirectNode)
			sc.Step(`^no direct node satisfies X-Roger-Min-TPS "([^"]*)" for "([^"]*)"$`, st.noDirectSatisfiesMinTPS)
			sc.Step(`^no Tower satisfies it either$`, st.noTowerSatisfiesEither)
			// Given: the edge fabric
			sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" with measured tps (\d+)$`, st.towerServesTPS)
			sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" at \$([0-9.]+)/1M$`, st.towerServesPriced)
			sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" with no tps measured yet$`, st.towerServesUnmeasured)
			sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)"$`, st.towerServes)
			sc.Step(`^approved Towers "([^"]*)" at \$([0-9.]+)/1M and "([^"]*)" at \$([0-9.]+)/1M with tps (\d+) serve it$`, st.twoTowersCheapFast)
			sc.Step(`^the coin would send this request to the edge$`, st.coinWouldSendToEdge)
			// When
			sc.Step(`^a request routes for "([^"]*)" with X-Roger-Min-TPS "([^"]*)"$`, st.routesWithMinTPS)
			sc.Step(`^a request routes for "([^"]*)" with X-Roger-Exclude-Nodes "([^"]*)"$`, st.routesExcluding)
			sc.Step(`^a request routes for "([^"]*)" with X-Roger-Pref "([^"]*)"$`, st.routesWithPref)
			sc.Step(`^the caller's request routes for "([^"]*)"$`, st.callerRoutes)
			sc.Step(`^a request for "([^"]*)" carries a non-empty tools array$`, st.carriesTools)
			sc.Step(`^a request for "([^"]*)" carries an image_url content part$`, st.carriesImage)
			sc.Step(`^a request for "([^"]*)" carries only text messages$`, st.carriesTextOnly)
			sc.Step(`^a request for "([^"]*)" carries X-Roger-Pref "([^"]*)" and body roger\.pref "([^"]*)"$`, st.headerPrefAndBodyPref)
			sc.Step(`^a request for "([^"]*)" carries X-Roger-Max-Price-Out "([^"]*)"$`, st.carriesMaxOutHeader)
			sc.Step(`^a request for "([^"]*)" carries body provider\.max_price\.completion ([0-9.]+)$`, st.carriesBodyMaxOut)
			sc.Step(`^a request for "([^"]*)" carries X-Roger-Max-Price-Out "([^"]*)" and body provider\.max_price\.completion ([0-9.]+)$`, st.carriesHeaderAndBodyMaxOut)
			sc.Step(`^a request for "([^"]*)" carries X-Roger-Min-TPS "([^"]*)" and body roger\.min_tps ([0-9.]+)$`, st.carriesHeaderAndBodyMinTPS)
			sc.Step(`^a hand-rolled request for "([^"]*)" carries no X-Roger-Max-Price-Out header$`, st.handRolledNoCap)
			sc.Step(`^a streaming request for "([^"]*)" is served by "([^"]*)" and settles at cost \$([0-9.]+)$`, st.streamServedSettles)
			sc.Step(`^a non-streaming request for "([^"]*)" is served by "([^"]*)"$`, st.nonStreamServed)
			sc.Step(`^a node registers "([^"]*)" at out-price \$([0-9.]+)/1M$`, st.nodeRegistersAt)
			// Then
			sc.Step(`^both "([^"]*)" and "([^"]*)" are candidates$`, st.bothCandidates)
			sc.Step(`^"([^"]*)" outscores "([^"]*)"$`, st.outscores)
			sc.Step(`^the broker routes with the ([a-z]+) profile$`, st.routedWithProfile)
			sc.Step(`^the broker's routing pass ran with the ([a-z]+) profile$`, st.routedWithProfile)
			sc.Step(`^the bridge declines the Tower$`, st.bridgeDeclined)
			sc.Step(`^the bridge is declined$`, st.bridgeDeclined)
			sc.Step(`^"([^"]*)" serves$`, st.nodeServes)
			sc.Step(`^"([^"]*)" serves at \$0$`, st.nodeServesAtZero)
			sc.Step(`^"([^"]*)" is tried first$`, st.towerTriedFirst)
			sc.Step(`^the bridge may serve via "([^"]*)"$`, st.bridgeMayServeVia)
			sc.Step(`^"([^"]*)" is a candidate$`, st.isCand)
			sc.Step(`^"([^"]*)" is NOT a candidate$`, st.isNotCand)
			sc.Step(`^the response is 503 with error code "no_match"$`, st.is503NoMatch)
			sc.Step(`^the broker's effective out-cap is \$([0-9.]+)/1M$`, st.effectiveOutCap)
			sc.Step(`^the response is not a 4xx on account of the cap$`, st.notA4xxForCap)
			sc.Step(`^the request finds no station serving$`, st.noStationServing)
			sc.Step(`^the registration is rejected$`, st.registrationRejected)
			sc.Step(`^a request with X-Roger-Max-Price-Out "100" has no offer above \$([0-9.]+)/1M to bind to$`, st.noOfferAboveCeiling)
			sc.Step(`^the last data frame before "\[DONE\]" is a usage chunk with empty choices$`, st.lastFrameIsUsageChunk)
			sc.Step(`^its usage\.rogerai\.receipt decodes to a receipt naming "([^"]*)" and "([^"]*)"$`, st.usageReceiptNames)
			sc.Step(`^its usage\.cost equals ([0-9.]+)$`, st.usageCostEquals)
			sc.Step(`^the response header X-RogerAI-Provider is "([^"]*)"$`, st.providerHeaderIs)
			sc.Step(`^the response carries X-RogerAI-Receipt, X-RogerAI-Cost, X-RogerAI-Tokens-In, X-RogerAI-Tokens-Out, X-RogerAI-Balance, X-RogerAI-Price, X-RogerAI-TPS and X-RogerAI-Quality$`, st.nonStreamHeaders)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/regression_pins.feature"},
			Tags:     "@broker",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/regression_pins @broker scenarios failed (see godog output above)")
	}
}
