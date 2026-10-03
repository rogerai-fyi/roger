package main

// routing_capability_gating_bdd_test.go makes features/routing/capability_gating.feature
// EXECUTABLE against the REAL broker. It rides the upstream_failover / regression-pins harness
// (rpState over foState: a real relayBroker over a real store + a valkey shared store, N real
// stations each with a scripted httptest upstream and a real station loop), and adds:
//
//   - the REAL tool-call canary. A station earns, keeps or loses verified "tools" only through
//     b.probeToolCall -> applyToolVerdict -> recordToolProbe; the station's scripted upstream
//     plays the model (honours the forced tool with THIS probe's nonce, answers plain text, 429s,
//     or replays an old reply). Nothing calls recordToolProbe for a direct station by hand.
//   - the REAL register handler for every capability a station DECLARES (vision, and the
//     attempts to declare "tools"), so stripDeclaredTools and Normalize run as in production.
//   - the REAL shared-registry mirror (applyRegistry) for the re-hydrated "tools" case.
//   - a second broker instance over the same shared store for the authoritative-host rules.
//   - the REAL sealed edge fabric (hub + share node + approved Tower) with a body-recording
//     upstream for the bridge scenarios.
//
// HOW CANDIDACY IS OBSERVED. A capability requirement lives in the request BODY, so it is
// observed through the real relay, never through a seam of its own: "X is a candidate" = one
// relay over a registry reduced to {X} is served by X; "X is NOT a candidate" = that relay is
// refused with 503 no_match AND X's upstream is never touched. A relay refused for any OTHER
// reason (a 400 on a routing key the broker does not honour yet, say) proves nothing about the
// capability gate, so the NOT-candidate step FAILS on it rather than passing vacuously.
//
// Behaviour that does not exist yet (explicit roger.require, provider.require_parameters, the
// no_match message naming the capability, models[] / provider.only, the bridge evaluating a
// Tower's tools verdict, the relay_no_match_capability counter, the refusal log line) is
// asserted honestly against the real broker so the scenario FAILS for the right reason.
//
// The OpenAPI scenario is a docs check and is tagged @docs for the docs runner.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

const (
	cg1Tool      = `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`
	cg1TextPart  = `{"type":"text","text":"what is in this picture?"}`
	cg1ImagePart = `{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}`
)

// cg1Req is one consumer request as the scenario words it. Raw JSON members are kept as the
// exact bytes sent, so "byte-for-byte" assertions compare what really went on the wire.
type cg1Req struct {
	model      string
	models     []string
	stream     bool
	messages   string // raw JSON array ("" = one user text message)
	tools      string // raw JSON value ("" = key absent)
	toolChoice string // raw JSON value
	respFormat string // raw JSON value
	roger      string // the members of the roger object ("" = no roger key)
	provider   string // the members of the provider object
	headers    map[string]string
	who        string // "" funded consumer, "anon", "grant", "band"
	prompt     string
}

type cg1Tower struct {
	*rpTower
	mu   sync.Mutex
	hits int
	body []byte
}

type cg1State struct {
	*rpState

	mu          sync.Mutex
	hits        map[string]int    // NON-canary upstream hits per station name
	bodies      map[string][]byte // last NON-canary body each station's upstream received
	canaryMode  map[string]string // honor | plain | 429 | replay
	canaryReply map[string][]byte // the last honoured canary reply a station produced

	req  cg1Req
	last rpResult
	rec  *recWriter
	run  []rpResult

	// taken right before the scenario's When fires
	cgHits     map[string]int
	cgTower    int
	cgHolds    int
	cgReceipts int
	cgBal      float64
	cgTrust    map[string]trustState
	cgStrikes  map[string]int
	cgLog      int

	owners    map[string]*fstation
	cgBand    string
	cgGrant   string
	tower     *cg1Tower
	offers    []map[string]any
	market    []map[string]any
	hostNode  string
	hostModel string
	anonSeed  bool
}

func (s *cg1State) cg1Reset() error {
	if err := s.resetPins(); err != nil {
		return err
	}
	s.hits, s.bodies = map[string]int{}, map[string][]byte{}
	s.canaryMode, s.canaryReply = map[string]string{}, map[string][]byte{}
	s.req, s.last, s.rec, s.run = cg1Req{}, rpResult{}, nil, nil
	s.cgHits, s.cgTower, s.cgHolds, s.cgReceipts, s.cgBal = nil, 0, 0, 0, 0
	s.cgTrust, s.cgStrikes, s.cgLog = nil, nil, 0
	s.owners = map[string]*fstation{}
	s.cgBand, s.cgGrant, s.tower = "", "", nil
	s.offers, s.market = nil, nil
	s.hostNode, s.hostModel, s.anonSeed = "", "", false
	return nil
}

// --- Background ------------------------------------------------------------------------

// cg1Defaults pins the canary's shipped defaults. Moderation stays as the relay harness has
// it (no classifier is reachable from a test); the capability gate does not depend on its mode.
func (s *cg1State) cg1Defaults() error {
	if toolsVerifiedTTL != 45*time.Minute || toolProbeEvery != 20*time.Minute {
		return fmt.Errorf("tool-call canary defaults changed: ttl=%s every=%s", toolsVerifiedTTL, toolProbeEvery)
	}
	return nil
}

// --- stations ---------------------------------------------------------------------------

// cg1Stand brings up a REAL Tier-A station whose poll THIS instance hosts (so it is the
// authoritative host for the tools verdict) and installs the model-playing upstream.
func (s *cg1State) cg1Stand(name, model string, in, out float64, owner *fstation) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	st := s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out, owner: owner})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, probeFails: 0, ttftMs: 200}
	if s.b.localPollAt == nil {
		s.b.localPollAt = map[string]time.Time{}
	}
	s.b.localPollAt[st.id] = time.Now()
	s.b.mu.Unlock()
	st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if fn, nonce, ok := cg1CanaryOf(body); ok {
			s.mu.Lock()
			mode, replay := s.canaryMode[name], s.canaryReply[name]
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch mode {
			case "honor":
				reply := cg1ToolCallReply(fn, nonce)
				s.mu.Lock()
				s.canaryReply[name] = reply
				s.mu.Unlock()
				_, _ = w.Write(reply)
			case "429":
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(utDefaultBody(429)))
			case "replay":
				_, _ = w.Write(replay)
			default: // a model that ignores tool definitions answers in plain text
				utRealCompletion(w)
			}
			return
		}
		s.mu.Lock()
		s.hits[name]++
		s.bodies[name] = body
		s.mu.Unlock()
		utRealCompletion(w)
	})
	return st
}

// cg1CanaryOf recognises the broker's tool-call canary by its forced tool's name
// (toolCanaryFn + "_" + nonce) and returns that name and the nonce to echo.
func cg1CanaryOf(body []byte) (fn, nonce string, ok bool) {
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Tools) != 1 {
		return "", "", false
	}
	fn = req.Tools[0].Function.Name
	if !strings.HasPrefix(fn, toolCanaryFn+"_") {
		return "", "", false
	}
	return fn, strings.TrimPrefix(fn, toolCanaryFn+"_"), true
}

func cg1ToolCallReply(fn, nonce string) []byte {
	args, _ := json.Marshal(map[string]string{"token": nonce})
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil,
			"tool_calls": []map[string]any{{"id": "call_1", "type": "function",
				"function": map[string]any{"name": fn, "arguments": string(args)}}}}}},
		"usage": map[string]int{"prompt_tokens": 40, "completion_tokens": 12},
	})
	return b
}

// cg1Canary runs the REAL tool-call canary against one (node, model) on broker b, with the
// station's upstream playing `mode`. Authority is what the broker itself resolves.
func (s *cg1State) cg1Canary(b *broker, name, model, mode string) {
	s.mu.Lock()
	s.canaryMode[name] = mode
	s.mu.Unlock()
	id := s.st(name).id
	b.mu.Lock()
	node := b.nodes[id]
	b.mu.Unlock()
	b.probeToolCall(node, model, b.authoritativeFor(id, time.Now()))
}

func (s *cg1State) cg1Verified(b *broker, name, model string) bool {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.toolsVerifiedForLocked(s.st(name).id, model)
}

func (s *cg1State) cg1Earn(name, model string) error {
	s.cg1Canary(s.b, name, model, "honor")
	if !s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("fixture: the real tool-call canary did not verify (%s, %s)", name, model)
	}
	return nil
}

func (s *cg1State) cg1Lose(name, model string) error {
	s.cg1Canary(s.b, name, model, "plain")
	if s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("fixture: a definitive canary failure on the authoritative host left (%s, %s) verified", name, model)
	}
	return nil
}

// cg1Reregister re-registers a station through the REAL /nodes/register handler with its
// offers rewritten by mutate, so declared capabilities are normalised and stripped as in
// production (a declared "tools" never survives this door).
func (s *cg1State) cg1Reregister(name string, mutate func(offers []protocol.ModelOffer) []protocol.ModelOffer) error {
	st := s.st(name)
	s.b.mu.Lock()
	cur := s.b.nodes[st.id]
	s.b.mu.Unlock()
	offers := mutate(append([]protocol.ModelOffer(nil), cur.Offers...))
	reg := protocol.NodeRegistration{NodeID: st.id, PubKey: st.pubHex, BridgeToken: st.tun.token, TS: time.Now().Unix(), Offers: offers}
	reg.SignRegistration(st.priv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, st.ownerPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("fixture: re-register %s = %d %s", name, w.Code, w.Body.String())
	}
	return nil
}

func cg1WithCaps(model string, caps ...string) func([]protocol.ModelOffer) []protocol.ModelOffer {
	return func(offers []protocol.ModelOffer) []protocol.ModelOffer {
		for i := range offers {
			if offers[i].Model == model {
				offers[i].Capabilities = append([]string(nil), caps...)
			}
		}
		return offers
	}
}

func cg1AddOffer(model string, caps ...string) func([]protocol.ModelOffer) []protocol.ModelOffer {
	return func(offers []protocol.ModelOffer) []protocol.ModelOffer {
		for _, o := range offers {
			if o.Model == model {
				return cg1WithCaps(model, caps...)(offers)
			}
		}
		o := offers[0]
		o.Model, o.Capabilities = model, append([]string(nil), caps...)
		return append(offers, o)
	}
}

// --- Given ------------------------------------------------------------------------------

func (s *cg1State) plain(name, model string) error {
	s.cg1Stand(name, model, 0.10, 0.30, nil)
	return nil
}

func (s *cg1State) recordsCap(name, model, cap string) error {
	s.cg1Stand(name, model, 0.10, 0.30, nil)
	switch cap {
	case protocol.CapTools:
		return s.cg1Earn(name, model)
	case protocol.CapVision:
		return s.cg1Reregister(name, cg1WithCaps(model, protocol.CapVision))
	}
	return fmt.Errorf("fixture: no way to record capability %q", cap)
}

func (s *cg1State) recordsBoth(name, model string) error {
	if err := s.recordsCap(name, model, protocol.CapVision); err != nil {
		return err
	}
	return s.cg1Earn(name, model)
}

func (s *cg1State) earnedTools(name, model string) error {
	return s.recordsCap(name, model, protocol.CapTools)
}

func (s *cg1State) declaredVision(name, model string) error {
	return s.recordsCap(name, model, protocol.CapVision)
}

func (s *cg1State) neverProbed(name string) error {
	id := s.st(name).id
	s.b.mu.Lock()
	s.b.trust[id] = trustState{}
	s.b.mu.Unlock()
	s.b.metricsMu.Lock()
	n := 0
	for k := range s.b.toolsOK {
		if strings.HasPrefix(k, id+"\x00") {
			n++
		}
	}
	s.b.metricsMu.Unlock()
	if n != 0 {
		return fmt.Errorf("fixture: %s already carries a tool-call verdict", name)
	}
	return nil
}

func (s *cg1State) registersDeclaringTools(name, model string) error {
	s.cg1Stand(name, model, 0.10, 0.30, nil)
	return s.cg1Reregister(name, cg1WithCaps(model, protocol.CapTools))
}

func (s *cg1State) registersDeclaringToolsCasings(name, model string) error {
	s.cg1Stand(name, model, 0.10, 0.30, nil)
	return s.cg1Reregister(name, cg1WithCaps(model, "TOOLS", " tools ", "Tools"))
}

func (s *cg1State) neverPassedCanary(name string) error {
	id := s.st(name).id
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	for k, v := range s.b.toolsMerged {
		if v && strings.HasPrefix(k, id+"\x00") {
			return fmt.Errorf("fixture: %s already has a verified tools verdict", name)
		}
	}
	return nil
}

// mirroredWithTools ingests the station's registration through the REAL shared-registry mirror
// (applyRegistry) with a raw "tools" capability on its offer - the path register's strip does
// not cover.
func (s *cg1State) mirroredWithTools(name, model string) error {
	st := s.cg1Stand(name, model, 0.10, 0.30, nil)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	s.b.mu.Unlock()
	offers := append([]protocol.ModelOffer(nil), reg.Offers...)
	for i := range offers {
		offers[i].Capabilities = []string{protocol.CapTools}
	}
	reg.Offers, reg.TS, reg.BridgeToken = offers, time.Now().Unix(), st.tun.token
	raw, _ := json.Marshal(reg)
	s.b.applyRegistry(map[string][]byte{st.id: raw}, nil)
	return nil
}

func (s *cg1State) lostTools(name, model string) error {
	if err := s.recordsCap(name, model, protocol.CapTools); err != nil {
		return err
	}
	return s.cg1Lose(name, model)
}

// staleToolsBit earns the bit for real, then ages its shared field past toolsVerifiedTTL (the
// field value IS the mark time) and runs the periodic sync, as a host that stopped re-proving.
func (s *cg1State) staleToolsBit(name, model string) error {
	if err := s.recordsCap(name, model, protocol.CapTools); err != nil {
		return err
	}
	old := time.Now().Add(-toolsVerifiedTTL - time.Minute).UnixMilli()
	s.mr.HSet(toolsKey(), s.st(name).id+"\x00"+model, strconv.FormatInt(old, 10))
	s.b.syncToolsVerified()
	return nil
}

func (s *cg1State) twoModels(name, a, b string) error {
	s.cg1Stand(name, a, 0.10, 0.30, nil)
	return s.cg1Reregister(name, cg1AddOffer(b))
}

func (s *cg1State) earnedBoth(name, a, b string) error {
	if err := s.twoModels(name, a, b); err != nil {
		return err
	}
	if err := s.cg1Earn(name, a); err != nil {
		return err
	}
	return s.cg1Earn(name, b)
}

func (s *cg1State) canaryPassed(name, model string) error { return s.cg1Earn(name, model) }

func (s *cg1State) pairNeverProbed(name, model string) error {
	if s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("fixture: (%s, %s) already verified", name, model)
	}
	return nil
}

func (s *cg1State) cheaperFasterUnprobed(name, model string) error {
	st := s.cg1Stand(name, model, 0.01, 0.05, nil)
	s.setTPS(st.id, 400)
	s.setSuccess(st.id, 0.99)
	return nil
}

func (s *cg1State) visionOnOneOffer(name, a, b string) error {
	s.cg1Stand(name, a, 0.10, 0.30, nil)
	if err := s.cg1Reregister(name, cg1AddOffer(b)); err != nil {
		return err
	}
	return s.cg1Reregister(name, cg1WithCaps(a, protocol.CapVision))
}

func (s *cg1State) visionAndPassedCanary(name, model string) error { return s.recordsBoth(name, model) }

func (s *cg1State) discoverListsNoTools(name, model string) error {
	s.cg1Stand(name, model, 0.10, 0.30, nil)
	if err := s.getDiscover(); err != nil {
		return err
	}
	return s.offerListsNoTools(name)
}

func (s *cg1State) pricedPlain(name, model string, out float64) error {
	st := s.cg1Stand(name, model, out/10, out, nil)
	// The better-scored station: only a filter keeps it out.
	s.setTPS(st.id, 300)
	s.setSuccess(st.id, 0.99)
	return nil
}

func (s *cg1State) pricedTools(name, model string, out float64) error {
	s.cg1Stand(name, model, out/10, out, nil)
	return s.cg1Earn(name, model)
}

func (s *cg1State) freePlain(name, model string) error {
	s.cg1Stand(name, model, 0, 0, nil)
	return nil
}

func (s *cg1State) freeTools(name, model string) error {
	s.cg1Stand(name, model, 0, 0, nil)
	return s.cg1Earn(name, model)
}

func (s *cg1State) laterReregistersTools(name, model string) error {
	return s.registersDeclaringTools(name, model)
}

func (s *cg1State) bothDeclaredVision(a, b, model string) error {
	for _, n := range []string{a, b} {
		if err := s.declaredVision(n, model); err != nil {
			return err
		}
	}
	return nil
}

func (s *cg1State) canaryPassedWithNonce(name, model, _ string) error {
	// The nonce is minted by the broker per probe (newToolNonce); "N1" names whichever one
	// this real probe used. The reply the station produced for it is kept for the replay.
	return s.cg1Earn(name, model)
}

func (s *cg1State) fundedWallet() error { return s.ensureFunded() }

// --- Given: owners, grants, bands -------------------------------------------------------

func (s *cg1State) ownerNode(owner, name, model string, tools bool) error {
	st := s.cg1Stand(name, model, 0.10, 0.30, s.owners[owner])
	if s.owners[owner] == nil {
		s.owners[owner] = st
	}
	if tools {
		return s.cg1Earn(name, model)
	}
	return nil
}

func (s *cg1State) ownerNodePlain(owner, name, model string) error {
	return s.ownerNode(owner, name, model, false)
}
func (s *cg1State) ownerNodeTools(owner, name, model string) error {
	return s.ownerNode(owner, name, model, true)
}

func (s *cg1State) grantFrom(owner, model string) error {
	o := s.owners[owner]
	if o == nil {
		return fmt.Errorf("fixture: no owner %q", owner)
	}
	secret := "rog-grant_cg1" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.cgGrant = secret
	g := store.Grant{ID: "grant_cg1_" + s.nonce, SecretHash: hex.EncodeToString(sum[:]), Owner: o.acct,
		Label: "cg1", Free: true, Models: []string{model}, CreatedAt: time.Now().Unix()}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	return rs1GrantWalletRow(s.db, g) // see there: a free grant's wallet needs a row to settle on Postgres
}

func (s *cg1State) privateBandPlain(_, name, model string) error {
	st := s.cg1Stand(name, model, 0.10, 0.30, nil)
	s.b.mu.Lock()
	s.b.private[st.id] = true
	s.b.mu.Unlock()
	code := "147.520 MHz · CGBD-" + strings.ToUpper(s.nonce[:4])
	s.cgBand = code
	return s.db.CreateBand(store.Band{ID: "band_cg1_" + s.nonce, CodeHash: protocol.BandCodeHash(code),
		CodeDisplay: "147.520 MHz · ••••-••••", Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()})
}

// --- Given: two instances ----------------------------------------------------------------

func (s *cg1State) twoInstances() error { return nil } // instance B is stood up once the node exists

func (s *cg1State) instanceAHostsProved(name, model string) error {
	if err := s.recordsCap(name, model, protocol.CapTools); err != nil {
		return err
	}
	b2 := s.instanceB() // mirrors the stations; B hosts no poll, so it is never authoritative
	id := s.st(name).id
	s.b.mu.Lock()
	tq := s.b.trust[id]
	s.b.mu.Unlock()
	b2.mu.Lock()
	b2.trust[id] = tq
	b2.mu.Unlock()
	b2.syncToolsVerified()
	s.hostNode, s.hostModel = name, model
	return nil
}

// --- Given: the edge fabric --------------------------------------------------------------

// cg1TowerUp is rpState.standUpTower with an upstream that records what the Tower's serving
// node received, and - when verified - a tools verdict for the routable row's (node, model).
func (s *cg1State) cg1TowerUp(model string, verified bool) error {
	if s.tower != nil {
		return nil
	}
	if err := s.ensureTower(); err != nil {
		return err
	}
	t, b, srv := s.t, s.b, s.towerSrv
	name := "cg1-tower"
	tw := &cg1Tower{rpTower: &rpTower{name: name}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tw.mu.Lock()
		tw.hits++
		tw.body = body
		tw.mu.Unlock()
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong from the tower"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	tw.closers = append(tw.closers, upstream.Close)
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
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
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fixture: the Tower's share node never attached")
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return fmt.Errorf("fixture: the Tower has no routable row (%v)", err)
	}
	tw.nodeID = rows[0].NodeID
	s.towers[name] = tw.rpTower
	s.tower = tw
	if verified {
		// The verdict store is keyed (node, model); the routable row's node is what a bridge
		// filter would read. recordToolProbe is its sole writer.
		b.recordToolProbe(tw.nodeID, model, true, false, true)
	}
	return nil
}

func (s *cg1State) towerNoVerdict(model string) error { return s.cg1TowerUp(model, false) }
func (s *cg1State) towerVerified(model string) error  { return s.cg1TowerUp(model, true) }

// --- the request ------------------------------------------------------------------------

func (s *cg1State) cg1Body(q cg1Req) []byte {
	var parts []string
	if q.model != "" {
		parts = append(parts, `"model":`+strconv.Quote(q.model))
	}
	if q.models != nil {
		mb, _ := json.Marshal(q.models)
		parts = append(parts, `"models":`+string(mb))
	}
	msgs := q.messages
	if msgs == "" {
		p := q.prompt
		if p == "" {
			p = utPrompt(s.tokens)
		}
		msgs = `[{"role":"user","content":` + strconv.Quote(p) + `}]`
	}
	parts = append(parts, `"messages":`+msgs)
	if q.tools != "" {
		parts = append(parts, `"tools":`+q.tools)
	}
	if q.toolChoice != "" {
		parts = append(parts, `"tool_choice":`+q.toolChoice)
	}
	if q.respFormat != "" {
		parts = append(parts, `"response_format":`+q.respFormat)
	}
	if q.stream {
		parts = append(parts, `"stream":true`)
	}
	if q.provider != "" {
		parts = append(parts, `"provider":{`+q.provider+`}`)
	}
	if q.roger != "" {
		parts = append(parts, `"roger":{`+q.roger+`}`)
	}
	return []byte("{" + strings.Join(parts, ",") + "}")
}

// cg1Fire sends one REAL relay on broker b and returns what the consumer saw.
func (s *cg1State) cg1Fire(b *broker, q cg1Req) (rpResult, *recWriter, error) {
	body := s.cg1Body(q)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	switch q.who {
	case "anon":
		if !s.anonSeed {
			// An anonymous wallet is seeded exactly as production seeds first use (free credits),
			// so the ~$0 free-offer hold can land (the upstream_failover harness does the same);
			// it is still NOT logged in and cannot spend on a paid station. Reading the balance
			// also creates the wallet row a $0 settle needs on the Postgres store.
			s.b.seedFunds = 0.5
			pub := hex.EncodeToString(s.anonPriv.Public().(ed25519.PublicKey))
			if _, err := s.b.db.BalanceOf(protocol.UserIDFromPubkey(pub), s.b.seedFunds); err != nil {
				return rpResult{}, nil, err
			}
			s.anonSeed = true
		}
		signReq(r, s.anonPriv, body)
	case "grant":
		r.Header.Set("Authorization", "Bearer "+s.cgGrant)
	default:
		if err := s.ensureFunded(); err != nil {
			return rpResult{}, nil, err
		}
		signReq(r, s.consumerPriv, body)
		if q.who == "band" {
			r.Header.Set("X-Roger-Freq", s.cgBand)
		}
	}
	for k, v := range q.headers {
		r.Header.Set(k, v)
	}
	b.mu.Lock()
	for _, st := range s.stations {
		b.lastSeen[st.id] = time.Now()
	}
	b.mu.Unlock()
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	b.relay(w, r)
	return rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()}, w, nil
}

func (s *cg1State) cg1HitsOf(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[name]
}

func (s *cg1State) cg1TowerHits() int {
	if s.tower == nil {
		return 0
	}
	s.tower.mu.Lock()
	defer s.tower.mu.Unlock()
	return s.tower.hits
}

func (s *cg1State) cg1Snap() error {
	s.cgHits = map[string]int{}
	s.cgTrust = map[string]trustState{}
	s.cgStrikes = map[string]int{}
	for n, st := range s.stations {
		s.cgHits[n] = s.cg1HitsOf(n)
		s.b.metricsMu.Lock()
		s.cgTrust[n] = s.b.trust[st.id]
		s.b.metricsMu.Unlock()
		ks, _ := s.db.StrikesByOwner(st.acct, 0)
		s.cgStrikes[n] = len(ks)
	}
	s.cgTower = s.cg1TowerHits()
	h, _, _, _, err := s.holdRows()
	if err != nil {
		return err
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	s.cgHolds, s.cgReceipts = h, rc
	if s.funded {
		bal, berr := s.balance()
		if berr != nil {
			return berr
		}
		s.cgBal = bal
	}
	s.cgLog = len(s.logs.String())
	return nil
}

// cg1When fires the scenario's request on the full registry and keeps the outcome.
func (s *cg1State) cg1When(q cg1Req) error {
	if q.who == "" || q.who == "band" {
		if err := s.ensureFunded(); err != nil {
			return err
		}
	}
	if err := s.cg1Snap(); err != nil {
		return err
	}
	res, rec, err := s.cg1Fire(s.b, q)
	if err != nil {
		return err
	}
	s.req, s.last, s.rec = q, res, rec
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = res.code, res.body, res.hdr, rec
	return nil
}

// cg1Isolated fires q over a registry reduced to {name} on broker b.
func (s *cg1State) cg1Isolated(b *broker, name string, q cg1Req) (rpResult, int, error) {
	id := s.st(name).id
	b.mu.Lock()
	saved := b.nodes
	b.nodes = map[string]protocol.NodeRegistration{id: saved[id]}
	b.mu.Unlock()
	before := s.cg1HitsOf(name)
	res, _, err := s.cg1Fire(b, q)
	b.mu.Lock()
	b.nodes = saved
	b.mu.Unlock()
	return res, s.cg1HitsOf(name) - before, err
}

func cg1ErrOf(body []byte) (code, msg string) {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return env.Error.Code, env.Error.Message
}

func (s *cg1State) cg1Cand(b *broker, name string, q cg1Req) error {
	res, _, err := s.cg1Isolated(b, name, q)
	if err != nil {
		return err
	}
	if res.code == 200 && res.hdr.Get("X-RogerAI-Provider") == s.st(name).id {
		return nil
	}
	return fmt.Errorf("%q should be a candidate, but the relay with only it on air answered %d %s", name, res.code, bytes.TrimSpace(res.body))
}

func (s *cg1State) cg1NotCand(b *broker, name string, q cg1Req) error {
	res, hits, err := s.cg1Isolated(b, name, q)
	if err != nil {
		return err
	}
	if hits > 0 || (res.code == 200 && res.hdr.Get("X-RogerAI-Provider") == s.st(name).id) {
		return fmt.Errorf("%q must NOT be a candidate, but it served the request (%d, upstream hits %d)", name, res.code, hits)
	}
	if code, _ := cg1ErrOf(res.body); res.code != http.StatusServiceUnavailable || code != "no_match" {
		return fmt.Errorf("%q was not served, but not because of the capability gate: the relay answered %d %s (want 503 no_match)", name, res.code, bytes.TrimSpace(res.body))
	}
	return nil
}

func cg1RequireJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if json.Valid([]byte(raw)) {
		return raw
	}
	// an Examples cell that carries its own quotes to keep its padding: ["" tools ""]
	if strings.HasPrefix(raw, `[""`) && strings.HasSuffix(raw, `""]`) && len(raw) >= 6 {
		b, _ := json.Marshal([]string{raw[3 : len(raw)-3]})
		return string(b)
	}
	return raw
}

func (s *cg1State) cg1IDs(raw string) string {
	for name, st := range s.stations {
		raw = strings.ReplaceAll(raw, `"`+name+`"`, `"`+st.id+`"`)
	}
	return raw
}

var (
	cg1ReRoleImage  = regexp.MustCompile(`^a "([^"]*)" message whose content array holds an image_url part$`)
	cg1ReRespFormat = regexp.MustCompile(`^response_format type "([^"]*)" and provider\.require_parameters (true|false)$`)
	cg1ReOrderNoFB  = regexp.MustCompile(`^one function tool, provider\.order (\[.*\]) and allow_fallbacks false$`)
	cg1ReToolList   = regexp.MustCompile(`^one function tool and provider\.(only|order|ignore) (\[.*\])$`)
	cg1ReToolHeader = regexp.MustCompile(`^one function tool and header "([^:"]+): ([^"]*)"$`)
	cg1ReHeader     = regexp.MustCompile(`^header "([^:"]+): ([^"]*)" and no body require$`)
	cg1ReImageURL   = regexp.MustCompile(`^an image_url part whose url is "([^"]*)"$`)
	cg1ReTextReads  = regexp.MustCompile(`^a text part whose text reads "([^"]*)"$`)
	cg1RePartType   = regexp.MustCompile(`^a content part of type "([^"]*)"$`)
	cg1ReStringMsg  = regexp.MustCompile(`^a user message whose content is the string "([^"]*)"$`)
)

func cg1UserMsg(parts ...string) string {
	return `[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]`
}

// cg1Shape turns the words after "carries" into the request the scenario describes.
func (s *cg1State) cg1Shape(q *cg1Req, tail string) error {
	tail = strings.TrimSpace(tail)
	switch {
	case tail == "one function tool", tail == "one function tool and no roger.require":
		q.tools = cg1Tool
	case strings.HasPrefix(tail, "one function tool and roger.require "):
		q.tools = cg1Tool
		q.roger = `"require":` + cg1RequireJSON(strings.TrimPrefix(tail, "one function tool and roger.require "))
	case tail == "one function tool and provider.require_parameters false":
		q.tools, q.provider = cg1Tool, `"require_parameters":false`
	case cg1ReOrderNoFB.MatchString(tail):
		m := cg1ReOrderNoFB.FindStringSubmatch(tail)
		q.tools, q.provider = cg1Tool, `"order":`+s.cg1IDs(m[1])+`,"allow_fallbacks":false`
	case cg1ReToolList.MatchString(tail):
		m := cg1ReToolList.FindStringSubmatch(tail)
		q.tools, q.provider = cg1Tool, `"`+m[1]+`":`+s.cg1IDs(m[2])
	case cg1ReToolHeader.MatchString(tail):
		m := cg1ReToolHeader.FindStringSubmatch(tail)
		v := m[2]
		if st, ok := s.stations[v]; ok {
			v = st.id
		}
		q.tools, q.headers = cg1Tool, map[string]string{m[1]: v}
	case strings.HasPrefix(tail, "roger.require with 33 entries all reading"):
		vals := make([]string, 33)
		for i := range vals {
			vals[i] = "tools"
		}
		b, _ := json.Marshal(vals)
		q.roger = `"require":` + string(b)
	case strings.HasPrefix(tail, "roger.require "):
		rest := strings.TrimPrefix(tail, "roger.require ")
		if strings.HasSuffix(rest, " and one function tool") {
			rest, q.tools = strings.TrimSuffix(rest, " and one function tool"), cg1Tool
		}
		q.roger = `"require":` + cg1RequireJSON(rest)
	case cg1ReHeader.MatchString(tail):
		m := cg1ReHeader.FindStringSubmatch(tail)
		q.headers = map[string]string{m[1]: m[2]}
	case strings.HasPrefix(tail, "tools "):
		q.tools = strings.TrimPrefix(tail, "tools ")
	case tail == `tool_choice "auto" and no tools array`, tail == `tool_choice "auto" and no provider object`:
		q.toolChoice = `"auto"`
	case tail == `tool_choice "required", no tools array, and provider.require_parameters true`:
		q.toolChoice, q.provider = `"required"`, `"require_parameters":true`
	case cg1ReRespFormat.MatchString(tail):
		m := cg1ReRespFormat.FindStringSubmatch(tail)
		q.respFormat = `{"type":` + strconv.Quote(m[1]) + `}`
		if m[1] == "json_schema" {
			q.respFormat = `{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}`
		}
		q.provider = `"require_parameters":` + m[2]
	case strings.HasPrefix(tail, "provider.require_parameters "):
		q.provider = `"require_parameters":` + strings.TrimPrefix(tail, "provider.require_parameters ")
	case cg1ReRoleImage.MatchString(tail):
		role := cg1ReRoleImage.FindStringSubmatch(tail)[1]
		q.messages = `[{"role":` + strconv.Quote(role) + `,"content":[` + cg1TextPart + `,` + cg1ImagePart + `]}]`
	case tail == "a user message whose content array is [text, text, image_url, text]":
		q.messages = cg1UserMsg(cg1TextPart, cg1TextPart, cg1ImagePart, cg1TextPart)
	case tail == "three user messages and only the second holds an image_url part":
		q.messages = `[{"role":"user","content":"first"},{"role":"user","content":[` + cg1TextPart + `,` + cg1ImagePart + `]},{"role":"user","content":"third"}]`
	case cg1ReImageURL.MatchString(tail):
		// the scenario's "iVBOR..." is an elided payload; a real PNG header stands in for it
		q.messages = cg1UserMsg(cg1TextPart, cg1ImagePart)
	case strings.HasPrefix(tail, "a content part {"):
		q.messages = cg1UserMsg(cg1TextPart, strings.TrimPrefix(tail, "a content part "))
	case cg1ReStringMsg.MatchString(tail):
		q.messages = `[{"role":"user","content":` + strconv.Quote(cg1ReStringMsg.FindStringSubmatch(tail)[1]) + `}]`
	case tail == "a user message whose content array holds two text parts":
		q.messages = cg1UserMsg(cg1TextPart, `{"type":"text","text":"and a second part"}`)
	case cg1ReTextReads.MatchString(tail):
		q.messages = cg1UserMsg(`{"type":"text","text":` + strconv.Quote(cg1ReTextReads.FindStringSubmatch(tail)[1]) + `}`)
	case cg1RePartType.MatchString(tail):
		typ := cg1RePartType.FindStringSubmatch(tail)[1]
		q.messages = cg1UserMsg(cg1TextPart, `{"type":`+strconv.Quote(typ)+`,"input_audio":{"data":"AAAA","format":"wav"}}`)
	case tail == "an image_url part":
		q.messages = cg1UserMsg(cg1TextPart, cg1ImagePart)
	default:
		return fmt.Errorf("cg1: no request shape for %q", tail)
	}
	return nil
}

// --- When -------------------------------------------------------------------------------

func (s *cg1State) whenShaped(q cg1Req, tail string) error {
	if err := s.cg1Shape(&q, tail); err != nil {
		return err
	}
	return s.cg1When(q)
}

func (s *cg1State) requestCarries(model, tail string) error {
	return s.whenShaped(cg1Req{model: model}, tail)
}
func (s *cg1State) streamingCarries(model, tail string) error {
	return s.whenShaped(cg1Req{model: model, stream: true}, tail)
}
func (s *cg1State) modelsCarries(list, tail string) error {
	var models []string
	if err := json.Unmarshal([]byte(list), &models); err != nil {
		return fmt.Errorf("bad models list %q: %v", list, err)
	}
	return s.whenShaped(cg1Req{models: models}, tail)
}
func (s *cg1State) anonCarries(model, tail string) error {
	return s.whenShaped(cg1Req{model: model, who: "anon"}, tail)
}
func (s *cg1State) grantCarries(model, tail string) error {
	return s.whenShaped(cg1Req{model: model, who: "grant"}, tail)
}
func (s *cg1State) bandCarries(model, tail string) error {
	return s.whenShaped(cg1Req{model: model, who: "band"}, tail)
}
func (s *cg1State) consumerCarries(model, tail string) error { return s.requestCarries(model, tail) }

func cg1ToolReq(model string) cg1Req { return cg1Req{model: model, tools: cg1Tool} }

func (s *cg1State) toolRequestIsServed(model string) error { return s.cg1When(cg1ToolReq(model)) }

func (s *cg1State) toolRequestServedPlainText(model, name string) error {
	// The station's upstream answers "The answer is 4." - plain text, no tool_calls.
	if err := s.cg1When(cg1ToolReq(model)); err != nil {
		return err
	}
	return s.servedBy(name)
}

func (s *cg1State) toolRequestWithPromptRefused(model, prompt string) error {
	q := cg1ToolReq(model)
	q.prompt = prompt
	if err := s.cg1When(q); err != nil {
		return err
	}
	if s.last.code == 200 {
		return fmt.Errorf("the request was served (200), not refused")
	}
	return nil
}

func (s *cg1State) nRequests(n int, q cg1Req) error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	if err := s.cg1Snap(); err != nil {
		return err
	}
	s.run, s.req = nil, q
	for i := 0; i < n; i++ {
		res, rec, err := s.cg1Fire(s.b, q)
		if err != nil {
			return err
		}
		s.run = append(s.run, res)
		s.last, s.rec = res, rec
	}
	return nil
}

func (s *cg1State) nToolRequestsRouted(n int, model string) error {
	return s.nRequests(n, cg1ToolReq(model))
}

func (s *cg1State) nImageRequestsRouted(n int, model string) error {
	return s.nRequests(n, cg1Req{model: model, messages: cg1UserMsg(cg1TextPart, cg1ImagePart)})
}

func (s *cg1State) nToolRequestsRefused(n int, model string) error {
	if err := s.nRequests(n, cg1ToolReq(model)); err != nil {
		return err
	}
	for i, r := range s.run {
		if r.code == 200 {
			return fmt.Errorf("request %d was served (200), not refused", i+1)
		}
	}
	return nil
}

func (s *cg1State) canaryPasses(name, model string) error {
	s.cg1Canary(s.b, name, model, "honor")
	return nil
}

func (s *cg1State) canaryFailsAuthoritative(name, model string) error {
	if !s.b.authoritativeFor(s.st(name).id, time.Now()) {
		return fmt.Errorf("fixture: this instance is not the authoritative host of %s", name)
	}
	s.cg1Canary(s.b, name, model, "plain")
	return nil
}

func (s *cg1State) canaryAnswers429(name, model string) error {
	s.cg1Canary(s.b, name, model, "429")
	return nil
}

func (s *cg1State) instanceBCanaryFails(name string) error {
	if s.b2 == nil {
		return fmt.Errorf("fixture: no instance B")
	}
	if s.b2.authoritativeFor(s.st(name).id, time.Now()) {
		return fmt.Errorf("fixture: instance B must not be the authoritative host of %s", name)
	}
	s.cg1Canary(s.b2, name, s.hostModel, "plain")
	return nil
}

func (s *cg1State) instanceACanaryFailsAndSyncs() error {
	s.cg1Canary(s.b, s.hostNode, s.hostModel, "plain")
	s.b2.syncToolsVerified()
	return nil
}

func (s *cg1State) replaysCanary(name, model, _ string) error {
	s.mu.Lock()
	had := len(s.canaryReply[name]) > 0
	s.mu.Unlock()
	if !had {
		return fmt.Errorf("fixture: %s produced no canary reply to replay", name)
	}
	s.cg1Canary(s.b, name, model, "replay")
	return nil
}

func (s *cg1State) getDiscover() error {
	w := httptest.NewRecorder()
	s.b.discover(w, httptest.NewRequest(http.MethodGet, "/discover", nil))
	var out struct {
		Offers []map[string]any `json:"offers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return fmt.Errorf("GET /discover = %d %s", w.Code, w.Body.String())
	}
	s.offers = out.Offers
	return nil
}

func (s *cg1State) getMarket() error {
	w := httptest.NewRecorder()
	s.b.market(w, httptest.NewRequest(http.MethodGet, "/market", nil))
	var out struct {
		Market []map[string]any `json:"market"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return fmt.Errorf("GET /market = %d %s", w.Code, w.Body.String())
	}
	s.market = out.Market
	return nil
}

// --- Then: candidacy ---------------------------------------------------------------------

func (s *cg1State) isCandidate(name string) error    { return s.cg1Cand(s.b, name, s.req) }
func (s *cg1State) isNotCandidate(name string) error { return s.cg1NotCand(s.b, name, s.req) }

func (s *cg1State) toolFindsCand(model, name string) error {
	return s.cg1Cand(s.b, name, cg1ToolReq(model))
}
func (s *cg1State) toolFindsNotCand(model, name string) error {
	return s.cg1NotCand(s.b, name, cg1ToolReq(model))
}
func (s *cg1State) toolAndImageFindsCand(model, name string) error {
	q := cg1ToolReq(model)
	q.messages = cg1UserMsg(cg1TextPart, cg1ImagePart)
	return s.cg1Cand(s.b, name, q)
}
func (s *cg1State) onBToolFindsCand(model, name string) error {
	s.b2.syncToolsVerified()
	return s.cg1Cand(s.b2, name, cg1ToolReq(model))
}
func (s *cg1State) onBToolFindsNotCand(model, name string) error {
	s.b2.syncToolsVerified()
	return s.cg1NotCand(s.b2, name, cg1ToolReq(model))
}

func (s *cg1State) pairNotVerified(name, model string) error {
	if s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("(%s, %s) earned verified tools from a replayed canary reply", name, model)
	}
	return nil
}

// effectiveRequirement reads the effective requirement off the refusal a station with NO
// capability gets for the same request: it must name every required capability and no other.
func (s *cg1State) effectiveRequirement(list string) error {
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return err
	}
	st := s.cg1Stand("cg1-bare", s.req.model, 0.10, 0.30, nil)
	res, hits, err := s.cg1Isolated(s.b, "cg1-bare", s.req)
	if err != nil {
		return err
	}
	code, msg := cg1ErrOf(res.body)
	if res.code != 503 || code != "no_match" || hits != 0 {
		return fmt.Errorf("a station with no capability (%s) got %d %s, want 503 no_match", st.id, res.code, bytes.TrimSpace(res.body))
	}
	for _, c := range []string{protocol.CapTools, protocol.CapVision} {
		need := false
		for _, w := range want {
			need = need || w == c
		}
		if got := strings.Contains(msg, c); got != need {
			return fmt.Errorf("refusal message %q names %q = %v, want %v (effective requirement %v)", msg, c, got, need, want)
		}
	}
	return nil
}

// --- Then: the response ------------------------------------------------------------------

func (s *cg1State) servedBy(name string) error {
	if s.last.code != 200 || s.last.hdr.Get("X-RogerAI-Provider") != s.st(name).id {
		return fmt.Errorf("the request should be served by %q, got %d provider=%q: %s", name, s.last.code,
			s.last.hdr.Get("X-RogerAI-Provider"), bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *cg1State) statusIs(code int) error {
	if s.last.code != code {
		return fmt.Errorf("status = %d, want %d: %s", s.last.code, code, bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *cg1State) errorCodeIs(want string) error {
	if code, _ := cg1ErrOf(s.last.body); code != want {
		return fmt.Errorf("error code = %q, want %q: %s", code, want, bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *cg1State) statusAndCode(code int, want string) error {
	if err := s.statusIs(code); err != nil {
		return err
	}
	return s.errorCodeIs(want)
}

func (s *cg1State) errorNames(key string) error {
	if _, msg := cg1ErrOf(s.last.body); !strings.Contains(msg, key) {
		return fmt.Errorf("error message %q does not name %q", msg, key)
	}
	return nil
}

func (s *cg1State) errorNamesCapability(cap string) error {
	_, msg := cg1ErrOf(s.last.body)
	if !regexp.MustCompile(`\b` + regexp.QuoteMeta(cap) + `\b`).MatchString(msg) {
		return fmt.Errorf("error message %q does not name the capability %q", msg, cap)
	}
	return nil
}

func (s *cg1State) noStationDispatched() error {
	for n := range s.stations {
		if d := s.cg1HitsOf(n) - s.cgHits[n]; d != 0 {
			return fmt.Errorf("station %q was dispatched to (%d request(s))", n, d)
		}
	}
	if d := s.cg1TowerHits() - s.cgTower; d != 0 {
		return fmt.Errorf("the Tower was dispatched to (%d request(s))", d)
	}
	return nil
}

func (s *cg1State) noHoldPlaced() error {
	h, _, _, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if h != s.cgHolds {
		return fmt.Errorf("%d hold(s) placed, want none", h-s.cgHolds)
	}
	return nil
}

func (s *cg1State) noDispatchNoHold() error {
	if err := s.noStationDispatched(); err != nil {
		return err
	}
	return s.noHoldPlaced()
}

func (s *cg1State) neverDispatchedTo(name string) error {
	if d := s.cg1HitsOf(name) - s.cgHits[name]; d != 0 {
		return fmt.Errorf("%q was dispatched to (%d request(s))", name, d)
	}
	return nil
}

func (s *cg1State) noReceiptWritten() error {
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	if rc != s.cgReceipts {
		return fmt.Errorf("%d receipt(s) written, want none", rc-s.cgReceipts)
	}
	return nil
}

func (s *cg1State) costZeroNoReceipt() error {
	if got := s.last.hdr.Get("X-RogerAI-Cost"); got != "0" {
		return fmt.Errorf("X-RogerAI-Cost = %q, want \"0\"", got)
	}
	if got := s.last.hdr.Get("X-RogerAI-Receipt"); got != "" {
		return fmt.Errorf("the refusal carries a receipt header")
	}
	return s.noReceiptWritten()
}

func (s *cg1State) responseCarries(header, value string) error {
	if got := s.last.hdr.Get(header); got != value {
		return fmt.Errorf("%s = %q, want %q (status %d %s)", header, got, value, s.last.code, bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *cg1State) costHeaderIs(value string) error {
	return s.responseCarries("X-RogerAI-Cost", value)
}

func (s *cg1State) notStruckEarnsNothing(name string) error {
	st := s.st(name)
	ks, err := s.db.StrikesByOwner(st.acct, 0)
	if err != nil {
		return err
	}
	if len(ks) != s.cgStrikes[name] {
		return fmt.Errorf("%q took %d strike(s) for a capability refusal", name, len(ks)-s.cgStrikes[name])
	}
	earned, err := s.db.EarningsOf(st.id)
	if err != nil {
		return err
	}
	if math.Abs(earned) > 1e-12 {
		return fmt.Errorf("%q earned %v from a refused request", name, earned)
	}
	return nil
}

func (s *cg1State) trustUnchanged(name string) error {
	st := s.st(name)
	s.b.metricsMu.Lock()
	now := s.b.trust[st.id]
	s.b.metricsMu.Unlock()
	if !reflect.DeepEqual(now, s.cgTrust[name]) {
		return fmt.Errorf("%q trust changed across a capability refusal: %+v -> %+v", name, s.cgTrust[name], now)
	}
	ks, err := s.db.StrikesByOwner(st.acct, 0)
	if err != nil {
		return err
	}
	if len(ks) != s.cgStrikes[name] {
		return fmt.Errorf("%q took a strike for a capability refusal", name)
	}
	return nil
}

func (s *cg1State) headerNotAnError() error { return s.statusIs(200) }

func (s *cg1State) cg1ServedBody() ([]byte, error) {
	id := s.last.hdr.Get("X-RogerAI-Provider")
	for n, st := range s.stations {
		if st.id == id {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.bodies[n], nil
		}
	}
	return nil, fmt.Errorf("no station served the request (%d %s)", s.last.code, bytes.TrimSpace(s.last.body))
}

func (s *cg1State) cg1BodyKey(body []byte, key string) (json.RawMessage, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false, fmt.Errorf("the station's body is not a JSON object: %s", body)
	}
	raw, ok := m[key]
	return raw, ok, nil
}

func (s *cg1State) stationKeyIntact(key, sent string) error {
	body, err := s.cg1ServedBody()
	if err != nil {
		return err
	}
	raw, ok, err := s.cg1BodyKey(body, key)
	if err != nil {
		return err
	}
	if !ok || string(raw) != sent {
		return fmt.Errorf("the station received %s=%s, want %s", key, raw, sent)
	}
	return nil
}

func (s *cg1State) stationToolChoiceIntact(v string) error {
	return s.stationKeyIntact("tool_choice", strconv.Quote(v))
}
func (s *cg1State) stationRespFormatIntact() error {
	return s.stationKeyIntact("response_format", s.req.respFormat)
}
func (s *cg1State) stationToolsByteForByte() error { return s.stationKeyIntact("tools", s.req.tools) }

func (s *cg1State) stationNoKey(key string) error {
	body, err := s.cg1ServedBody()
	if err != nil {
		return err
	}
	if _, ok, kerr := s.cg1BodyKey(body, key); kerr != nil {
		return kerr
	} else if ok {
		return fmt.Errorf("the station received a %q key: %s", key, body)
	}
	return nil
}

func (s *cg1State) status503BeforeSSE() error {
	if err := s.statusIs(503); err != nil {
		return err
	}
	s.rec.mu.Lock()
	statuses := append([]int(nil), s.rec.statuses...)
	s.rec.mu.Unlock()
	if len(statuses) != 1 || statuses[0] != 503 {
		return fmt.Errorf("statuses written = %v, want a single 503", statuses)
	}
	if ct := s.last.hdr.Get("Content-Type"); strings.Contains(ct, "event-stream") {
		return fmt.Errorf("an SSE header was committed (Content-Type %q)", ct)
	}
	return nil
}

func (s *cg1State) marketListsCap(model, cap string) error {
	for _, m := range s.market {
		if m["model"] == model {
			caps, _ := m["capabilities"].([]any)
			for _, c := range caps {
				if c == cap {
					return nil
				}
			}
			return fmt.Errorf("/market lists %q with capabilities %v, want %q among them", model, caps, cap)
		}
	}
	return fmt.Errorf("/market does not list %q", model)
}

func (s *cg1State) cg1OfferCaps(name, model string) ([]string, error) {
	id := s.st(name).id
	for _, o := range s.offers {
		if o["node_id"] == id && (model == "" || o["model"] == model) {
			var caps []string
			raw, _ := o["capabilities"].([]any)
			for _, c := range raw {
				caps = append(caps, fmt.Sprint(c))
			}
			sort.Strings(caps)
			return caps, nil
		}
	}
	return nil, fmt.Errorf("/discover lists no offer for %q %s", name, model)
}

func (s *cg1State) offerListsCaps(name, model, list string) error {
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return err
	}
	sort.Strings(want)
	got, err := s.cg1OfferCaps(name, model)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("/discover lists capabilities %v for %q, want %v", got, name, want)
	}
	return nil
}

func (s *cg1State) offerListsNoTools(name string) error {
	got, err := s.cg1OfferCaps(name, "")
	if err != nil {
		return err
	}
	for _, c := range got {
		if c == protocol.CapTools {
			return fmt.Errorf("/discover lists \"tools\" for %q: %v", name, got)
		}
	}
	return nil
}

// --- Then: batches -----------------------------------------------------------------------

func (s *cg1State) everyServedBy(name string) error {
	id := s.st(name).id
	if len(s.run) == 0 {
		return fmt.Errorf("no request was routed")
	}
	for i, r := range s.run {
		if r.code != 200 || r.hdr.Get("X-RogerAI-Provider") != id {
			return fmt.Errorf("request %d: %d provider=%q, want served by %q: %s", i+1, r.code,
				r.hdr.Get("X-RogerAI-Provider"), name, bytes.TrimSpace(r.body))
		}
	}
	return nil
}

func (s *cg1State) everyServedDirectly(name string) error {
	if err := s.everyServedBy(name); err != nil {
		return err
	}
	for i, r := range s.run {
		if relay := r.hdr.Get("X-RogerAI-Relay"); relay != "" {
			return fmt.Errorf("request %d rode Tower %q", i+1, relay)
		}
	}
	return nil
}

func (s *cg1State) bridgeNeverEntered() error {
	if d := s.cg1TowerHits() - s.cgTower; d != 0 {
		return fmt.Errorf("the Tower's serving node received %d request(s)", d)
	}
	return nil
}

func (s *cg1State) bothReceiveTraffic() error {
	seen := map[string]int{}
	for i, r := range s.run {
		if r.code != 200 {
			return fmt.Errorf("request %d = %d %s", i+1, r.code, bytes.TrimSpace(r.body))
		}
		seen[r.hdr.Get("X-RogerAI-Provider")]++
	}
	if len(seen) < 2 {
		return fmt.Errorf("only one station received traffic across %d requests: %v", len(s.run), seen)
	}
	return nil
}

// --- Then: money ---------------------------------------------------------------------------

func (s *cg1State) holdSizedOn(name string) error {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold}, 5000)
	if err != nil {
		return err
	}
	if n := len(rows) - s.cgHolds; n != 1 {
		return fmt.Errorf("%d hold(s) placed, want exactly one", n)
	}
	newest := rows[0]
	for _, r := range rows[1:] {
		if r.ID > newest.ID {
			newest = r
		}
	}
	s.b.mu.Lock()
	offer := s.b.nodes[s.st(name).id].Offers[0]
	s.b.mu.Unlock()
	want := holdCostFor(pricingPlan{}, offer, s.cg1Body(s.req), time.Now())
	if math.Abs(-newest.Amount-want) > 1e-9 {
		return fmt.Errorf("hold = %v, want %v (the ceiling of %q alone)", -newest.Amount, want, name)
	}
	return nil
}

// neverInPlan: a station that is not in the plan is never attempted and never priced into the
// hold. The plan itself has no wire readout; its two consequences are what is asserted.
func (s *cg1State) neverInPlan(name string) error { return s.neverDispatchedTo(name) }

func (s *cg1State) balanceUnchanged() error {
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if math.Abs(bal-s.cgBal) > 1e-12 {
		return fmt.Errorf("balance %v -> %v across a capability refusal", s.cgBal, bal)
	}
	return nil
}

func (s *cg1State) relayCompletesAsToday() error {
	if s.last.code != 200 {
		return fmt.Errorf("relay = %d %s", s.last.code, bytes.TrimSpace(s.last.body))
	}
	if s.last.hdr.Get("X-RogerAI-Receipt") == "" {
		return fmt.Errorf("the served relay carries no receipt")
	}
	if !bytes.Contains(s.last.body, []byte("The answer is 4.")) {
		return fmt.Errorf("the plain-text answer did not reach the consumer: %s", s.last.body)
	}
	return nil
}

// nextCanaryDecides: the gate itself leaves the verdict alone after a plain-text answer; only
// the next (failing) canary on the authoritative host removes it.
func (s *cg1State) nextCanaryDecides(name string) error {
	model := s.req.model
	if !s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("%q lost verified tools from a plain-text answer alone (the canary, not the relay, decides)", name)
	}
	s.cg1Canary(s.b, name, model, "plain")
	if s.cg1Verified(s.b, name, model) {
		return fmt.Errorf("a definitive canary failure left %q verified", name)
	}
	return nil
}

// --- Then: the bridge ---------------------------------------------------------------------

func (s *cg1State) servedThroughBridge() error {
	if s.last.code != 200 || s.last.hdr.Get("X-RogerAI-Relay") == "" {
		return fmt.Errorf("the request should ride the bridge, got %d relay=%q: %s", s.last.code,
			s.last.hdr.Get("X-RogerAI-Relay"), bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *cg1State) relayNamesTower() error {
	if got := s.last.hdr.Get("X-RogerAI-Relay"); s.tower == nil || got != s.tower.id {
		return fmt.Errorf("X-RogerAI-Relay = %q, want the Tower's id", got)
	}
	return nil
}

func (s *cg1State) cg1HubBody() ([]byte, error) {
	if s.tower == nil {
		return nil, fmt.Errorf("fixture: no Tower")
	}
	s.tower.mu.Lock()
	defer s.tower.mu.Unlock()
	if s.tower.hits-s.cgTower == 0 || s.tower.body == nil {
		return nil, fmt.Errorf("the Tower's serving node received nothing (relay %d %s)", s.last.code, bytes.TrimSpace(s.last.body))
	}
	return s.tower.body, nil
}

func (s *cg1State) hubToolsIntact() error {
	body, err := s.cg1HubBody()
	if err != nil {
		return err
	}
	raw, ok, err := s.cg1BodyKey(body, "tools")
	if err != nil {
		return err
	}
	if !ok || string(raw) != s.req.tools {
		return fmt.Errorf("the Tower's serving node received tools=%s, want %s", raw, s.req.tools)
	}
	return nil
}

func (s *cg1State) hubNoCarriers() error {
	body, err := s.cg1HubBody()
	if err != nil {
		return err
	}
	for _, k := range []string{"roger", "provider"} {
		if _, ok, kerr := s.cg1BodyKey(body, k); kerr != nil {
			return kerr
		} else if ok {
			return fmt.Errorf("the Tower's serving node received a %q key: %s", k, body)
		}
	}
	return nil
}

// --- Then: bands, anonymous ---------------------------------------------------------------

// bandNotRevealed: the refusal for a real band whose station lacks the capability is
// indistinguishable from the refusal for a code that resolves to no band at all.
func (s *cg1State) bandNotRevealed() error {
	saved := s.cgBand
	s.cgBand = "147.520 MHz · ZZZZ-0000"
	res, _, err := s.cg1Fire(s.b, s.req)
	s.cgBand = saved
	if err != nil {
		return err
	}
	if res.code != s.last.code || !bytes.Equal(bytes.TrimSpace(res.body), bytes.TrimSpace(s.last.body)) {
		return fmt.Errorf("a real band answered %d %s but an unknown code answered %d %s", s.last.code,
			bytes.TrimSpace(s.last.body), res.code, bytes.TrimSpace(res.body))
	}
	return nil
}

func (s *cg1State) saysLogIn() error {
	if _, msg := cg1ErrOf(s.last.body); !strings.Contains(strings.ToLower(msg), "log in") {
		return fmt.Errorf("error message %q does not say to log in to spend", msg)
	}
	return nil
}

// --- Then: telemetry and logs --------------------------------------------------------------

func cg1Leaf(v any, key string) (float64, bool) {
	switch t := v.(type) {
	case map[string]any:
		if f, ok := t[key].(float64); ok {
			return f, true
		}
		for _, c := range t {
			if f, ok := cg1Leaf(c, key); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func (s *cg1State) adminReads(key string, want int) error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	got, ok := cg1Leaf(s.adminResp, key)
	if !ok {
		return fmt.Errorf("/admin/live carries no %s counter", key)
	}
	if int(got) != want {
		return fmt.Errorf("/admin/live %s = %v, want %d", key, got, want)
	}
	return nil
}

func (s *cg1State) cg1LogSince() []string {
	all := s.logs.String()
	if s.cgLog > len(all) {
		s.cgLog = 0
	}
	return strings.Split(all[s.cgLog:], "\n")
}

func (s *cg1State) oneLogLineNames(model, cap string) error {
	n := 0
	for _, line := range s.cg1LogSince() {
		if strings.Contains(line, model) && regexp.MustCompile(`\b`+regexp.QuoteMeta(cap)+`\b`).MatchString(line) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d log line(s) name %q and %q for the refusal, want exactly one", n, model, cap)
	}
	return nil
}

func (s *cg1State) noLogLineContains(text string) error {
	if strings.Contains(s.logs.String(), text) {
		return fmt.Errorf("a log line contains %q", text)
	}
	return nil
}

// --- the suite -----------------------------------------------------------------------------

func TestRoutingCapabilityGatingBDD(t *testing.T) {
	st := &cg1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.cg1Reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardownPins()
				return ctx, nil
			})
			// Background
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)
			sc.Step(`^the tool-call canary and moderation are enabled with their defaults$`, st.cg1Defaults)

			// Given: stations
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and the broker records capability "([^"]*)" for it$`, st.recordsCap)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and the broker records capabilities "tools" and "vision" for it$`, st.recordsBoth)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and was seen just now$`, st.plain)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no recorded capability$`, st.plain)
			sc.Step(`^node "([^"]*)" is the ONLY station on air for "([^"]*)" and has no recorded capability$`, st.plain)
			sc.Step(`^node "([^"]*)" is the only station for "([^"]*)" and has no recorded capability$`, st.plain)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and declared capability "vision" at registration$`, st.declaredVision)
			sc.Step(`^"([^"]*)" has never been probed$`, st.neverProbed)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and declared no capabilities$`, st.plain)
			sc.Step(`^node "([^"]*)" registers for "([^"]*)" declaring capability "tools"$`, st.registersDeclaringTools)
			sc.Step(`^node "([^"]*)" registers for "([^"]*)" declaring capabilities \["TOOLS", " tools ", "Tools"\]$`, st.registersDeclaringToolsCasings)
			sc.Step(`^"([^"]*)" has never passed a tool-call canary$`, st.neverPassedCanary)
			sc.Step(`^node "([^"]*)" for "([^"]*)" reached the registry through the shared-registry mirror with a stored "tools" capability$`, st.mirroredWithTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and has never been tool-call probed$`, st.plain)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and previously earned verified "tools"$`, st.earnedTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and earned verified "tools"$`, st.earnedTools)
			sc.Step(`^public node "([^"]*)" is on air for "([^"]*)" and earned verified "tools"$`, st.earnedTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", cheaper, faster, and never tool-call probed$`, st.cheaperFasterUnprobed)
			sc.Step(`^node "([^"]*)" earned verified "tools" for "([^"]*)" longer ago than toolsVerifiedTTL with no re-verification$`, st.staleToolsBit)
			sc.Step(`^node "([^"]*)" lost verified "tools" for "([^"]*)" to a definitive failure$`, st.lostTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and "([^"]*)"$`, st.twoModels)
			sc.Step(`^node "([^"]*)" earned verified "tools" for both "([^"]*)" and "([^"]*)"$`, st.earnedBoth)
			sc.Step(`^the tool-call canary for \("([^"]*)", "([^"]*)"\) passed$`, st.canaryPassed)
			sc.Step(`^the tool-call canary for \("([^"]*)", "([^"]*)"\) passed with nonce "([^"]*)"$`, st.canaryPassedWithNonce)
			sc.Step(`^\("([^"]*)", "([^"]*)"\) has never been tool-call probed$`, st.pairNeverProbed)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" declaring "vision" and for "([^"]*)" declaring nothing$`, st.visionOnOneOffer)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" declaring "vision" and with a passed tool-call canary$`, st.visionAndPassedCanary)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and /discover lists no "tools" capability for it$`, st.discoverListsNoTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price ([0-9.]+) with no recorded capability$`, st.pricedPlain)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price ([0-9.]+) and earned verified "tools"$`, st.pricedTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at 0/0 with no recorded capability$`, st.freePlain)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at 0/0 and earned verified "tools"$`, st.freeTools)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and later re-registers declaring "tools"$`, st.laterReregistersTools)
			sc.Step(`^node "([^"]*)" and node "([^"]*)" are on air for "([^"]*)" and both declared "vision"$`, st.bothDeclaredVision)
			sc.Step(`^a consumer with a funded wallet$`, st.fundedWallet)
			// Given: owners, grants, bands, instances, the edge
			sc.Step(`^owner "([^"]*)" has node "([^"]*)" for "([^"]*)" with no recorded capability$`, st.ownerNodePlain)
			sc.Step(`^owner "([^"]*)" has node "([^"]*)" for "([^"]*)" that earned verified "tools"$`, st.ownerNodeTools)
			sc.Step(`^a grant from "([^"]*)" allows model "([^"]*)"$`, st.grantFrom)
			sc.Step(`^a private band on frequency "([^"]*)" whose only station "([^"]*)" serves "([^"]*)" with no recorded capability$`, st.privateBandPlain)
			sc.Step(`^the broker runs two instances behind the shared store$`, st.twoInstances)
			sc.Step(`^instance A hosts the poll of node "([^"]*)" and proved verified "tools" for "([^"]*)"$`, st.instanceAHostsProved)
			sc.Step(`^a Tower relay also hosts "([^"]*)" with no tools verdict$`, st.towerNoVerdict)
			sc.Step(`^a Tower relay hosts "([^"]*)" with no tools verdict$`, st.towerNoVerdict)
			sc.Step(`^a Tower relay hosts "([^"]*)" with a verified tools verdict$`, st.towerVerified)
			sc.Step(`^a Tower relay hosts "([^"]*)" with a verified tools verdict and is the only capable server$`, st.towerVerified)

			// When: requests
			sc.Step(`^a request for "([^"]*)" carries (.+)$`, st.requestCarries)
			sc.Step(`^a streaming request for "([^"]*)" carries (.+)$`, st.streamingCarries)
			sc.Step(`^a request with models (\[[^\]]*\]) carries (.+)$`, st.modelsCarries)
			sc.Step(`^an anonymous request for "([^"]*)" carries (.+)$`, st.anonCarries)
			sc.Step(`^a grant-keyed request for "([^"]*)" carries (.+)$`, st.grantCarries)
			sc.Step(`^a request for "([^"]*)" with the band code carries (.+)$`, st.bandCarries)
			sc.Step(`^the consumer's request for "([^"]*)" carries (.+)$`, st.consumerCarries)
			sc.Step(`^a request for "([^"]*)" carrying one function tool is served$`, st.toolRequestIsServed)
			sc.Step(`^a request for "([^"]*)" carrying one function tool is served by "([^"]*)" with a plain-text answer$`, st.toolRequestServedPlainText)
			sc.Step(`^a request for "([^"]*)" carrying one function tool and the prompt "([^"]*)" is refused$`, st.toolRequestWithPromptRefused)
			sc.Step(`^(\d+) requests for "([^"]*)" each carrying one function tool are routed$`, st.nToolRequestsRouted)
			sc.Step(`^(\d+) requests for "([^"]*)" each carrying an image_url part are routed$`, st.nImageRequestsRouted)
			sc.Step(`^(\d+) requests for "([^"]*)" each carrying one function tool are refused$`, st.nToolRequestsRefused)
			// When: the canary, the catalog
			sc.Step(`^the tool-call canary for \("([^"]*)", "([^"]*)"\) passes$`, st.canaryPasses)
			sc.Step(`^the tool-call canary for \("([^"]*)", "([^"]*)"\) fails definitively on the authoritative host$`, st.canaryFailsAuthoritative)
			sc.Step(`^the tool-call canary for \("([^"]*)", "([^"]*)"\) answers 429$`, st.canaryAnswers429)
			sc.Step(`^instance B's own canary against "([^"]*)" fails$`, st.instanceBCanaryFails)
			sc.Step(`^instance A's canary fails definitively and the shared verdict syncs$`, st.instanceACanaryFailsAndSyncs)
			sc.Step(`^the station answers the canary for \("([^"]*)", "([^"]*)"\) with the tool_calls it produced for nonce "([^"]*)"$`, st.replaysCanary)
			sc.Step(`^a consumer GETs /discover$`, st.getDiscover)
			sc.Step(`^a consumer GETs /market$`, st.getMarket)

			// Then: candidacy
			sc.Step(`^"([^"]*)" is a candidate$`, st.isCandidate)
			sc.Step(`^"([^"]*)" is NOT a candidate$`, st.isNotCandidate)
			sc.Step(`^a request for "([^"]*)" carrying one function tool (?:still )?finds "([^"]*)" a candidate$`, st.toolFindsCand)
			sc.Step(`^a request for "([^"]*)" carrying one function tool finds "([^"]*)" NOT a candidate$`, st.toolFindsNotCand)
			sc.Step(`^a request for "([^"]*)" carrying one function tool and an image_url part finds "([^"]*)" a candidate$`, st.toolAndImageFindsCand)
			sc.Step(`^on instance B a request for "([^"]*)" carrying one function tool still finds "([^"]*)" a candidate$`, st.onBToolFindsCand)
			sc.Step(`^on instance B a request for "([^"]*)" carrying one function tool finds "([^"]*)" NOT a candidate$`, st.onBToolFindsNotCand)
			sc.Step(`^\("([^"]*)", "([^"]*)"\) does not earn verified "tools"$`, st.pairNotVerified)
			sc.Step(`^the effective requirement is exactly (\[.*\])$`, st.effectiveRequirement)
			// Then: the response
			sc.Step(`^the request is served by "([^"]*)"$`, st.servedBy)
			sc.Step(`^the status is (\d+)$`, st.statusIs)
			sc.Step(`^the status is (\d+) with error code "([^"]*)"$`, st.statusAndCode)
			sc.Step(`^the status is 503 before any SSE header is committed$`, st.status503BeforeSSE)
			sc.Step(`^the error code is "([^"]*)"$`, st.errorCodeIs)
			sc.Step(`^the error message names "([^"]*)"$`, st.errorNames)
			sc.Step(`^the error message names the capability "([^"]*)"$`, st.errorNamesCapability)
			sc.Step(`^no station is dispatched to and no hold is placed$`, st.noDispatchNoHold)
			sc.Step(`^no station (?:is|was) dispatched to$`, st.noStationDispatched)
			sc.Step(`^"([^"]*)" was never dispatched to$`, st.neverDispatchedTo)
			sc.Step(`^no attempt was made against "([^"]*)"$`, st.neverDispatchedTo)
			sc.Step(`^no hold was placed$`, st.noHoldPlaced)
			sc.Step(`^the response carries "X-RogerAI-Cost: 0" and no receipt$`, st.costZeroNoReceipt)
			sc.Step(`^the response carries "([A-Za-z-]+): ([^"]*)"$`, st.responseCarries)
			sc.Step(`^X-RogerAI-Cost is "([^"]*)"$`, st.costHeaderIs)
			sc.Step(`^"([^"]*)" is not struck and earns nothing$`, st.notStruckEarnsNothing)
			sc.Step(`^"([^"]*)" trust and probe counters are unchanged$`, st.trustUnchanged)
			sc.Step(`^the unknown header is not an error$`, st.headerNotAnError)
			sc.Step(`^the station receives the body with tool_choice "([^"]*)" intact$`, st.stationToolChoiceIntact)
			sc.Step(`^the station receives response_format intact$`, st.stationRespFormatIntact)
			sc.Step(`^the station receives no "([^"]*)" key$`, st.stationNoKey)
			sc.Step(`^the station receives the tools array byte-for-byte as sent$`, st.stationToolsByteForByte)
			sc.Step(`^"([^"]*)" lists capability "([^"]*)" on /market$`, st.marketListsCap)
			sc.Step(`^the "([^"]*)" offer for "([^"]*)" lists capabilities (\[.*\])$`, st.offerListsCaps)
			sc.Step(`^the "([^"]*)" offer lists no "tools" capability$`, st.offerListsNoTools)
			// Then: batches
			sc.Step(`^every one of them is served by "([^"]*)"$`, st.everyServedBy)
			sc.Step(`^every one of them is served by "([^"]*)" directly$`, st.everyServedDirectly)
			sc.Step(`^the bridge was never entered$`, st.bridgeNeverEntered)
			sc.Step(`^both stations receive traffic under the normal scoring$`, st.bothReceiveTraffic)
			// Then: money
			sc.Step(`^the hold was sized on "([^"]*)" only$`, st.holdSizedOn)
			sc.Step(`^"([^"]*)" never appears in the plan$`, st.neverInPlan)
			sc.Step(`^the consumer's balance is unchanged$`, st.balanceUnchanged)
			sc.Step(`^no receipt exists for the request$`, st.noReceiptWritten)
			sc.Step(`^the relay completes as today$`, st.relayCompletesAsToday)
			sc.Step(`^the next tool-call canary decides whether "([^"]*)" keeps verified "tools"$`, st.nextCanaryDecides)
			// Then: the bridge
			sc.Step(`^the request is served through the bridge$`, st.servedThroughBridge)
			sc.Step(`^the response carries "X-RogerAI-Relay" naming the Tower$`, st.relayNamesTower)
			sc.Step(`^the Tower's hub receives the body with the tools array intact$`, st.hubToolsIntact)
			sc.Step(`^the Tower's hub receives no "roger" and no "provider" key$`, st.hubNoCarriers)
			// Then: bands, anonymous
			sc.Step(`^the error message does not reveal whether the band exists$`, st.bandNotRevealed)
			sc.Step(`^the error message says to log in to spend$`, st.saysLogIn)
			// Then: telemetry and logs
			sc.Step(`^/admin/live reads ([a-z_]+) (\d+)$`, st.adminReads)
			sc.Step(`^exactly one log line names "([^"]*)" and "([^"]*)"$`, st.oneLogLineNames)
			sc.Step(`^no log line contains "([^"]*)"$`, st.noLogLineContains)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/routing/capability_gating.feature"},
			Tags:  "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@part-b && ~@part-c && ~@slice2 && ~@slice3 && ~@slice4",
		},
	}
	if suite.Run() != 0 {
		t.Fatal("capability_gating.feature failed")
	}
}
