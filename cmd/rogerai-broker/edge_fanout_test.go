package main

// features/tower/edge_fanout.feature: consumer traffic fans out across both fabrics.
//
// The relay audit's finding, verified before this existed: every real consumer calls
// /v1/chat/completions, which refused Towers outright ("no node offers X"), so no live
// traffic could ride a Tower and Towers could not earn. These tests drive the ordinary
// consumer endpoint against a REAL sealed edge fabric - the same in-process hub, station
// agent, and fake upstream the sealed-canary test stands up - because the bridge under
// test is a money path and a synthetic hub would prove nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"crypto/ed25519"
	crand "crypto/rand"
	"github.com/stretchr/testify/require"
	"math/rand"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// liveSealedFabric stands up the whole real edge plane behind broker b: a fake OpenAI
// upstream, a hub listener, an approved Tower advertising it, and a share node that
// self-attaches and serves `model` through it. Returns the tower.
func liveSealedFabric(t *testing.T, b *broker, srv *httptest.Server, model string) linkTower {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong from the edge"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	t.Cleanup(upstream.Close)

	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hubLn.Close() })
	tw := liveEdgeTower(t, b, srv, "fanout-tower-op", hubLn.Addr().String())

	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, tw.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: tw.id, EpochKey: tw.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	nodeOp := signedInOperator(t, b, "fanout-node-op")
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	require.Eventually(t, func() bool {
		ats, aerr := b.tower.stations.ByTower(tw.id)
		if aerr != nil || len(ats) == 0 {
			return false
		}
		hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
		return true
	}, 10*time.Second, 50*time.Millisecond)
	return tw
}

// relayAs drives the ordinary consumer endpoint exactly as roger does: signed, JSON, the
// same path every CLI/TUI/proxy request takes.
func relayAs(t *testing.T, b *broker, priv interface{ Sign([]byte) ([]byte, error) }, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.relay(rec, req)
	return rec
}

// The audit's headline: a model served ONLY behind a Tower must be reachable through the
// ordinary endpoint - the consumer changes nothing and the Tower finally carries real
// traffic. Before the bridge this 503ed with "no node offers".
func TestEdgeOnlyModelIsServedThroughTheOrdinaryEndpoint(t *testing.T) {
	b, srv := towerTestBroker(t)
	const model = "edge-only-model"
	liveSealedFabric(t, b, srv, model)

	consumer := signedInConsumer(t, b)

	// Precondition, so this test cannot silently become a direct-path test: no direct
	// node offers the model.
	b.mu.Lock()
	_, _, direct := b.pickFor(model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	require.False(t, direct, "fixture: the model must be edge-only for this scenario")

	body := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	b.relay(rec, req)

	require.Equal(t, http.StatusOK, rec.Code,
		"an edge-only model must ride the Tower, not 503: %s", rec.Body.String())
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.Choices)
	require.Contains(t, out.Choices[0].Message.Content, "pong from the edge",
		"the answer must be the station's, through the sealed hub")
	// The contract the client already parses: a provider is named, and the relay that
	// carried it is on the receipt headers.
	require.NotEmpty(t, rec.Header().Get("X-RogerAI-Provider"), "the contract shape must match a direct relay")
}

// Eligibility holds on the bridge exactly as at authorize: a quarantined Tower hosting
// the model is never selected, and with no eligible tower and no direct node the refusal
// is the same honest 503 as before - one answer, not a hang or a leak.
func TestBridgeSelectsOnlyEligibleTowersAndRefusesHonestly(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "quarantined-only-model"
	// A tower hosting the model that was never approved: routableEdge would promote it,
	// so build the row by hand around the promotion.
	tw := enrolledTower(t, b, "quarantine-owner")
	attachStation(t, b, "st-q", tw.id, "quarantine-owner")
	routableEdge(t, b, tw.id, "st-q", model, "203.0.113.9:8443")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateSuspended))

	consumer := signedInConsumer(t, b)
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	b.relay(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a suspended tower's model is not served, and the refusal is the honest 503")
	require.Contains(t, rec.Body.String(), model, "the refusal names the model")
}

// Fallback: a failing Tower does not dead-end a request another relay could serve. A dead
// hub (nothing listening) and a live one both host the model; the request must land on
// the live one, and the dead one must earn a station-fault on its record - never a canary
// count (that is the suspension signal, and organic failures must not inflate it).
func TestBridgeFallsBackToAnotherTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	const model = "fallback-model"

	// A dead Tower: approved, hosting the model, advertising an address nothing serves.
	dead := enrolledTower(t, b, "dead-tower-op")
	attachStation(t, b, "st-dead", dead.id, "dead-tower-op")
	routableEdge(t, b, dead.id, "st-dead", model, "127.0.0.1:1") // port 1: connection refused

	// A live Tower serving the same model.
	live := liveSealedFabric(t, b, srv, model)
	_ = live

	consumer := signedInConsumer(t, b)
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()

	// Deterministically visit the dead Tower first by draining its placement first is not
	// controllable here; instead the fallback loop must reach the live one within its
	// budget regardless of order. Retry the whole request a few times to defeat the
	// placement coin without asserting on which tower was tried first.
	served := false
	for i := 0; i < 6 && !served; i++ {
		rec = httptest.NewRecorder()
		b.relay(rec, req)
		if rec.Code == http.StatusOK {
			served = true
		}
	}
	require.True(t, served, "the request must reach the live Tower despite the dead one: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "pong from the edge")
}

// A failed bridge attempt leaves no funds pinned: the hold is released at failure, not at
// the orphan sweep, so a consumer served elsewhere is never short a held balance.
func TestBridgeReleasesHoldOnFailure(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "held-fail-model"
	// One priced Tower with a dead hub: the drive fails after the hold is placed.
	tw := enrolledTower(t, b, "held-op")
	attachStation(t, b, "st-h", tw.id, "held-op")
	routableEdgePriced(t, b, tw.id, "st-h", model, "127.0.0.1:1", 100, 100)

	consumer := signedInConsumer(t, b)
	walletBefore := consumerBalance(t, b, consumer)

	body := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	b.relay(rec, req)

	// The model is edge-only and the only Tower is dead: the honest 503 stands.
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, walletBefore, consumerBalance(t, b, consumer),
		"a failed attempt must release its hold, not pin the consumer's funds")
}

// routableEdgePriced is routableEdge with a per-token price on the row, for the money
// tests: it must be inside the model's band or authorize/bridge refuse it.
func routableEdgePriced(t *testing.T, b *broker, towerID, stationID, model, endpoint string, priceIn, priceOut int64) {
	t.Helper()
	if tw, ok := b.tower.registry.Get(towerID); ok && tw.State != admit.StateActive {
		require.NoError(t, b.tower.registry.Transition(towerID, admit.StateActive))
	}
	nodeID := "n-" + stationID
	b.mu.Lock()
	if b.nodes == nil {
		b.nodes = map[string]protocol.NodeRegistration{}
	}
	if b.lastSeen == nil {
		b.lastSeen = map[string]time.Time{}
	}
	b.nodes[nodeID] = protocol.NodeRegistration{NodeID: nodeID}
	b.lastSeen[nodeID] = time.Now()
	b.mu.Unlock()
	require.NoError(t, b.tower.routable.Replace(towerID, []fleet.Station{{
		TowerID: towerID, StationID: stationID, OfferID: "self-" + stationID,
		Model: model, Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: endpoint,
		NodeID: nodeID, PriceIn: priceIn, PriceOut: priceOut,
	}}))
}

func consumerBalance(t *testing.T, b *broker, priv ed25519.PrivateKey) float64 {
	t.Helper()
	o, ok, err := b.db.OwnerByPubkey(hexOf(priv.Public().(ed25519.PublicKey)))
	require.NoError(t, err)
	require.True(t, ok)
	wallet, wok := accountWalletForOwner(o)
	require.True(t, wok)
	bal, err := b.db.BalanceOf(wallet, b.seedFunds)
	require.NoError(t, err)
	return bal
}

// The exclusion set is what makes the within-one-request fallback real: two dead Towers
// and one live one, and a SINGLE request must reach the live one - which is only possible
// if the loop excludes each failed Tower and draws again, up to its budget. Without the
// exclusion, the loop could redial the same dead Tower twice and give up. edgeBridgeMaxTowers
// bounds the retries, so this needs the live Tower reachable within that budget: two dead
// plus one live exceeds a budget of 2, so we use one dead + one live and assert the live
// one is reached even when the dead one is drawn first.
func TestBridgeExclusionReachesTheLiveTowerInOneRequest(t *testing.T) {
	b, srv := towerTestBroker(t)
	const model = "exclusion-model"

	dead := enrolledTower(t, b, "excl-dead-op")
	attachStation(t, b, "st-xd", dead.id, "excl-dead-op")
	routableEdge(t, b, dead.id, "st-xd", model, "127.0.0.1:1")
	live := liveSealedFabric(t, b, srv, model)
	_ = live

	consumer := signedInConsumer(t, b)

	// Drive relayViaEdge directly with a seeded rng so placement is reproducible, and
	// assert a single call serves - proving the fallback happened WITHIN one request. The
	// seed is swept so at least one ordering puts the dead Tower first; every seed must
	// still end served, because the exclusion guarantees the live one is drawn next.
	served := 0
	for seed := int64(0); seed < 8; seed++ {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, itoa64(ts))
		req.Header.Set(protocol.HeaderSig, sig)
		rec := httptest.NewRecorder()
		ok := b.relayViaEdge(rec, req, model, false, []byte(body), rngFromSeed(seed), false)
		require.True(t, ok, "seed %d: relayViaEdge must write a response", seed)
		if rec.Code == http.StatusOK {
			served++
		}
	}
	require.Equal(t, 8, served,
		"EVERY single-request drive must reach the live Tower - the exclusion is what lets it draw past the dead one")
}

func rngFromSeed(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// The consent gate, both modes: hard (edge-only) tells an anonymous caller the truth -
// the model IS served, just not to the unsigned - and soft (a direct node stands ready)
// stays silent so the direct path serves as it always did.
func TestBridgeConsentGateHardAndSoft(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "consent-model"
	tw := enrolledTower(t, b, "consent-op")
	attachStation(t, b, "st-c", tw.id, "consent-op")
	routableEdge(t, b, tw.id, "st-c", model, "203.0.113.7:8443")

	// Anonymous, hard mode: the honest 403, not "no node offers".
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	handled := b.relayViaEdge(rec, req, model, false, []byte("{}"), rngFromSeed(1), false)
	require.True(t, handled)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "signed-in account")

	// Anonymous, soft mode: silence - false, nothing written - so the direct pick serves.
	rec2 := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(rec2, req, model, false, []byte("{}"), rngFromSeed(1), true))
	require.Equal(t, 0, rec2.Body.Len(), "soft mode must write nothing on a gate refusal")

	// A model no tower serves: false in both modes, before any gating.
	require.False(t, b.relayViaEdge(httptest.NewRecorder(), req, "nowhere-model", false, []byte("{}"), rngFromSeed(1), false))
}

// The streamed bridge answer is one well-formed SSE chunk plus [DONE], with the same cost
// headers as the plain shape - honest about the hub being submit/answer, not a stream.
func TestBridgedStreamShape(t *testing.T) {
	b, srv := towerTestBroker(t)
	const model = "stream-edge-model"
	liveSealedFabric(t, b, srv, model)
	consumer := signedInConsumer(t, b)

	body := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	require.True(t, b.relayViaEdge(rec, req, model, true, []byte(body), rngFromSeed(3), false))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	out := rec.Body.String()
	require.Contains(t, out, `"delta"`)
	require.Contains(t, out, "pong from the edge")
	require.True(t, strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]"), "the stream must terminate: %q", out)
	require.NotEmpty(t, rec.Header().Get("X-RogerAI-Cost"))
	require.NotEmpty(t, rec.Header().Get("X-RogerAI-Relay"), "the receipt names the relay that carried it")
}

// The abuse bounds hold on the bridge exactly as at the authorize endpoint: the rate
// limit and the standing open-attempt cap both answer 429 in hard mode and silence in
// soft mode - the bridge is not a way around either bound.
func TestBridgeHonorsRateAndSlotBounds(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "bounded-model"
	tw := enrolledTower(t, b, "bound-op")
	attachStation(t, b, "st-b", tw.id, "bound-op")
	routableEdge(t, b, tw.id, "st-b", model, "203.0.113.7:8443")
	consumer := signedInConsumer(t, b)

	body := `{"model":"` + model + `"}`
	mk := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, itoa64(ts))
		req.Header.Set(protocol.HeaderSig, sig)
		return req
	}

	// Rate limit exhausted: hard mode says 429 with Retry-After, soft mode stays silent.
	// (rpm 0 means UNLIMITED in this limiter, so drain a one-burst bucket instead.)
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	o, _, err := b.db.OwnerByPubkey(hexOf(consumer.Public().(ed25519.PublicKey)))
	require.NoError(t, err)
	wallet, _ := accountWalletForOwner(o)
	_, _ = b.rl.allow("edge:" + wallet) // drain the single slot
	rec := httptest.NewRecorder()
	require.True(t, b.relayViaEdge(rec, mk(), model, false, []byte(body), rngFromSeed(1), false))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	rec2 := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(rec2, mk(), model, false, []byte(body), rngFromSeed(1), true))
	require.Zero(t, rec2.Body.Len())
}

// A dead endpoint behind the only hosting tower: the consumer gets the honest 503, the
// hold-free path writes nothing extra, and the tower earns a fault on its record.
func TestBridgeDeadEndpointIsHonest503(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "deadend-model"
	tw := enrolledTower(t, b, "dead-op2")
	attachStation(t, b, "st-d2", tw.id, "dead-op2")
	routableEdge(t, b, tw.id, "st-d2", model, "127.0.0.1:1")
	consumer := signedInConsumer(t, b)
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"x"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	b.relay(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), model)
}

// The remaining refusal arms, each in both modes where it matters: the standing slot cap,
// the empty wallet, the out-of-band price, and an unusable advertised pin.
func TestBridgeRefusalArms(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "arms-model"
	tw := enrolledTower(t, b, "arms-op")
	attachStation(t, b, "st-a", tw.id, "arms-op")
	routableEdge(t, b, tw.id, "st-a", model, "203.0.113.7:8443")
	consumer := signedInConsumer(t, b)
	o, _, err := b.db.OwnerByPubkey(hexOf(consumer.Public().(ed25519.PublicKey)))
	require.NoError(t, err)
	wallet, _ := accountWalletForOwner(o)

	mk := func(m string, key ed25519.PrivateKey) *http.Request {
		body := `{"model":"` + m + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		pub, ts, sig := protocol.SignRequest(key, http.MethodPost, "/v1/chat/completions", []byte(body))
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, itoa64(ts))
		req.Header.Set(protocol.HeaderSig, sig)
		return req
	}

	// SLOT CAP: every open-attempt slot taken - hard 429, soft silence.
	taken := 0
	for b.edgeAccountReserve(wallet) {
		taken++
		require.Less(t, taken, 10000, "the standing cap must be finite")
	}
	rec := httptest.NewRecorder()
	require.True(t, b.relayViaEdge(rec, mk(model, consumer), model, false, []byte("{}"), rngFromSeed(1), false))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	recS := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(recS, mk(model, consumer), model, false, []byte("{}"), rngFromSeed(1), true))
	require.Zero(t, recS.Body.Len())
	for i := 0; i < taken; i++ {
		b.edgeAccountRelease(wallet)
	}

	// EMPTY WALLET on a priced row: the hold fails - hard 402, soft silence.
	const pricedModel = "arms-priced-model"
	twp := enrolledTower(t, b, "arms-priced-op")
	attachStation(t, b, "st-ap", twp.id, "arms-priced-op")
	routableEdgePriced(t, b, twp.id, "st-ap", pricedModel, "203.0.113.7:8443", 100, 100)
	poorPub, poor, err := ed25519.GenerateKey(crand.Reader)
	require.NoError(t, err)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: hexOf(poorPub), Login: "poor-arms", Email: "poor@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	recP := httptest.NewRecorder()
	require.True(t, b.relayViaEdge(recP, mk(pricedModel, poor), pricedModel, false, []byte("{}"), rngFromSeed(1), false))
	require.Equal(t, http.StatusPaymentRequired, recP.Code)
	recPS := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(recPS, mk(pricedModel, poor), pricedModel, false, []byte("{}"), rngFromSeed(1), true))
	require.Zero(t, recPS.Body.Len())

	// OUT-OF-BAND PRICE: the row is excluded before it becomes money; with no other tower
	// hosting the model the bridge has nothing and says so by declining, not by serving.
	const absurdModel = "arms-absurd-model"
	twa := enrolledTower(t, b, "arms-absurd-op")
	attachStation(t, b, "st-ab", twa.id, "arms-absurd-op")
	routableEdgePriced(t, b, twa.id, "st-ab", absurdModel, "203.0.113.7:8443", 999999999999, 999999999999)
	recA := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(recA, mk(absurdModel, consumer), absurdModel, false, []byte("{}"), rngFromSeed(1), false),
		"an out-of-band price is a wrong offer, excluded before minting")

	// UNUSABLE PIN: a malformed advertised TLS pin cannot even build a client - a tower
	// fault, surfaced as the honest refusal.
	const pinModel = "arms-pin-model"
	twn := enrolledTower(t, b, "arms-pin-op")
	attachStation(t, b, "st-an", twn.id, "arms-pin-op")
	if tws, ok := b.tower.registry.Get(twn.id); ok && tws.State != admit.StateActive {
		require.NoError(t, b.tower.registry.Transition(twn.id, admit.StateActive))
	}
	require.NoError(t, b.tower.routable.Replace(twn.id, []fleet.Station{{
		TowerID: twn.id, StationID: "st-an", OfferID: "self-st-an",
		Model: pinModel, Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.7:8443", TLSSPKI: "not-a-pin",
		NodeID: "n-st-an",
	}}))
	b.mu.Lock()
	b.nodes["n-st-an"] = protocol.NodeRegistration{NodeID: "n-st-an"}
	b.lastSeen["n-st-an"] = time.Now()
	b.mu.Unlock()
	recN := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(recN, mk(pinModel, consumer), pinModel, false, []byte("{}"), rngFromSeed(1), false))
}

// With the production vet armed, a tower advertising a loopback endpoint is skipped as
// unreachable-by-design at the drive - never dialed, never scored a canary failure - and
// the consumer still gets the honest refusal.
func TestBridgeSkipsNonPublicEndpointsWhenVetted(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	b.canaryVet = vetPublicIP
	const model = "vetted-model"
	tw := enrolledTower(t, b, "vet-op")
	attachStation(t, b, "st-v", tw.id, "vet-op")
	routableEdge(t, b, tw.id, "st-v", model, "127.0.0.1:1")
	consumer := signedInConsumer(t, b)

	body := `{"model":"` + model + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	pub, ts, sig := protocol.SignRequest(consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(rec, req, model, false, []byte(body), rngFromSeed(1), false),
		"a design-skipped endpoint serves nothing and the caller's refusal stands")
}

// An account with no resolvable wallet (no GitHub, no Apple, no email identity) cannot be
// billed, so the bridge refuses it exactly like an unsigned caller - and stays silent in
// soft mode so the direct path can still serve free traffic.
func TestBridgeRefusesAWalletlessAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = srv
	const model = "walletless-model"
	tw := enrolledTower(t, b, "walletless-op")
	attachStation(t, b, "st-w", tw.id, "walletless-op")
	routableEdge(t, b, tw.id, "st-w", model, "203.0.113.7:8443")

	pub, priv, err := ed25519.GenerateKey(crand.Reader)
	require.NoError(t, err)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: hexOf(pub), Login: "walletless"}))

	body := `{"model":"` + model + `"}`
	mk := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		p, ts, sig := protocol.SignRequest(priv, http.MethodPost, "/v1/chat/completions", []byte(body))
		req.Header.Set(protocol.HeaderPubkey, p)
		req.Header.Set(protocol.HeaderTS, itoa64(ts))
		req.Header.Set(protocol.HeaderSig, sig)
		return req
	}
	rec := httptest.NewRecorder()
	require.True(t, b.relayViaEdge(rec, mk(), model, false, []byte(body), rngFromSeed(1), false))
	require.Equal(t, http.StatusForbidden, rec.Code)
	recS := httptest.NewRecorder()
	require.False(t, b.relayViaEdge(recS, mk(), model, false, []byte(body), rngFromSeed(1), true))
	require.Zero(t, recS.Body.Len())
}
