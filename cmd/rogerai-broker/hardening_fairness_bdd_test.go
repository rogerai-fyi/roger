package main

// hardening_fairness_bdd_test.go makes features/routing/fairness_and_abuse.feature EXECUTABLE
// (slice 6 hardening, contract §14.A) and holds the harness the money and integrity runners
// share (fa6State). It drives the REAL broker: relayBroker over the real store (Postgres when
// ROGERAI_TEST_DATABASE_URL is set, else the in-memory reference), the miniredis shared store,
// real stations whose loops post real signed receipts (upstream_failover_bdd_test.go), the real
// strike / cooling / rate-limit machinery, and the real sealed Tower fabric
// (routing_regression_pins_bdd_test.go) for the coin scenarios. No mocks: only the local model
// server behind each station is a scripted httptest upstream.
//
// OBSERVATION POINTS (stated once, used throughout):
//   - Consumers "alice".."dave" are distinct GitHub-bound accounts with their own wallets and
//     their own client IP (CF-Connecting-IP); "one client IP" scenarios share one address.
//   - "X is (NOT) a candidate for P" fires ONE relay as payer P with provider.only [X] and
//     reads whether X's upstream received it. A pair cooldown or a station cooldown both show
//     as "not reached"; the station-wide state alone is read through pickFor (no payer).
//   - "received nothing" / "attempts" count each station's upstream hits since the step that
//     fired the relay (fa6Mark), and the global hit order is recorded by every scripted
//     upstream for order assertions.
//   - New §14 knobs are set as environment variables before the When (os.Setenv, restored at
//     teardown); GREEN reads them at request time or when it builds its limiter.
//   - Shares are counted over real relays (X-RogerAI-Provider / X-RogerAI-Relay), never over
//     pickFor draws, so the coin, the band and home-first are observed as a consumer sees them.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// fa6Who is one consumer identity: a signed keypair, the wallet the broker bills, and the
// client IP its requests arrive from.
type fa6Who struct {
	name    string
	priv    ed25519.PrivateKey
	wallet  string
	ip      string
	unbound bool
	grant   string // bearer token when the identity is a grant holder
}

type fa6Resp struct {
	who    string
	code   int
	body   []byte
	hdr    http.Header
	stream bool
}

// fa6Spec is one relay's shaping.
type fa6Spec struct {
	who    string
	model  string
	extra  map[string]any
	hdr    map[string]string
	stream bool
	prompt int
	ip     string
	priv   ed25519.PrivateKey
	b      *broker
	grant  string
	raw    []byte // a hand-built body (overrides model/extra/prompt)
}

type fa6State struct {
	*rpState

	who                      map[string]*fa6Who
	ops                      map[string]string // owner alias ("op1") -> station name
	resps                    []fa6Resp
	last                     fa6Resp
	mark                     map[string]int
	hits                     []string // upstream hit order across stations (scripted upstreams record it)
	hitMu                    sync.Mutex
	envSet                   []string
	succSnap                 map[string]float64
	trustSnp                 map[string]trustState
	coolSeen                 map[string]bool // a station observed station-wide cooling during a When
	scen                     map[string]any  // scenario-local scratch
	ipN                      int
	unbound                  int
	recPrompt, recCompletion int
	sidecar6                 *httptest.Server
	lastModel                string
	towerPool                []*fa6Who // funded edge buyers (a bridged attempt holds ~$2 until settled)
	mailsMark                int
	bodies                   map[string][][]byte // every body each scripted upstream received, in order
	defPrompt                int                 // the prompt size (tokens) a relay sends when its spec names none (0 = 50)
}

// --- lifecycle -------------------------------------------------------------------------------

func (s *fa6State) reset() error {
	if err := s.rpState.resetPins(); err != nil {
		return err
	}
	s.who, s.ops = map[string]*fa6Who{}, map[string]string{}
	s.resps, s.last, s.mark, s.hits = nil, fa6Resp{}, map[string]int{}, nil
	s.succSnap, s.trustSnp, s.coolSeen, s.scen = map[string]float64{}, map[string]trustState{}, map[string]bool{}, map[string]any{}
	s.ipN, s.unbound, s.recPrompt, s.recCompletion = 0, 0, 0, 0
	s.lastModel, s.towerPool, s.mailsMark = "qwen3-32b", nil, 0
	s.bodies = map[string][][]byte{}
	s.defPrompt = 0
	s.model = "qwen3-32b"
	s.b.lockWin = 24 * time.Hour
	// A recount sidecar the scenarios can steer: the prompt and completion counts are
	// configurable; 0 keeps the harness default (1,000,000 = "never lower than the claim").
	s.sidecar6 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := s.recPrompt
		if strings.Contains(in.Text, fa6CompletionMarker) {
			n = s.recCompletion
		}
		if n == 0 {
			n = 1_000_000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": n, "exact": true})
	}))
	s.b.recount = recountConfig{url: s.sidecar6.URL, tolerance: 0.02, strikeTolerance: 0.25, client: &http.Client{Timeout: 4 * time.Second}}
	return nil
}

func (s *fa6State) teardown() {
	for _, k := range s.envSet {
		_ = os.Unsetenv(k)
	}
	s.envSet = nil
	if s.sidecar6 != nil {
		s.sidecar6.Close()
		s.sidecar6 = nil
	}
	s.rpState.teardownPins()
}

func (s *fa6State) setenv(k, v string) {
	_ = os.Setenv(k, v)
	s.envSet = append(s.envSet, k)
}

const fa6CompletionMarker = "fa6-completion"

// --- identities --------------------------------------------------------------------------------

func (s *fa6State) nextIP() string {
	s.ipN++
	return fmt.Sprintf("198.51.100.%d", s.ipN)
}

// consumer returns (creating) a GitHub-bound, logged-in consumer with its own wallet + IP.
func (s *fa6State) consumer(name string) *fa6Who {
	if w, ok := s.who[name]; ok {
		return w
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	id := gid.Int64() + 1000
	if err := s.db.BindOwner(store.Owner{GitHubID: id, Login: name + "-" + s.nonce, Pubkey: pub, Email: name + "@example.com"}); err != nil {
		s.t.Fatalf("bind consumer %s: %v", name, err)
	}
	w := &fa6Who{name: name, priv: priv, wallet: "u_gh_" + strconv.FormatInt(id, 10), ip: s.nextIP()}
	s.who[name] = w
	return w
}

func (s *fa6State) fundWho(name string, amount float64) error {
	w := s.consumer(name)
	_, err := s.db.AddCredits(w.wallet, amount)
	return err
}

// unboundKey mints a fresh keypair bound to no account (a "new keypair" caller). Its wallet is
// the pubkey-derived id; it gets a row the way production gives one (BalanceOf seeds it).
func (s *fa6State) unboundKey(ip string) *fa6Who {
	s.unbound++
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	w := &fa6Who{name: fmt.Sprintf("unbound-%d", s.unbound), priv: priv, wallet: protocol.UserIDFromPubkey(pub), ip: ip, unbound: true}
	_, _ = s.db.BalanceOf(w.wallet, s.b.seedFunds)
	s.who[w.name] = w
	return w
}

func (s *fa6State) balanceOf(name string) float64 {
	w := s.consumer(name)
	bal, _ := s.db.PeekBalance(w.wallet)
	return bal
}

// --- stations ----------------------------------------------------------------------------------

// node stands up a real Tier-A station `name` for `model` (default prices in $0.10 out $0.30).
func (s *fa6State) node(name, model string, in, out float64) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	st := s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out, ctx: 131072})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.recordHits(st)
	return st
}

func (s *fa6State) defaultNode(name, model string) *fstation { return s.node(name, model, 0.10, 0.30) }

// ensureNode returns the named station, standing it up for the scenario model when a Given
// names it before any "on air" step.
func (s *fa6State) ensureNode(name string) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	return s.defaultNode(name, s.lastModel)
}

// recordHits makes the station's upstream record the global hit order, wrapping whatever
// script is installed later (scriptOn always goes through record).
func (s *fa6State) recordHits(st *fstation) { s.real(st) }

// scriptOn installs `f` on the station, recording each hit in the global order.
func (s *fa6State) scriptOn(st *fstation, f func(n int, w http.ResponseWriter, r *http.Request)) {
	st.set(func(n int, w http.ResponseWriter, r *http.Request) {
		b, _ := readAllKeep(r)
		s.hitMu.Lock()
		s.hits = append(s.hits, st.name)
		s.bodies[st.name] = append(s.bodies[st.name], b)
		s.hitMu.Unlock()
		f(n, w, r)
	})
}

func fa6Real(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + fa6CompletionMarker + ` The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20}}`))
}

func fa6RealStream(w http.ResponseWriter, from string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s from %s\"}}]}\n\n", fa6CompletionMarker, from)
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5000,\"completion_tokens\":20}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (s *fa6State) real(st *fstation) {
	s.scriptOn(st, func(_ int, w http.ResponseWriter, r *http.Request) {
		if fa6IsStream(r) {
			fa6RealStream(w, st.name)
			return
		}
		fa6Real(w)
	})
}

func fa6IsStream(r *http.Request) bool {
	b, _ := readAllKeep(r)
	return bytes.Contains(b, []byte(`"stream":true`))
}

// readAllKeep reads the request body and puts it back so a later read sees it again.
func readAllKeep(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	r.Body.Close()
	b := buf.Bytes()
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, err
}

// answerFrom makes the station answer (status, body, extra headers) for the NEXT request only
// (every=false) or for every request; other requests are served normally.
func (s *fa6State) answerFrom(st *fstation, every bool, status int, body string, hdr map[string]string) {
	first := st.upstreamCount() + 1
	s.scriptOn(st, func(n int, w http.ResponseWriter, r *http.Request) {
		if every || n == first {
			w.Header().Set("Content-Type", "application/json")
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		if fa6IsStream(r) {
			fa6RealStream(w, st.name)
			return
		}
		fa6Real(w)
	})
}

func fa6ErrBody(msg string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	return string(b)
}

func (s *fa6State) curate(st *fstation, provider string) {
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Curated, reg.CuratedProvider, reg.Region = true, provider, provider
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
}

func (s *fa6State) setCapacity(st *fstation, capacity, busy int) {
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[st.id] = float64(capacity) * tpsPerSlot
	s.b.inflight[st.id] = busy
	s.b.metricsMu.Unlock()
}

func (s *fa6State) setTTFT(st *fstation, ms int) {
	s.b.mu.Lock()
	tq := s.b.trust[st.id]
	tq.ttftMs = float64(ms)
	s.b.trust[st.id] = tq
	s.b.mu.Unlock()
}

// setOfferField sets a field on the station's (first) offer through reflection, failing with
// the field's name when protocol.ModelOffer has no such field (a contract field not built).
func (s *fa6State) setOfferField(st *fstation, field string, v any) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reg := s.b.nodes[st.id]
	if len(reg.Offers) == 0 {
		return fmt.Errorf("%s has no offer", st.name)
	}
	o := reflect.ValueOf(&reg.Offers[0]).Elem()
	f := o.FieldByName(field)
	if !f.IsValid() || !f.CanSet() {
		return fmt.Errorf("protocol.ModelOffer has no %s field: cannot declare it on %s (contract §14 field not built)", field, st.name)
	}
	f.Set(reflect.ValueOf(v).Convert(f.Type()))
	s.b.nodes[st.id] = reg
	return nil
}

// --- relaying ----------------------------------------------------------------------------------

func (s *fa6State) snapshot() {
	s.mark = map[string]int{}
	for n, st := range s.stations {
		s.mark[n] = st.upstreamCount()
	}
	s.hitMu.Lock()
	s.hits = nil
	s.hitMu.Unlock()
	s.resps = nil
	s.heartbeat()
}

func (s *fa6State) sinceMark(name string) int {
	st, ok := s.stations[name]
	if !ok {
		return 0
	}
	return st.upstreamCount() - s.mark[name]
}

func (s *fa6State) body(sp fa6Spec) []byte {
	if sp.raw != nil {
		return sp.raw
	}
	model := sp.model
	if model == "" {
		model = s.lastModel
	}
	n := sp.prompt
	if n == 0 {
		n = s.defPrompt
	}
	if n == 0 {
		n = 50
	}
	m := map[string]any{"model": model, "messages": []map[string]any{{"role": "user", "content": utPrompt(n)}}}
	if sp.stream {
		m["stream"] = true
	}
	for k, v := range sp.extra {
		if k == "model" {
			m["model"] = v
			continue
		}
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// do fires ONE real relay and records the outcome.
func (s *fa6State) do(sp fa6Spec) fa6Resp {
	body := s.body(sp)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ip := sp.ip
	who := sp.who
	if sp.grant != "" {
		r.Header.Set("Authorization", "Bearer "+sp.grant)
	} else {
		priv := sp.priv
		if priv == nil {
			if who == "" {
				who = "alice"
			}
			w := s.consumer(who)
			priv = w.priv
			if ip == "" {
				ip = w.ip
			}
		}
		signReq(r, priv, body)
	}
	if ip == "" {
		ip = "198.51.100.250"
	}
	r.Header.Set("CF-Connecting-IP", ip)
	r.RemoteAddr = ip + ":40000"
	for k, v := range sp.hdr {
		r.Header.Set(k, v)
	}
	b := sp.b
	if b == nil {
		b = s.b
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	b.relay(w, r)
	res := fa6Resp{who: who, code: w.Code, body: w.Body.Bytes(), hdr: w.Header(), stream: sp.stream}
	s.resps = append(s.resps, res)
	s.last = res
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = w.Code, w.Body.Bytes(), w.Header(), w
	return res
}

func fa6Order(ids ...string) map[string]any { return map[string]any{"order": ids} }

// candidateFor: one relay as payer `who` with provider.only [station] reaches the station.
func (s *fa6State) candidateFor(name, who string, b *broker) bool {
	st := s.ensureNode(name)
	before := st.upstreamCount()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": map[string]any{"only": []string{st.id}}}, b: b})
	return st.upstreamCount() > before
}

func (s *fa6State) served(r fa6Resp) string {
	id := r.hdr.Get("X-RogerAI-Provider")
	for n, st := range s.stations {
		if st.id == id {
			return n
		}
	}
	if relay := r.hdr.Get("X-RogerAI-Relay"); relay != "" {
		for n, tw := range s.towers {
			if tw.id == relay {
				return n
			}
		}
		return "tower:" + relay
	}
	return id
}

func (s *fa6State) shares() (map[string]int, int) {
	out, total := map[string]int{}, 0
	for _, r := range s.resps {
		if r.code != 200 {
			continue
		}
		total++
		out[s.served(r)]++
	}
	return out, total
}

func (s *fa6State) errCode(r fa6Resp) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

func (s *fa6State) owner(alias string) (*fstation, error) {
	name, ok := s.ops[alias]
	if !ok {
		return nil, fmt.Errorf("no station owned by %q in this scenario", alias)
	}
	return s.st(name), nil
}

// latestReceiptKeys returns the stored receipt (as raw keys) of the latest attempt that
// station `name` made for wallet `who`.
func (s *fa6State) latestReceiptKeys(who, name string) (map[string]any, error) {
	w := s.consumer(who)
	es, err := s.db.RecentByUser(w.wallet, 2000)
	if err != nil {
		return nil, err
	}
	st := s.st(name)
	for _, e := range es {
		if e.Node == st.id {
			_, keys, err := s.storedReceipt(e.RequestID)
			return keys, err
		}
	}
	return nil, fmt.Errorf("no receipt for %s's attempt on %s (%d receipts for the wallet)", who, name, len(es))
}

func fa6Pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func atoiMust(v string) int     { n, _ := strconv.Atoi(v); return n }
func atofMust(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }

// --- Background ------------------------------------------------------------------------------

func (s *fa6State) strikeThresholds(warn, ban string) error {
	s.b.strikeWarnAt, s.b.strikeBanAt = atoiMust(warn), atoiMust(ban)
	return nil
}

func (s *fa6State) strikeFloor(n string) error {
	s.setenv("ROGERAI_STRIKE_MIN_PAYERS", n)
	return nil
}

func (s *fa6State) cooldownThreshold(k, window string) error {
	s.setenv("ROGERAI_COOLDOWN_MIN_PAYERS", k)
	s.setenv("ROGERAI_COOLDOWN_PAYER_WINDOW", window+"s")
	return nil
}

func (s *fa6State) fourConsumers(a, b, c, d, amount string) error {
	for _, n := range []string{a, b, c, d} {
		if err := s.fundWho(n, atofMust(amount)); err != nil {
			return err
		}
	}
	return nil
}

// --- Given: stations -------------------------------------------------------------------------

func (s *fa6State) nodeOwnedOnAir(name, op, model string) error {
	s.lastModel = model
	s.defaultNode(name, model)
	s.ops[op] = name
	return nil
}

func (s *fa6State) nodeOnAir(name, model string) error {
	s.lastModel = model
	s.defaultNode(name, model)
	return nil
}

func (s *fa6State) twoNodesTwoModels(n1, m1, n2, m2 string) error {
	s.defaultNode(n1, m1)
	s.defaultNode(n2, m2)
	s.lastModel = m1
	return nil
}

func (s *fa6State) twoNodesOnAir(n1, n2, model string) error {
	s.lastModel = model
	s.defaultNode(n1, model)
	s.defaultNode(n2, model)
	return nil
}

func (s *fa6State) answersNextRaw(name, status, body string) error {
	s.answerFrom(s.ensureNode(name), false, atoiMust(status), body, nil)
	return nil
}

func (s *fa6State) answersNextMsg(name, status, msg string) error {
	s.answerFrom(s.ensureNode(name), false, atoiMust(status), fa6ErrBody(msg), nil)
	return nil
}

func (s *fa6State) answers429RA(name, ra string) error {
	s.answerFrom(s.ensureNode(name), false, 429, utDefaultBody(429), map[string]string{"Retry-After": ra})
	return nil
}

func (s *fa6State) answersEvery429RA(name, ra string) error {
	s.answerFrom(s.ensureNode(name), true, 429, utDefaultBody(429), map[string]string{"Retry-After": ra})
	return nil
}

func (s *fa6State) answersEvery(name, status string) error {
	code := atoiMust(status)
	body := fa6ErrBody("upstream says " + status)
	if code == 429 {
		body = utDefaultBody(429)
	}
	s.answerFrom(s.ensureNode(name), true, code, body, nil)
	return nil
}

func (s *fa6State) answersEveryBody(name, status, body string) error {
	s.answerFrom(s.ensureNode(name), true, atoiMust(status), body, nil)
	return nil
}

func (s *fa6State) rejectsTopKey(name, key, status string) error {
	st := s.ensureNode(name)
	code := atoiMust(status)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, r *http.Request) {
		b, _ := readAllKeep(r)
		if bytes.Contains(b, []byte(`"`+key+`":`)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(fa6ErrBody("unknown parameter: " + key)))
			return
		}
		fa6Real(w)
	})
	return nil
}

// fa6KeyName extracts the top-level key name of a `"key": value` fragment.
var fa6KeyRe = regexp.MustCompile(`^"([^"]+)"\s*:\s*(.+)$`)

func (s *fa6State) serverRejectsKey(name, op, frag, status string) error {
	s.lastModel = "qwen3-32b"
	s.defaultNode(name, "qwen3-32b")
	s.ops[op] = name
	m := fa6KeyRe.FindStringSubmatch(frag)
	if m == nil {
		return fmt.Errorf("cannot parse key fragment %s", frag)
	}
	return s.rejectsTopKey(name, m[1], status)
}

// --- When: relays ----------------------------------------------------------------------------

func (s *fa6State) relaysPinned(who, model, node string) error {
	s.snapshot()
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": fa6Order(s.ensureNode(node).id)}})
	return nil
}

func (s *fa6State) relaysTo(who, model string) error {
	s.snapshot()
	s.landOrder()
	s.lastModel = model
	s.do(fa6Spec{who: who, model: model})
	return nil
}

func (s *fa6State) relaysOnceTo(who, model string) error { return s.relaysTo(who, model) }

func (s *fa6State) relaysModelList(who, model, next string) error {
	s.snapshot()
	s.landOrder()
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{"models": []string{next}}})
	return nil
}

func (s *fa6State) relaysNTimesPinnedNoFallback(who, n, model, node, key, val string) error {
	s.snapshot()
	st := s.ensureNode(node)
	s.succSnap[node] = s.successOf(st)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: model, extra: map[string]any{
			"provider": map[string]any{"order": []string{st.id}, "allow_fallbacks": false},
			key:        atoiMust(val),
		}})
	}
	return nil
}

func (s *fa6State) relaysNPinnedNoFallback(who, n, model, node string) error {
	s.snapshot()
	st := s.ensureNode(node)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": map[string]any{"order": []string{st.id}, "allow_fallbacks": false}}})
	}
	return nil
}

func (s *fa6State) relaysNPinned(who, n, model, node string) error {
	s.snapshot()
	st := s.ensureNode(node)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": fa6Order(st.id)}})
	}
	return nil
}

func (s *fa6State) relaysNTimes(who, model, n string) error {
	s.snapshot()
	s.landOrder()
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

func (s *fa6State) differentConsumersWithKey(n, model, frag string) error {
	s.snapshot()
	m := fa6KeyRe.FindStringSubmatch(frag)
	if m == nil {
		return fmt.Errorf("cannot parse key fragment %s", frag)
	}
	var v any
	if err := json.Unmarshal([]byte(m[2]), &v); err != nil {
		return fmt.Errorf("fragment value %s: %v", m[2], err)
	}
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("drift-%d", i)
		if err := s.fundWho(who, 10); err != nil {
			return err
		}
		s.do(fa6Spec{who: who, model: model, extra: map[string]any{m[1]: v}})
	}
	return nil
}

func (s *fa6State) distinctConsumersEach(n, model string) error {
	s.snapshot()
	s.landOrder()
	for i := 0; i < atoiMust(n); i++ {
		who := fa6Names[i%len(fa6Names)]
		if i >= len(fa6Names) {
			who = fmt.Sprintf("payer-%d", i)
		}
		if err := s.fundWho(who, 0); err != nil {
			return err
		}
		if s.balanceOf(who) < 1 {
			_ = s.fundWho(who, 10)
		}
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

var fa6Names = []string{"alice", "bob", "carol", "dave"}

func (s *fa6State) unboundOneIPEach(n, model string) error {
	s.snapshot()
	s.lastModel = model
	ip := s.nextIP()
	for i := 0; i < atoiMust(n); i++ {
		w := s.unboundKey(ip)
		s.do(fa6Spec{priv: w.priv, ip: ip, model: model})
		s.coolSeenUpdate()
	}
	return nil
}

func (s *fa6State) unboundOneIPEachNoModel(n string) error { return s.unboundOneIPEach(n, s.lastModel) }

func (s *fa6State) coolSeenUpdate() {
	for n := range s.stations {
		if s.isCooling(n) {
			s.coolSeen[n] = true
		}
	}
}

// --- Then: outcomes ------------------------------------------------------------------------------

func (s *fa6State) statusIs(code string) error {
	if s.last.code != atoiMust(code) {
		return fmt.Errorf("response status %d, want %s: %.300s", s.last.code, code, s.last.body)
	}
	return nil
}

func (s *fa6State) receivedNothing(name string) error {
	if n := s.sinceMark(name); n != 0 {
		return fmt.Errorf("%q received %d request(s), want none", name, n)
	}
	return nil
}

func (s *fa6State) attemptVoided(reason, upstream string) error {
	var name string
	for n := range s.stations {
		if s.sinceMark(n) > 0 {
			name = n
		}
	}
	if name == "" {
		return fmt.Errorf("no station was dispatched to")
	}
	keys, err := s.latestReceiptKeys(s.last.who, name)
	if err != nil {
		return err
	}
	if got, _ := keys["void_reason"].(string); got != reason {
		return fmt.Errorf("the attempt's void_reason is %q, want %q (receipt %v)", got, reason, keys)
	}
	if got := fmt.Sprint(keys["upstream_status"]); got != upstream {
		return fmt.Errorf("the attempt's upstream_status is %q, want %s", got, upstream)
	}
	return nil
}

func (s *fa6State) charged(who, amount string) error {
	w := s.consumer(who)
	bal, err := s.db.PeekBalance(w.wallet)
	if err != nil {
		return err
	}
	start := s.funding(who)
	if diff := start - bal; diff > atofMust(amount)+1e-9 {
		return fmt.Errorf("%s was charged %.6f, want %s", who, diff, amount)
	}
	return nil
}

func (s *fa6State) funding(who string) float64 {
	if v, ok := s.scen["fund:"+who].(float64); ok {
		return v
	}
	return 10
}

func (s *fa6State) noStrikes(op string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	rows, err := s.db.StrikesByOwner(st.acct, 0)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("%d owner_strikes row(s) exist for %s, want none (%+v)", len(rows), op, rows[0])
	}
	return nil
}

func (s *fa6State) payouts(op, state string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	held, err := s.db.AccountRecountHeld(st.acct)
	if err != nil {
		return err
	}
	if want := state == "held"; held != want {
		return fmt.Errorf("%s's payouts held=%v, want %s", op, held, state)
	}
	return nil
}

func (s *fa6State) successOf(st *fstation) float64 {
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	return s.b.success[st.id]
}

func (s *fa6State) successUnchanged(op string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	if got, was := s.successOf(st), s.succSnap[st.name]; got != was {
		return fmt.Errorf("%s's routing success rate moved %.4f -> %.4f", op, was, got)
	}
	return nil
}

func (s *fa6State) servedBy200(name string) error {
	if s.last.code != 200 {
		return fmt.Errorf("response %d, want 200 served by %s: %.300s", s.last.code, name, s.last.body)
	}
	if got := s.served(s.last); got != name {
		return fmt.Errorf("served by %q, want %q", got, name)
	}
	return nil
}

func (s *fa6State) attemptVoidReasonNot(name, want, not string) error {
	keys, err := s.latestReceiptKeys(s.last.who, name)
	if err != nil {
		return err
	}
	got, _ := keys["void_reason"].(string)
	if got != want || got == not {
		return fmt.Errorf("%s's attempt void_reason %q, want %q (not %q)", name, got, want, not)
	}
	return nil
}

func (s *fa6State) attemptVoidReason(want string) error {
	for n := range s.stations {
		if s.sinceMark(n) == 0 {
			continue
		}
		keys, err := s.latestReceiptKeys(s.last.who, n)
		if err != nil {
			return err
		}
		if got, _ := keys["void_reason"].(string); got != want {
			return fmt.Errorf("the attempt's void_reason is %q, want %q", got, want)
		}
		return nil
	}
	return fmt.Errorf("no attempt was dispatched")
}

func (s *fa6State) everyResponse(code string) error {
	for i, r := range s.resps {
		if r.code != atoiMust(code) {
			return fmt.Errorf("response %d of %d is %d, want %s: %.200s", i+1, len(s.resps), r.code, code, r.body)
		}
	}
	return nil
}

func (s *fa6State) exactlyOneAttempt() error {
	total := 0
	for n := range s.stations {
		total += s.sinceMark(n)
	}
	if total != 1 {
		return fmt.Errorf("%d attempts were dispatched, want exactly one", total)
	}
	return nil
}

func (s *fa6State) bodyErrCode(code string) error {
	if got := s.errCode(s.last); got != code {
		return fmt.Errorf("error.code %q, want %q: %.300s", got, code, s.last.body)
	}
	return nil
}

func (s *fa6State) metaStation(name string) error {
	var e struct {
		Error struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"error"`
	}
	_ = json.Unmarshal(s.last.body, &e)
	got := fmt.Sprint(e.Error.Metadata["station"])
	if got != name && got != s.st(name).id {
		return fmt.Errorf("error.metadata.station %q, want %q: %.300s", got, name, s.last.body)
	}
	return nil
}

func (s *fa6State) metaRaw() error {
	var e struct {
		Error struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"error"`
	}
	_ = json.Unmarshal(s.last.body, &e)
	raw := e.Error.Metadata["raw"]
	if raw == nil || !strings.Contains(fmt.Sprint(raw), "unknown parameter") {
		return fmt.Errorf("error.metadata.raw does not carry the station's body: %.300s", s.last.body)
	}
	return nil
}

func (s *fa6State) strikeRows(n, kind, op string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	rows, err := s.db.StrikesByOwner(st.acct, 0)
	if err != nil {
		return err
	}
	c := 0
	for _, r := range rows {
		if r.Kind == kind {
			c++
		}
	}
	if c != atoiMust(n) {
		return fmt.Errorf("%d %q strike row(s) for %s, want %s", c, kind, op, n)
	}
	return nil
}

func (s *fa6State) strikesNamePayer(op, who string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	rows, err := s.db.StrikesByOwner(st.acct, 0)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no strike rows for %s", op)
	}
	w := s.consumer(who)
	for _, r := range rows {
		if !strings.Contains(r.Evidence, w.wallet) {
			return fmt.Errorf("strike evidence %s does not name the payer %s (%s)", r.Evidence, who, w.wallet)
		}
	}
	return nil
}

// --- impossible input -----------------------------------------------------------------------------

func (s *fa6State) claimsImpossible(name, op string) error {
	s.lastModel = "qwen3-32b"
	st := s.defaultNode(name, "qwen3-32b")
	s.ops[op] = name
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}],"usage":{"prompt_tokens":9000000,"completion_tokens":5}}`))
	})
	return nil
}

func (s *fa6State) bannedAsToday(op string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	if !s.b.isOwnerBanned(st.acct) {
		return fmt.Errorf("%s is not banned after a zero-doubt impossible-input claim", op)
	}
	return nil
}

func (s *fa6State) chainZeroReceipt(name, reason string) error {
	keys, err := s.latestReceiptKeys(s.last.who, name)
	if err != nil {
		return err
	}
	if got, _ := keys["void_reason"].(string); got != reason {
		return fmt.Errorf("%s's chain receipt void_reason %q, want %q", name, got, reason)
	}
	if c, ok := keys["cost"].(float64); ok && c != 0 {
		return fmt.Errorf("%s's receipt cost %v, want $0", name, c)
	}
	return nil
}

// generationRecord GETs /generation for the last request as its maker.
func (s *fa6State) generationRecord() (map[string]any, error) {
	id := s.last.hdr.Get("X-RogerAI-Request-Id")
	if id == "" {
		return nil, fmt.Errorf("the response carried no X-RogerAI-Request-Id")
	}
	r := httptest.NewRequest(http.MethodGet, "/generation?id="+id, nil)
	signReq(r, s.consumer(s.last.who).priv, nil)
	rr := httptest.NewRecorder()
	s.b.routes().ServeHTTP(rr, r)
	if rr.Code != 200 {
		return nil, fmt.Errorf("GET /generation = %d %.300s", rr.Code, rr.Body.String())
	}
	var m map[string]any
	return m, json.Unmarshal(rr.Body.Bytes(), &m)
}

func (s *fa6State) generationAttemptCode(code string) error {
	rec, err := s.generationRecord()
	if err != nil {
		return err
	}
	atts, _ := rec["attempts"].([]any)
	for _, a := range atts {
		if m, ok := a.(map[string]any); ok && m["error_code"] == code {
			return nil
		}
	}
	return fmt.Errorf("/generation attempts carry no error_code %q: %v", code, rec["attempts"])
}

// --- cooldown ------------------------------------------------------------------------------------

func (s *fa6State) answersPayerNext429(name, who, ra string) error { return s.answers429RA(name, ra) }

func (s *fa6State) isCandidateFor(name, who string) error {
	if !s.candidateFor(name, who, nil) {
		return fmt.Errorf("%q is not a candidate for %q (a relay with provider.only never reached it: %d %.200s)", name, who, s.last.code, s.last.body)
	}
	return nil
}

func (s *fa6State) notCandidateForSecs(name, who, secs string) error {
	if s.candidateFor(name, who, nil) {
		return fmt.Errorf("%q is a candidate for %q, want not for %ss", name, who, secs)
	}
	return nil
}

func (s *fa6State) threePayersWithin(a, b, c, model, secs string) error {
	s.snapshot()
	s.landOrder()
	for i, who := range []string{a, b, c} {
		if i > 0 {
			s.advance(time.Duration(atoiMust(secs)/3) * time.Second)
		}
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

func (s *fa6State) coolingForEveryone(name, secs string) error {
	if !s.isCooling(name) {
		return fmt.Errorf("%q is not cooling for every payer", name)
	}
	ra, err := s.payerRetryAfter(name, "dave")
	if err != nil {
		return err
	}
	if ra < atoiMust(secs)-2 || ra > atoiMust(secs) {
		return fmt.Errorf("station cooldown Retry-After %d, want ~%s", ra, secs)
	}
	return nil
}

// payerRetryAfter: a relay as `who` pinned to the station with no fallback; a cooling pair or
// station answers 503 band_cooling with its Retry-After.
func (s *fa6State) payerRetryAfter(name, who string) (int, error) {
	st := s.ensureNode(name)
	s.heartbeat()
	s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": map[string]any{"only": []string{st.id}}}})
	if s.last.code != 503 {
		return 0, fmt.Errorf("relay as %s pinned to %s = %d, want 503 band_cooling: %.200s", who, name, s.last.code, s.last.body)
	}
	return atoiMust(s.last.hdr.Get("Retry-After")), nil
}

func (s *fa6State) servedByFor(who, name string) error {
	s.do(fa6Spec{who: who, model: s.lastModel})
	return s.servedBy200(name)
}

func (s *fa6State) twoPayersEachOnce(a, b, model string) error {
	s.snapshot()
	s.landOrder()
	s.do(fa6Spec{who: a, model: model})
	s.coolSeenUpdate()
	s.do(fa6Spec{who: b, model: model})
	s.coolSeenUpdate()
	return nil
}

func (s *fa6State) nodeEvery429(name string) error {
	s.ensureNode(name)
	return s.answersEvery(name, "429")
}

func (s *fa6State) relaysAtTimes(a, b, tb, c, tc string) error {
	s.snapshot()
	s.landOrder()
	s.do(fa6Spec{who: a, model: s.lastModel})
	s.coolSeenUpdateEveryone()
	s.advance(time.Duration(atoiMust(tb)) * time.Second)
	s.do(fa6Spec{who: b, model: s.lastModel})
	s.coolSeenUpdateEveryone()
	s.advance(time.Duration(atoiMust(tc)-atoiMust(tb)) * time.Second)
	s.do(fa6Spec{who: c, model: s.lastModel})
	s.coolSeenUpdateEveryone()
	return nil
}

func (s *fa6State) coolSeenUpdateEveryone() { s.coolSeenUpdate() }

func (s *fa6State) neverCooledForEveryone(name string) error {
	if s.coolSeen[name] {
		return fmt.Errorf("%q cooled for every payer during the When", name)
	}
	return nil
}

func (s *fa6State) pairCapNever(who, name string) error {
	ra, err := s.payerRetryAfter(name, who)
	if err != nil {
		// not cooling at all for the payer: no cap to exceed
		if s.last.code == 429 || s.last.code == 200 {
			return nil
		}
		return err
	}
	if ra > s.cooldownCap() {
		return fmt.Errorf("%s's pair cooldown Retry-After %d exceeds the cap %d", who, ra, s.cooldownCap())
	}
	return nil
}

func (s *fa6State) cooldownCap() int {
	return int(cooldownMax().Seconds())
}

func (s *fa6State) onlyStation(name, model string) error {
	s.lastModel = model
	s.defaultNode(name, model)
	return nil
}

func (s *fa6State) pairCoolingFor(who, name, secs string) error {
	st := s.ensureNode(name)
	s.answerFrom(st, false, 429, utDefaultBody(429), map[string]string{"Retry-After": secs})
	s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": map[string]any{"only": []string{st.id}}}})
	if s.last.code != 429 {
		return fmt.Errorf("making %s's pair cool: relay = %d, want 429 (%.200s)", who, s.last.code, s.last.body)
	}
	s.real(st)
	return nil
}

func (s *fa6State) resp503CodeRA(code, ra string) error {
	if s.last.code != 503 {
		return fmt.Errorf("response %d, want 503: %.300s", s.last.code, s.last.body)
	}
	if got := s.errCode(s.last); got != code {
		return fmt.Errorf("error.code %q, want %q: %.300s", got, code, s.last.body)
	}
	if got := s.last.hdr.Get("Retry-After"); atoiMust(got) < atoiMust(ra)-1 || atoiMust(got) > atoiMust(ra) {
		return fmt.Errorf("Retry-After %q, want %s", got, ra)
	}
	return nil
}

func (s *fa6State) noAttempt() error {
	for n := range s.stations {
		if s.sinceMark(n) > 0 {
			return fmt.Errorf("%q received %d attempt(s), want none", n, s.sinceMark(n))
		}
	}
	return nil
}

func (s *fa6State) twoInstances() error {
	s.instanceB()
	return nil
}

func (s *fa6State) answeredOnA(name, who, ra string) error {
	st := s.ensureNode(name)
	if s.b2 == nil {
		s.instanceB()
	}
	s.answerFrom(st, false, 429, utDefaultBody(429), map[string]string{"Retry-After": ra})
	s.do(fa6Spec{who: who, model: st.model})
	if s.last.code != 429 && s.last.code != 503 {
		return fmt.Errorf("the 429 on instance A answered %d: %.200s", s.last.code, s.last.body)
	}
	s.real(st)
	return nil
}

func (s *fa6State) relaysOnB(who, model string) error {
	s.snapshot()
	s.do(fa6Spec{who: who, model: model, b: s.b2})
	return nil
}

func (s *fa6State) notCandidateOnB(name, who string) error {
	if s.candidateFor(name, who, s.b2) {
		return fmt.Errorf("%q is a candidate for %q on instance B", name, who)
	}
	return nil
}

func (s *fa6State) answersPayerHuge(name, who, ra string) error {
	return s.pairCoolingFor(who, name, ra)
}

func (s *fa6State) pairIsCap(who, name string) error {
	ra, err := s.payerRetryAfter(name, who)
	if err != nil {
		return err
	}
	if ra != s.cooldownCap() && ra != s.cooldownCap()-1 {
		return fmt.Errorf("%s's pair cooldown Retry-After %d, want the cap %d", who, ra, s.cooldownCap())
	}
	s.scen["capRA"] = ra
	return nil
}

func (s *fa6State) answersAgainBeforeExpiry(name, who, ra string) error {
	// The pair is cooling, so no relay of `who` reaches the station: the second throttle verdict
	// is the station's own report of a 429 for that payer, applied through the production
	// cooling entry point.
	st := s.ensureNode(name)
	s.b.coolStation(st.id, st.model, atoiMust(ra))
	return nil
}

func (s *fa6State) pairKeepsLater() error {
	for n := range s.stations {
		ra, err := s.payerRetryAfter(n, "alice")
		if err != nil {
			return err
		}
		if ra < s.cooldownCap()-2 {
			return fmt.Errorf("the pair cooldown fell to %ds, want the later expiry (~%ds)", ra, s.cooldownCap())
		}
		return nil
	}
	return fmt.Errorf("no station in the scenario")
}

func (s *fa6State) freeOnAir(name, model string) error {
	s.lastModel = model
	s.node(name, model, 0, 0)
	s.b.seedFunds = 0.5
	return nil
}

func (s *fa6State) countAsOnePayer() error {
	for n := range s.stations {
		if s.coolSeen[n] || s.isCooling(n) {
			return fmt.Errorf("%q cooled for every payer: the unbound keypairs counted as distinct payers", n)
		}
	}
	return nil
}

func (s *fa6State) distinctRelayWithin(n, within string) error {
	s.snapshot()
	s.landOrder()
	k := atoiMust(n)
	step := time.Duration(0)
	if k > 1 {
		step = time.Duration(atoiMust(within)) * time.Second / time.Duration(k-1)
	}
	for i := 0; i < k; i++ {
		if i > 0 {
			s.advance(step)
		}
		who := fa6Names[i%len(fa6Names)]
		s.do(fa6Spec{who: who, model: s.lastModel})
	}
	return nil
}

func (s *fa6State) coolingState(name, state string) error {
	cool := s.isCooling(name)
	if state == "is cooling" && !cool {
		return fmt.Errorf("%q is not cooling for every payer", name)
	}
	if state == "is NOT cooling" && cool {
		return fmt.Errorf("%q is cooling for every payer", name)
	}
	return nil
}

// cooledThreeTimes drives three station-wide cooldowns at the cap (each behind real demand
// from three distinct payers) and runs the production alert check, as the approved
// "a station that keeps cooling pages the founder once" fixture does.
func (s *fa6State) cooledNTimes(name, times string) error {
	rounds := map[string]int{"three": 3, "five": 5}[times]
	st := s.ensureNode(name)
	s.trustSnp[name] = s.trustOf(st)
	s.b.adminEmails = []string{"founder@example.com"}
	s.mailsMark = len(s.mails)
	for round := 0; round < rounds; round++ {
		s.answerFrom(st, true, 429, utDefaultBody(429), map[string]string{"Retry-After": "120"})
		for _, who := range []string{"alice", "bob", "carol"} {
			s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": map[string]any{"only": []string{st.id}}}})
		}
		s.advance(130 * time.Second)
	}
	s.b.alertCheckOnce(s.now())
	s.b.alertCheckOnce(s.now())
	s.real(st)
	return nil
}

func (s *fa6State) trustOf(st *fstation) trustState {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.trust[st.id]
}

func (s *fa6State) trustUnchanged(name string) error {
	st := s.st(name)
	got, was := s.trustOf(st), s.trustSnp[name]
	if got.probeFails != was.probeFails || got.probeOK != was.probeOK || got.probed != was.probed {
		return fmt.Errorf("%s's trust state moved: %+v -> %+v", name, was, got)
	}
	return nil
}

func (s *fa6State) pagedOnce() error {
	time.Sleep(200 * time.Millisecond) // the alert mail is sent off the request path
	s.mailMu.Lock()
	mails := append([]string(nil), s.mails[s.mailsMark:]...)
	s.mailMu.Unlock()
	n := 0
	for _, m := range mails {
		if strings.Contains(strings.ToLower(m), "cooling") {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d cooling page(s) sent, want exactly one", n)
	}
	return nil
}

func (s *fa6State) onlyPairCooling(who string) error {
	st := s.defaultNode("n-only", s.lastModel)
	s.mailsMark = len(s.mails)
	return s.pairCoolingFor(who, st.name, "30")
}

func (s *fa6State) noCoolingAlert() error {
	for _, m := range s.mails[s.mailsMark:] {
		if strings.Contains(strings.ToLower(m), "cooling") {
			return fmt.Errorf("a cooling alert was sent: %.200s", m)
		}
	}
	return nil
}

func (s *fa6State) marketCountsOnAir() error {
	r := httptest.NewRequest(http.MethodGet, "/market", nil)
	rr := httptest.NewRecorder()
	s.b.market(rr, r)
	if !strings.Contains(rr.Body.String(), s.lastModel) {
		return fmt.Errorf("/market does not list %s: %.300s", s.lastModel, rr.Body.String())
	}
	if s.isCooling("n-only") {
		return fmt.Errorf("the only station is cooling for every payer: the market would read 0 providers")
	}
	return nil
}

// --- TPM ------------------------------------------------------------------------------------------

func (s *fa6State) registerCurated(model, tpm string, curated bool) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, ownerPriv, _ := ed25519.GenerateKey(nil)
	ownerPub := hex.EncodeToString(ownerPriv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 500000, Login: "tpm-" + s.nonce + "-" + strconv.FormatBool(curated), Pubkey: ownerPub}); err != nil {
		return err
	}
	id := "tpm-" + utNonce()
	offer := protocol.ModelOffer{Model: model, Ctx: 131072}
	reg := protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub), BridgeToken: "tok-" + id, TS: time.Now().Unix()}
	if curated {
		reg.Curated, reg.CuratedProvider = true, "openrouter"
		offer.UpstreamIn, offer.UpstreamOut = 0.10, 0.30
	} else {
		offer.PriceIn, offer.PriceOut = 0.10, 0.30
	}
	offer.TPM = atoiMust(tpm) // a signed field of the offer, like every other
	reg.Offers = []protocol.ModelOffer{offer}
	reg.SignRegistration(priv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, ownerPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode, s.regBody = w.Code, w.Body.String()
	s.scen["regNode"] = id
	return nil
}

func (s *fa6State) curatedRegistersTPM(model, tpm string) error {
	return s.registerCurated(model, tpm, true)
}
func (s *fa6State) humanRegistersTPM(model, tpm string) error {
	return s.registerCurated(model, tpm, false)
}

func (s *fa6State) regAcceptedShowsTPM(tpm string) error {
	if s.regCode != 200 {
		return fmt.Errorf("registration = %d %s, want accepted", s.regCode, s.regBody)
	}
	id, _ := s.scen["regNode"].(string)
	r := httptest.NewRequest(http.MethodGet, "/discover", nil)
	rr := httptest.NewRecorder()
	s.b.discover(rr, r)
	var d struct {
		Offers []map[string]any `json:"offers"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	for _, o := range d.Offers {
		if o["node_id"] == id {
			if fmt.Sprint(o["tpm"]) != tpm {
				return fmt.Errorf("/discover offer tpm %v, want %s", o["tpm"], tpm)
			}
			return nil
		}
	}
	return fmt.Errorf("/discover lists no offer for the registered node %s", id)
}

func (s *fa6State) regRejectedNaming(what string) error {
	if s.regCode == 200 {
		return fmt.Errorf("registration accepted, want rejected naming %q", what)
	}
	if !strings.Contains(s.regBody, what) {
		return fmt.Errorf("rejection %q does not name %q", s.regBody, what)
	}
	return nil
}

func (s *fa6State) curatedTPMOnly(name, tpm, model string) error {
	s.lastModel = model
	st := s.defaultNode(name, model)
	s.curate(st, "openrouter")
	return s.setOfferField(st, "TPM", atoiMust(tpm))
}

func (s *fa6State) tpmShare(pct string) error {
	s.setenv("ROGERAI_TPM_REQUEST_SHARE", pct)
	return nil
}

func (s *fa6State) relaysMeasuredPrompt(who, n string) error {
	s.snapshot()
	s.do(fa6Spec{who: who, model: s.lastModel, prompt: atoiMust(n)})
	return nil
}

func (s *fa6State) resp429CodeRA(code string) error {
	if s.last.code != 429 {
		return fmt.Errorf("response %d, want 429: %.300s", s.last.code, s.last.body)
	}
	if got := s.errCode(s.last); got != code {
		return fmt.Errorf("error.code %q, want %q: %.300s", got, code, s.last.body)
	}
	if s.last.hdr.Get("Retry-After") == "" {
		return fmt.Errorf("no Retry-After")
	}
	return nil
}

func (s *fa6State) notCoolingForAnyone(name string) error {
	if s.isCooling(name) {
		return fmt.Errorf("%q is cooling", name)
	}
	if !s.candidateFor(name, "bob", nil) {
		return fmt.Errorf("%q is not a candidate for bob", name)
	}
	return nil
}

func (s *fa6State) curatedTPMAndHuman(cur, tpm, human, model string) error {
	if err := s.curatedTPMOnly(cur, tpm, model); err != nil {
		return err
	}
	s.defaultNode(human, model)
	return nil
}

func (s *fa6State) curatedNoTPMOnly(name, model string) error {
	s.lastModel = model
	s.curate(s.defaultNode(name, model), "openrouter")
	return nil
}

func (s *fa6State) receivesRequest(name string) error {
	if s.sinceMark(name) == 0 {
		return fmt.Errorf("%q received nothing: %d %.200s", name, s.last.code, s.last.body)
	}
	return nil
}

// --- rate limits -----------------------------------------------------------------------------------

func (s *fa6State) realLimiters() {
	if s.scen["limiters"] == nil {
		s.b.rl = loadRateLimiter()
		s.b.anonRL = loadAnonRateLimiter()
		s.scen["limiters"] = true
	}
}

func (s *fa6State) anonLimit(rpm, burst string) error {
	s.setenv("ROGERAI_ANON_RATE_RPM", rpm)
	s.setenv("ROGERAI_ANON_RATE_BURST", burst)
	s.b.rl = loadRateLimiter()
	s.b.anonRL = loadAnonRateLimiter()
	s.scen["limiters"] = true
	return nil
}

func (s *fa6State) freeStationFor(model string) *fstation {
	if st, ok := s.stations["n-free"]; ok {
		return st
	}
	s.b.seedFunds = 0.5
	return s.node("n-free", model, 0, 0)
}

func (s *fa6State) unboundFresh(n string) error {
	s.realLimiters()
	s.freeStationFor(s.lastModel)
	s.snapshot()
	ip := s.nextIP()
	for i := 0; i < atoiMust(n); i++ {
		w := s.unboundKey(ip)
		s.do(fa6Spec{priv: w.priv, ip: ip, model: s.lastModel})
	}
	return nil
}

func (s *fa6State) admitted() (int, int) {
	ok, limited := 0, 0
	for _, r := range s.resps {
		if r.code == 429 {
			limited++
		} else {
			ok++
		}
	}
	return ok, limited
}

func (s *fa6State) atMostAdmitted(n string) error {
	ok, _ := s.admitted()
	if ok > atoiMust(n) {
		return fmt.Errorf("%d of %d requests were admitted, want at most %s", ok, len(s.resps), n)
	}
	return nil
}

func (s *fa6State) rest429(msg string) error {
	for _, r := range s.resps {
		if r.code == 429 && !strings.Contains(string(r.body), msg) {
			return fmt.Errorf("a 429 said %.200s, want %q", r.body, msg)
		}
	}
	return nil
}

func (s *fa6State) keypairBound(who string) error { s.consumer(who); return nil }

func (s *fa6State) sendsFromOneIP(who, n string) error {
	s.realLimiters()
	s.freeStationFor(s.lastModel)
	_ = s.fundWho(who, 0)
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: s.lastModel})
	}
	return nil
}

func (s *fa6State) drawsOnAccountBucket(who string) error {
	for i, r := range s.resps {
		if r.code == 429 {
			return fmt.Errorf("request %d of %s was rate-limited: %.200s", i+1, who, r.body)
		}
	}
	return nil
}

func (s *fa6State) behindOneIP(a, b string) error {
	ip := s.nextIP()
	s.consumer(a).ip = ip
	s.consumer(b).ip = ip
	return nil
}

func (s *fa6State) eachSends(n string) error {
	s.realLimiters()
	s.freeStationFor(s.lastModel)
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		for _, who := range []string{"alice", "bob"} {
			s.do(fa6Spec{who: who, model: s.lastModel})
		}
	}
	return nil
}

func (s *fa6State) neitherLimited() error {
	for i, r := range s.resps {
		if r.code == 429 {
			return fmt.Errorf("request %d (%s) was rate-limited: %.200s", i+1, r.who, r.body)
		}
	}
	return nil
}

func (s *fa6State) freePerIP(rpm string) error {
	s.setenv("ROGERAI_FREE_RATE_RPM", rpm)
	return nil
}

func (s *fa6State) nodeFreeFor(name, model string) error {
	s.lastModel = model
	s.b.seedFunds = 0.5
	s.node(name, model, 0, 0)
	return nil
}

func (s *fa6State) oneIPSends(n, model string) error {
	s.realLimiters()
	s.snapshot()
	ip := s.nextIP()
	w := s.unboundKey(ip)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{priv: w.priv, ip: ip, model: model})
	}
	return nil
}

func (s *fa6State) dispatchedAtMostRest(n, code string) error {
	dispatched := 0
	for name := range s.stations {
		dispatched += s.sinceMark(name)
	}
	if dispatched > atoiMust(n) {
		return fmt.Errorf("%d requests were dispatched, want at most %s", dispatched, n)
	}
	for _, r := range s.resps {
		if r.code != 200 && (r.code != 429 || s.errCode(r) != code) {
			return fmt.Errorf("a refused request answered %d code %q, want 429 %q: %.200s", r.code, s.errCode(r), code, r.body)
		}
	}
	return nil
}

func (s *fa6State) freePerIPStation(rpm string) error {
	s.setenv("ROGERAI_FREE_STATION_RPM", rpm)
	return nil
}

func (s *fa6State) twoFree(a, b, model string) error {
	s.lastModel = model
	s.b.seedFunds = 0.5
	s.node(a, model, 0, 0)
	s.node(b, model, 0, 0)
	return nil
}

func (s *fa6State) oneIPPinned(n, name string) error {
	s.realLimiters()
	s.snapshot()
	st := s.ensureNode(name)
	ip := s.nextIP()
	w := s.unboundKey(ip)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{priv: w.priv, ip: ip, model: st.model, extra: map[string]any{"provider": fa6Order(st.id)}})
	}
	return nil
}

func (s *fa6State) atMostReach(n, name string) error {
	if got := s.sinceMark(name); got > atoiMust(n) {
		return fmt.Errorf("%d requests reached %q, want at most %s", got, name, n)
	}
	return nil
}

func (s *fa6State) freePinCap(n string) error {
	s.setenv("ROGERAI_FREE_PIN_RPM", n)
	return nil
}

func (s *fa6State) sendsPinnedWithin(who, n, name string) error {
	s.realLimiters()
	s.snapshot()
	st := s.ensureNode(name)
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": fa6Order(st.id)}})
	}
	return nil
}

func (s *fa6State) rest429Code(code string) error {
	for _, r := range s.resps {
		if r.code != 200 && (r.code != 429 || s.errCode(r) != code) {
			return fmt.Errorf("a refused request answered %d code %q, want 429 %q: %.200s", r.code, s.errCode(r), code, r.body)
		}
	}
	return nil
}

func (s *fa6State) sendsPaid(who, n string) error {
	s.realLimiters()
	st := s.node("p-1", s.lastModel, 0.10, 0.30)
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: st.model})
	}
	return nil
}

func (s *fa6State) noneRefusedFor(code string) error {
	for i, r := range s.resps {
		if s.errCode(r) == code {
			return fmt.Errorf("request %d was refused %q", i+1, code)
		}
	}
	return nil
}

func (s *fa6State) noneRefusedFor2(a, b string) error {
	if err := s.noneRefusedFor(a); err != nil {
		return err
	}
	return s.noneRefusedFor(b)
}

func (s *fa6State) ownsNode(who, name string) error {
	w := s.consumer(who)
	st := s.standUp(name, stationOpts{model: s.lastModel, priceIn: 0.10, priceOut: 0.30, ctx: 131072, ownerPriv: w.priv})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.real(st)
	_, _ = s.db.BalanceOf(w.wallet, s.b.seedFunds)
	return nil
}

func (s *fa6State) sendsToOwn(who, n string) error {
	s.realLimiters()
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{who: who, model: s.lastModel})
	}
	return nil
}

func (s *fa6State) freeGrantRPM(rpm string) error {
	owner := s.node("g-1", s.lastModel, 0.10, 0.30)
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	id := "grant_" + s.nonce
	s.scen["grant"] = secret
	_, _ = s.db.BalanceOf("g_"+id, s.b.seedFunds)
	return s.db.CreateGrant(store.Grant{ID: id, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct, Label: "free", Free: true, RPM: atofMust(rpm), Burst: atofMust(rpm), DailyCap: 1_000_000, CreatedAt: time.Now().Unix()})
}

func (s *fa6State) grantHolderSends(n string) error {
	s.realLimiters()
	s.snapshot()
	tok, _ := s.scen["grant"].(string)
	ip := s.nextIP()
	for i := 0; i < atoiMust(n); i++ {
		s.do(fa6Spec{grant: tok, ip: ip, model: s.lastModel})
	}
	return nil
}

func (s *fa6State) freeToEachInstance(n string) error {
	s.realLimiters()
	st := s.freeStationFor(s.lastModel)
	s.instanceB() // (re)mirror the stations onto instance B now that the free station exists
	s.b2.rl, s.b2.anonRL = loadRateLimiter(), loadAnonRateLimiter()
	s.snapshot()
	ip := s.nextIP()
	w := s.unboundKey(ip)
	for _, b := range []*broker{s.b, s.b2} {
		for i := 0; i < atoiMust(n); i++ {
			s.do(fa6Spec{priv: w.priv, ip: ip, model: st.model, b: b})
		}
	}
	return nil
}

func (s *fa6State) freeLimitTogether(n string) error {
	limit := 20
	if v := os.Getenv("ROGERAI_FREE_RATE_RPM"); v != "" {
		limit = atoiMust(v)
	}
	dispatched := 0
	for name := range s.stations {
		dispatched += s.sinceMark(name)
	}
	if dispatched > limit {
		return fmt.Errorf("%d of the %s free requests across both instances were dispatched, want at most the per-IP limit %d", dispatched, n, limit)
	}
	return nil
}

// --- sort band -------------------------------------------------------------------------------------

// pricedForCost sets in = out = price so the relative estimated request costs match the
// scenario's figures (every relay sends the same prompt and output budget).
func (s *fa6State) pricedStations(names []string, costs []float64) {
	for i, n := range names {
		p := costs[i] * 100 // $0.0100 -> $1.00/1M, preserving the ratios
		s.node(n, s.lastModel, p, p)
	}
}

func (s *fa6State) threeAtCosts(a, b, c, model, ca, cb, cc string) error {
	s.lastModel = model
	s.pricedStations([]string{a, b, c}, []float64{atofMust(ca), atofMust(cb), atofMust(cc)})
	return nil
}

func (s *fa6State) equalSpare() error {
	for _, st := range s.stations {
		s.setCapacity(st, 4, 0)
	}
	return nil
}

func (s *fa6State) consumersSort(n, sortBy string) error {
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("c%d", i%20)
		if i < 20 {
			_ = s.fundWho(who, 50)
		}
		s.heartbeat()
		s.do(fa6Spec{who: who, model: s.lastModel, extra: map[string]any{"provider": map[string]any{"sort": sortBy}}})
	}
	return nil
}

func (s *fa6State) eachServeBetween(a, b, lo, hi string) error {
	sh, total := s.shares()
	for _, n := range []string{a, b} {
		p := fa6Pct(sh[n], total)
		if p < atofMust(lo) || p > atofMust(hi) {
			return fmt.Errorf("%q served %.1f%% (%d of %d), want %s%%-%s%%; shares %v", n, p, sh[n], total, lo, hi, sh)
		}
	}
	return nil
}

func (s *fa6State) servesSomeNone(name, what string) error {
	sh, total := s.shares()
	switch what {
	case "serves none":
		if sh[name] != 0 {
			return fmt.Errorf("%q served %d of %d, want none; shares %v", name, sh[name], total, sh)
		}
	case "serves some":
		if sh[name] == 0 {
			return fmt.Errorf("%q served none of %d, want some; shares %v", name, total, sh)
		}
	}
	return nil
}

func (s *fa6State) twoAtCosts(a, b, model, ca, cb string) error {
	s.lastModel = model
	s.pricedStations([]string{a, b}, []float64{atofMust(ca), atofMust(cb)})
	return nil
}

func (s *fa6State) bothServe(a, b string) error {
	sh, total := s.shares()
	if sh[a] == 0 || sh[b] == 0 {
		return fmt.Errorf("shares %v of %d, want both %q and %q to serve", sh, total, a, b)
	}
	return nil
}

func (s *fa6State) withinBand(a, b string) error {
	s.pricedStations([]string{a, b}, []float64{0.0100, 0.0102})
	return nil
}

func (s *fa6State) slotsBusy(a, ab, ac, b, bb, bc string) error {
	s.setCapacity(s.st(a), atoiMust(ac), atoiMust(ab))
	s.setCapacity(s.st(b), atoiMust(bc), atoiMust(bb))
	return nil
}

func (s *fa6State) servesMore(a, b string) error {
	sh, total := s.shares()
	if sh[a] <= sh[b] {
		return fmt.Errorf("%q served %d and %q %d of %d, want %q more", a, sh[a], b, sh[b], total, a)
	}
	return nil
}

func (s *fa6State) everySlotBusy(name string) error {
	s.setCapacity(s.st(name), 4, 4)
	return nil
}

func (s *fa6State) consumerSort(sortBy string) error {
	s.snapshot()
	s.do(fa6Spec{who: "alice", model: s.lastModel, extra: map[string]any{"provider": map[string]any{"sort": sortBy}}})
	return nil
}

func (s *fa6State) servedByName(name string) error { return s.servedBy200(name) }

func (s *fa6State) identicalStations(a, b, model string) error {
	s.lastModel = model
	for _, n := range []string{a, b} {
		st := s.node(n, model, 1, 1)
		s.setCapacity(st, 4, 0)
		s.setTPS(st.id, 50)
	}
	return nil
}

func (s *fa6State) eachBetween(lo, hi string) error {
	sh, total := s.shares()
	for _, n := range s.order {
		p := fa6Pct(sh[n], total)
		if p < atofMust(lo) || p > atofMust(hi) {
			return fmt.Errorf("%q served %.1f%% of %d, want %s%%-%s%%; shares %v", n, p, total, lo, hi, sh)
		}
	}
	return nil
}

// sameRequestSame: the request id is minted by the broker and seeds the pick; the observable
// claim is that a pick under a given seed is reproducible, read through the production pickFor
// with the production seed derivation for one id, twice.
func (s *fa6State) sameRequestSame() error {
	id := "fa6-" + s.nonce
	pick := func() string {
		s.b.mu.Lock()
		defer s.b.mu.Unlock()
		n, _, ok := s.b.pickFor(s.lastModel, false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(id), sort: sortPrice})
		if !ok {
			return ""
		}
		return n.NodeID
	}
	a, b := pick(), pick()
	if a == "" || a != b {
		return fmt.Errorf("the same request seed picked %q then %q", a, b)
	}
	return nil
}

func (s *fa6State) threeTPS(a, b, c, ta, tb, tc, model string) error {
	s.lastModel = model
	for i, n := range []string{a, b, c} {
		st := s.node(n, model, 1, 1)
		s.setTPS(st.id, float64(atoiMust([]string{ta, tb, tc}[i])))
	}
	return nil
}

func (s *fa6State) shareAndNone(a, b, c string) error {
	sh, total := s.shares()
	if sh[a] == 0 || sh[b] == 0 || sh[c] != 0 {
		return fmt.Errorf("shares %v of %d, want %q and %q to share and %q none", sh, total, a, b, c)
	}
	return nil
}

func (s *fa6State) threeTTFT(a, b, c, ta, tb, tc string) error {
	for i, n := range []string{a, b, c} {
		st := s.node(n, s.lastModel, 1, 1)
		s.setTTFT(st, atoiMust([]string{ta, tb, tc}[i]))
	}
	return nil
}

func (s *fa6State) measuredAndUnmeasured(a, ta, b string) error {
	sa := s.node(a, s.lastModel, 1, 1)
	s.setTPS(sa.id, float64(atoiMust(ta)))
	sb := s.node(b, s.lastModel, 1, 1)
	s.b.metricsMu.Lock()
	delete(s.b.tps, sb.id)
	s.b.metricsMu.Unlock()
	return nil
}

func (s *fa6State) fourAtCosts(a, b, c, d, ca, cb, cc, cd string) error {
	s.pricedStations([]string{a, b, c, d}, []float64{atofMust(ca), atofMust(cb), atofMust(cc), atofMust(cd)})
	return nil
}

func (s *fa6State) everyNext429() error {
	for _, st := range s.stations {
		s.answerFrom(st, false, 429, utDefaultBody(429), nil)
	}
	return nil
}

func (s *fa6State) attemptsBandThen(c, d string) error {
	s.hitMu.Lock()
	hits := append([]string(nil), s.hits...)
	s.hitMu.Unlock()
	if len(hits) < 3 {
		return fmt.Errorf("attempts %v, want the two band stations then %q", hits, c)
	}
	band := map[string]bool{hits[0]: true, hits[1]: true}
	if !band["s1"] || !band["s2"] || hits[2] != c {
		return fmt.Errorf("attempts %v, want s1 and s2 (band, seeded order) then %q", hits, c)
	}
	if len(hits) > 3 && hits[3] != d {
		return fmt.Errorf("attempts %v, want %q fourth", hits, d)
	}
	return nil
}

func (s *fa6State) sortNoFallback(sortBy string) error {
	s.snapshot()
	s.do(fa6Spec{who: "alice", model: s.lastModel, extra: map[string]any{"provider": map[string]any{"sort": sortBy, "allow_fallbacks": false}}})
	return nil
}

func (s *fa6State) oneAttemptTo(a, b string) error {
	if err := s.exactlyOneAttempt(); err != nil {
		return err
	}
	if s.sinceMark(a)+s.sinceMark(b) != 1 {
		return fmt.Errorf("the one attempt went to neither %q nor %q", a, b)
	}
	return nil
}

func (s *fa6State) sortBand(price, speed string) error {
	s.setenv("ROGERAI_SORT_BAND_PRICE", price)
	s.setenv("ROGERAI_SORT_BAND_SPEED", speed)
	return nil
}

func (s *fa6State) differBy(a, b, diff, metric string) error {
	d := atofMust(diff) / 100
	switch metric {
	case "price":
		s.node(a, s.lastModel, 1, 1)
		s.node(b, s.lastModel, 1+d, 1+d)
	case "tps":
		sa := s.node(a, s.lastModel, 1, 1)
		sb := s.node(b, s.lastModel, 1, 1)
		s.setTPS(sa.id, 100)
		s.setTPS(sb.id, 100*(1-d))
	}
	return nil
}

func (s *fa6State) consumersFor(n, model string) error {
	s.snapshot()
	s.lastModel = model
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("c%d", i%20)
		if i < 20 {
			_ = s.fundWho(who, 50)
		}
		s.heartbeat()
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

func (s *fa6State) bothServeAny() error {
	sh, total := s.shares()
	n := 0
	for _, c := range sh {
		if c > 0 {
			n++
		}
	}
	if n < 2 {
		return fmt.Errorf("shares %v of %d, want both stations to serve", sh, total)
	}
	return nil
}

func (s *fa6State) declaresCapacityMeasured(name, declared, measured string) error {
	st := s.node(name, s.lastModel, 1, 1)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.HW = "declared-capacity-" + declared
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[st.id] = float64(atoiMust(measured)) * tpsPerSlot
	s.b.metricsMu.Unlock()
	return nil
}

func (s *fa6State) declaresCapacity(name, declared string) error {
	st := s.node(name, s.lastModel, 1.01, 1.01)
	s.setCapacity(st, atoiMust(declared), 0)
	return nil
}

func (s *fa6State) consumersSortBand(n, sortBy string) error { return s.consumersSort(n, sortBy) }

func (s *fa6State) weightFromMeasured(name string) error {
	sh, total := s.shares()
	other := ""
	for n := range s.stations {
		if n != name {
			other = n
		}
	}
	if sh[name] > sh[other] {
		return fmt.Errorf("%q (measured concurrency 1) served %d of %d against %q's %d: its weight followed its declared capacity", name, sh[name], total, other, sh[other])
	}
	return nil
}

// --- Tower coin -----------------------------------------------------------------------------------

// edgePool returns funded, GitHub-bound buyers for bridged relays (one account holds at most 32
// open edge attempts, and the harness never settles a bridged hold).
func (s *fa6State) edgeBuyer(i int) *fa6Who {
	for len(s.towerPool) < 60 {
		k := len(s.towerPool)
		w := s.consumer(fmt.Sprintf("edge-%d", k))
		_, _ = s.db.AddCredits(w.wallet, 400)
		s.towerPool = append(s.towerPool, w)
	}
	return s.towerPool[i%len(s.towerPool)]
}

func (s *fa6State) directsAndTower(n, dcap, tcap, model string) error {
	s.lastModel, s.model = model, model
	for i := 0; i < atoiMust(n); i++ {
		st := s.node(fmt.Sprintf("d%d", i+1), model, 1, 1)
		s.setCapacity(st, atoiMust(dcap), 0)
	}
	tw, err := s.standUpTower("t1", model, 1, 1, 50)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[tw.nodeID] = float64(atoiMust(tcap)) * tpsPerSlot
	s.b.metricsMu.Unlock()
	return nil
}

func (s *fa6State) everyTierA() error {
	s.b.mu.Lock()
	for _, st := range s.stations {
		s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	}
	for _, tw := range s.towers {
		s.b.trust[tw.nodeID] = trustState{probed: true, probeOK: true, ttftMs: 200}
	}
	s.b.mu.Unlock()
	return nil
}

func (s *fa6State) consumersForEdge(n, model string) error {
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		w := s.edgeBuyer(i)
		if i%50 == 0 {
			s.heartbeat()
		}
		s.do(fa6Spec{who: w.name, model: model})
	}
	return nil
}

func (s *fa6State) fabricShares() (direct, tower, total int) {
	for _, r := range s.resps {
		if r.code != 200 {
			continue
		}
		total++
		if r.hdr.Get("X-RogerAI-Relay") != "" {
			tower++
		} else {
			direct++
		}
	}
	return
}

func (s *fa6State) towerServesBetween(lo, hi string) error {
	_, tower, total := s.fabricShares()
	p := fa6Pct(tower, total)
	if p < atofMust(lo) || p > atofMust(hi) {
		return fmt.Errorf("the Tower served %.1f%% (%d of %d), want %s%%-%s%%", p, tower, total, lo, hi)
	}
	return nil
}

func (s *fa6State) capacityEachSide(dcap, tcap, model string) error {
	s.lastModel, s.model = model, model
	st := s.node("d1", model, 1, 1)
	s.setCapacity(st, atoiMust(dcap), 0)
	tw, err := s.standUpTower("t1", model, 1, 1, 50)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[tw.nodeID] = float64(atoiMust(tcap)) * tpsPerSlot
	s.b.metricsMu.Unlock()
	return s.everyTierA()
}

func (s *fa6State) eachFabricBetween(lo, hi string) error {
	direct, tower, total := s.fabricShares()
	for name, c := range map[string]int{"direct": direct, "tower": tower} {
		p := fa6Pct(c, total)
		if p < atofMust(lo) || p > atofMust(hi) {
			return fmt.Errorf("the %s fabric served %.1f%% (%d of %d), want %s%%-%s%%", name, p, c, total, lo, hi)
		}
	}
	return nil
}

func (s *fa6State) directsExcluded(capacity, excluded string) error {
	model := s.lastModel
	if model == "qwen3-32b" {
		model = "m"
	}
	s.lastModel, s.model = model, model
	n := atoiMust(capacity) // capacity-1 stations; `excluded` of them carry a quant the filter refuses
	for i := 0; i < n; i++ {
		st := s.node(fmt.Sprintf("d%d", i+1), model, 1, 1)
		s.setCapacity(st, 1, 0)
		q := "Q8_0"
		if i < atoiMust(excluded) {
			q = "Q4_K_M"
		}
		_ = s.setOfferField(st, "Quant", q)
	}
	s.scen["filters"] = map[string]any{"provider": map[string]any{"quantizations": []string{"Q8_0"}}}
	return nil
}

func (s *fa6State) towerCapEligible(tcap string) error {
	tw, err := s.standUpTower("t1", s.lastModel, 1, 1, 50)
	if err != nil {
		return err
	}
	// "eligible": the node behind the row declares the quant the request's filter admits.
	s.b.mu.Lock()
	reg, ok := s.b.nodes[tw.nodeID]
	if ok {
		found := false
		for i := range reg.Offers {
			if reg.Offers[i].Model == s.lastModel {
				reg.Offers[i].Quant, found = "Q8_0", true
			}
		}
		if !found { // the share node registered no offer row of its own: declare the one it serves
			reg.Offers = append(reg.Offers, protocol.ModelOffer{Model: s.lastModel, Quant: "Q8_0", Ctx: 131072})
		}
		s.b.nodes[tw.nodeID] = reg
	}
	s.b.mu.Unlock()
	if !ok {
		return fmt.Errorf("the Tower row's node %s has no registration to declare a quant on", tw.nodeID)
	}
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[tw.nodeID] = float64(atoiMust(tcap)) * tpsPerSlot
	s.b.metricsMu.Unlock()
	return s.everyTierA()
}

func (s *fa6State) consumersWithFilters(n, model string) error {
	s.snapshot()
	f, _ := s.scen["filters"].(map[string]any)
	for i := 0; i < atoiMust(n); i++ {
		w := s.edgeBuyer(i)
		if i%50 == 0 {
			s.heartbeat()
		}
		s.do(fa6Spec{who: w.name, model: model, extra: f})
	}
	return nil
}

func (s *fa6State) capacityFor(dcap, tcap, model string) error {
	return s.capacityEachSide(dcap, tcap, model)
}

// sameIDTwice cannot be built at the wire: the broker mints the request id that seeds the coin,
// and a client cannot replay one (an Idempotency-Key replay returns the stored outcome without
// a second routing pass). Reported as unconstructable.
func (s *fa6State) sameIDTwice() error {
	return fmt.Errorf("cannot construct: the request id that seeds the coin is minted by the broker; a client cannot relay the same id twice")
}

func (s *fa6State) sameFabricFirst() error {
	return fmt.Errorf("cannot observe: see the When (the request id is not client-settable)")
}

func (s *fa6State) directCheaper() error {
	for _, st := range s.stations {
		s.b.mu.Lock()
		reg := s.b.nodes[st.id]
		for i := range reg.Offers {
			reg.Offers[i].PriceIn, reg.Offers[i].PriceOut = 0.10, 0.10
		}
		s.b.nodes[st.id] = reg
		s.b.mu.Unlock()
	}
	return nil
}

func (s *fa6State) directTriedFirst() error {
	if s.last.code == 200 && s.last.hdr.Get("X-RogerAI-Relay") == "" {
		return nil
	}
	return fmt.Errorf("the head was not the direct station: %d relay=%q %.200s", s.last.code, s.last.hdr.Get("X-RogerAI-Relay"), s.last.body)
}

func (s *fa6State) consumerSortPriceEdge(sortBy string) error {
	s.snapshot()
	w := s.edgeBuyer(0)
	s.do(fa6Spec{who: w.name, model: s.lastModel, extra: map[string]any{"provider": map[string]any{"sort": sortBy}}})
	return nil
}

func (s *fa6State) ownsDirectAndTower(who, model, _ string) error {
	s.lastModel, s.model = model, model
	w := s.consumer(who)
	_, _ = s.db.AddCredits(w.wallet, 400)
	st := s.standUp("mine", stationOpts{model: model, priceIn: 1, priceOut: 1, ctx: 131072, ownerPriv: w.priv})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.real(st)
	_, err := s.standUpTower("t1", model, 1, 1, 50)
	return err
}

func (s *fa6State) relaysFor(who, model string) error {
	s.snapshot()
	for i := 0; i < 24; i++ {
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

func (s *fa6State) towerNeverTried() error {
	for _, r := range s.resps {
		if r.hdr.Get("X-RogerAI-Relay") != "" {
			return fmt.Errorf("a relay rode the Tower %q", r.hdr.Get("X-RogerAI-Relay"))
		}
	}
	return nil
}

func (s *fa6State) towerStationCap(model, capacity string) error {
	s.lastModel, s.model = model, model
	tw, err := s.standUpTower("t1", model, 1, 1, 50)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[tw.nodeID] = float64(atoiMust(capacity)) * tpsPerSlot
	s.b.metricsMu.Unlock()
	return nil
}

func (s *fa6State) oneDirectCap(capacity string) error {
	st := s.node("d1", s.lastModel, 1, 1)
	s.setCapacity(st, atoiMust(capacity), 0)
	return s.everyTierA()
}

// --- home first -----------------------------------------------------------------------------------

func (s *fa6State) humanAndCurated(h, c, model string) error {
	s.lastModel = model
	s.defaultNode(h, model)
	s.curate(s.defaultNode(c, model), "openrouter")
	return nil
}

func (s *fa6State) fasterCheaper(name string) error {
	st := s.st(name)
	s.setTPS(st.id, 300)
	s.setTTFT(st, 50)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Offers[0].PriceIn, reg.Offers[0].PriceOut = 0.05, 0.10
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *fa6State) consumersNoRouting(n, model string) error {
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("c%d", i%20)
		if i < 20 {
			_ = s.fundWho(who, 50)
		}
		s.heartbeat()
		s.do(fa6Spec{who: who, model: model})
	}
	return nil
}

func (s *fa6State) everyServedBy(name string) error {
	sh, total := s.shares()
	if total == 0 || sh[name] != total {
		return fmt.Errorf("%q served %d of %d, want every request; shares %v", name, sh[name], total, sh)
	}
	return nil
}

func (s *fa6State) humanBusyAndCurated(h, c, model string) error {
	if err := s.humanAndCurated(h, c, model); err != nil {
		return err
	}
	s.setCapacity(s.st(h), 1, 1)
	return nil
}

func (s *fa6State) consumerRelaysTo(model string) error {
	s.snapshot()
	s.lastModel = model
	s.do(fa6Spec{who: "alice", model: model})
	return nil
}

func (s *fa6State) twoHumansOneCurated(h1, h2, c, model string) error {
	s.lastModel = model
	s.defaultNode(h1, model)
	s.defaultNode(h2, model)
	s.curate(s.defaultNode(c, model), "openrouter")
	return nil
}

func (s *fa6State) bothAnswer429(a, b string) error {
	s.answerFrom(s.st(a), false, 429, utDefaultBody(429), nil)
	s.answerFrom(s.st(b), false, 429, utDefaultBody(429), nil)
	return nil
}

func (s *fa6State) attemptsAre(a, b, c string) error {
	s.hitMu.Lock()
	hits := append([]string(nil), s.hits...)
	s.hitMu.Unlock()
	want := []string{a, b, c}
	if len(hits) < 3 {
		return fmt.Errorf("attempts %v, want %v", hits, want)
	}
	if !(map[string]bool{hits[0]: true, hits[1]: true}[a] && map[string]bool{hits[0]: true, hits[1]: true}[b]) || hits[2] != c {
		return fmt.Errorf("attempts %v, want %v (the two home stations first)", hits, want)
	}
	return nil
}

func (s *fa6State) curatedOnly(c, model string) error {
	s.lastModel = model
	s.curate(s.defaultNode(c, model), "openrouter")
	return nil
}

func (s *fa6State) humanAndFasterCurated(h, c, model string) error {
	if err := s.humanAndCurated(h, c, model); err != nil {
		return err
	}
	st := s.st(c)
	s.setTPS(st.id, 300)
	s.setTTFT(st, 50)
	return nil
}

var fa6OptinRe = regexp.MustCompile(`^(roger\.pref|provider\.sort) "([^"]+)"$|^provider\.order \["([^"]+)"\]$|^provider\.only \["([^"]+)", "([^"]+)"\]$|^the X-Roger-Node "([^"]+)" pin$`)

func (s *fa6State) optin(text string) (fa6Spec, error) {
	sp := fa6Spec{who: "alice", model: s.lastModel}
	m := fa6OptinRe.FindStringSubmatch(text)
	if m == nil {
		return sp, fmt.Errorf("unknown opt-in %q", text)
	}
	switch {
	case m[1] == "roger.pref":
		sp.extra = map[string]any{"roger": map[string]any{"pref": m[2]}}
	case m[1] == "provider.sort":
		sp.extra = map[string]any{"provider": map[string]any{"sort": m[2]}}
	case m[3] != "":
		sp.extra = map[string]any{"provider": fa6Order(s.st(m[3]).id)}
	case m[4] != "":
		sp.extra = map[string]any{"provider": map[string]any{"only": []string{s.st(m[4]).id, s.st(m[5]).id}}}
	case m[6] != "":
		sp.hdr = map[string]string{"X-Roger-Node": s.st(m[6]).id}
	}
	return sp, nil
}

func (s *fa6State) consumerRelaysWith(model, text string) error {
	s.lastModel = model
	sp, err := s.optin(text)
	if err != nil {
		return err
	}
	s.snapshot()
	s.scen["optin"] = sp
	n := 40
	for i := 0; i < n; i++ {
		who := fmt.Sprintf("c%d", i%20)
		if i < 20 {
			_ = s.fundWho(who, 50)
		}
		sp.who = who
		s.heartbeat()
		s.do(sp)
	}
	return nil
}

// competesEqual: with the opt-in, the faster curated station is not pushed behind the home
// station - it serves at least some of the batch (named / pinned forms: all of it).
func (s *fa6State) competesEqual(c, h string) error {
	sh, total := s.shares()
	if sh[c] == 0 {
		return fmt.Errorf("%q served none of %d under the opt-in (shares %v): it did not compete with %q", c, total, sh, h)
	}
	return nil
}

func (s *fa6State) consumerOrder(name string) error {
	s.snapshot()
	s.do(fa6Spec{who: "alice", model: s.lastModel, extra: map[string]any{"provider": fa6Order(s.st(name).id)}})
	return nil
}

func (s *fa6State) consumerSelfHosted() error {
	s.snapshot()
	s.do(fa6Spec{who: "alice", model: s.lastModel, extra: map[string]any{"roger": map[string]any{"self_hosted_only": true}}})
	return nil
}

func (s *fa6State) notServedBy(name string) error {
	if s.last.code == 200 && s.served(s.last) == name {
		return fmt.Errorf("served by %q", name)
	}
	return nil
}

func (s *fa6State) humansCoolingCuratedOn(model, c string) error {
	s.lastModel = model
	h := s.defaultNode("n-home", model)
	s.curate(s.defaultNode(c, model), "openrouter")
	s.answerFrom(h, false, 429, utDefaultBody(429), map[string]string{"Retry-After": "60"})
	for _, who := range []string{"alice", "bob", "carol"} {
		s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": map[string]any{"only": []string{h.id}}}})
		s.answerFrom(h, false, 429, utDefaultBody(429), map[string]string{"Retry-After": "60"})
	}
	if !s.isCooling("n-home") {
		return fmt.Errorf("could not cool the human station for every payer")
	}
	return nil
}

func (s *fa6State) curatedTowerAndHuman(model string) error {
	s.lastModel, s.model = model, model
	h := s.node("n-human", model, 1, 1)
	_ = h
	tw, err := s.standUpTower("t1", model, 0.5, 0.5, 300)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	reg := s.b.nodes[tw.nodeID]
	reg.Curated, reg.CuratedProvider = true, "openrouter"
	s.b.nodes[tw.nodeID] = reg
	s.b.mu.Unlock()
	return s.everyTierA()
}

func (s *fa6State) humanDirectEvery() error {
	sh, total := s.shares()
	if total == 0 || sh["n-human"] != total {
		return fmt.Errorf("the human direct station served %d of %d, want every request; shares %v", sh["n-human"], total, sh)
	}
	return nil
}

func (s *fa6State) consumersRelayToEdge(n, model string) error { return s.consumersForEdge(n, model) }

func (s *fa6State) twoCuratedOnly(a, b, model string) error {
	s.lastModel = model
	s.curate(s.defaultNode(a, model), "openrouter")
	s.curate(s.defaultNode(b, model), "conifer")
	s.setTPS(s.st(a).id, 80)
	s.setTPS(s.st(b).id, 40)
	return nil
}

// ordinaryScore: between two curated stations the batch is split by the ordinary scored
// selection - both serve (P2C over scores), the better-scored one at least as often.
func (s *fa6State) ordinaryScore() error {
	sh, total := s.shares()
	if total == 0 {
		return fmt.Errorf("no request was served")
	}
	if sh["cur-1"] < sh["cur-2"] {
		return fmt.Errorf("the faster curated station served %d and the slower %d of %d: not the ordinary score", sh["cur-1"], sh["cur-2"], total)
	}
	return nil
}

// --- runner ---------------------------------------------------------------------------------------

func TestFairnessAbuseBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &fa6State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			fa6Register(sc, st)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/routing/fairness_and_abuse.feature"},
			Tags: "~@cli && ~@tui && ~@proxy && ~@docs && ~@later", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("fairness_and_abuse.feature failed")
	}
}

func fa6Register(sc *godog.ScenarioContext, st *fa6State) {
	// Background
	sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
	sc.Step(`^the fee rate is 30%$`, st.feeRate30)
	sc.Step(`^the strike warn threshold is (\d+) and the ban threshold is (\d+)$`, st.strikeThresholds)
	sc.Step(`^the empty-output distinct-payer floor is (\d+)$`, st.strikeFloor)
	sc.Step(`^the station cooldown payer threshold is (\d+) within (\d+) seconds$`, st.cooldownThreshold)
	sc.Step(`^funded consumers "([^"]+)", "([^"]+)", "([^"]+)" and "([^"]+)" each hold \$([0-9.]+) in distinct wallets$`, st.fourConsumers)

	// #1 consumer-caused 4xx
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" is on air for "([^"]+)"$`, st.nodeOwnedOnAir)
	sc.Step(`^node "([^"]+)" is on air for "([^"]+)"$`, st.nodeOnAir)
	sc.Step(`^"([^"]+)" answers the next request with (\d{3}) (.+)$`, st.answersNextRaw)
	sc.Step(`^"([^"]+)" relays to "([^"]+)" with provider\.order \["([^"]+)"\]$`, st.relaysPinned)
	sc.Step(`^the response status is (\d+)$`, st.statusIs)
	sc.Step(`^"([^"]+)" received nothing$`, st.receivedNothing)
	sc.Step(`^the attempt's receipt is voided with void_reason "([^"]+)" and upstream_status (\d+)$`, st.attemptVoided)
	sc.Step(`^"([^"]+)" was charged ([0-9.]+)$`, st.charged)
	sc.Step(`^no owner_strikes row exists for "([^"]+)"$`, st.noStrikes)
	sc.Step(`^"([^"]+)"'s payouts are (not held|held)$`, st.payouts)
	sc.Step(`^node "([^"]+)" is on air for "([^"]+)" and node "([^"]+)" for "([^"]+)"$`, st.twoNodesTwoModels)
	sc.Step(`^"([^"]+)" answers with (\d{3}) "([^"]*)"$`, st.answersNextMsg)
	sc.Step(`^"([^"]+)" answers with (\d{3}) (\{.*\})$`, st.answersNextRaw)
	sc.Step(`^"([^"]+)" answers with 429 and Retry-After (\d+)$`, st.answers429RA)
	sc.Step(`^"([^"]+)" relays with "model": "([^"]+)" and "models": \["([^"]+)"\]$`, st.relaysModelList)
	sc.Step(`^the response is 200 served by "([^"]+)"$`, st.servedBy200)
	sc.Step(`^the "([^"]+)" attempt's void_reason is "([^"]+)", not "([^"]+)"$`, st.attemptVoidReasonNot)
	sc.Step(`^"([^"]+)" relays to "([^"]+)"$`, st.relaysTo)
	sc.Step(`^the attempt's void_reason is "([^"]+)"$`, st.attemptVoidReason)
	sc.Step(`^"([^"]+)" rejects the top-level key "([^"]+)" with (\d+)$`, st.rejectsTopKey)
	sc.Step(`^"([^"]+)" relays (\d+) times to "([^"]+)" with provider\.order \["([^"]+)"\], provider\.allow_fallbacks false and top-level "([^"]+)": (\d+)$`, st.relaysNTimesPinnedNoFallback)
	sc.Step(`^every response is (\d+)$`, st.everyResponse)
	sc.Step(`^"([^"]+)"'s success rate used by routing is unchanged$`, st.successUnchanged)
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" runs a server that rejects (.+) with (\d+)$`, st.serverRejectsKey)
	sc.Step(`^(\d+) different consumers relay to "([^"]+)" with (.+) set$`, st.differentConsumersWithKey)
	sc.Step(`^nodes "([^"]+)" and "([^"]+)" are on air for "([^"]+)"$`, st.twoNodesOnAir)
	sc.Step(`^exactly one attempt was dispatched$`, st.exactlyOneAttempt)
	sc.Step(`^the response body has error\.code "([^"]+)"$`, st.bodyErrCode)
	sc.Step(`^error\.metadata\.station is "([^"]+)"$`, st.metaStation)
	sc.Step(`^error\.metadata\.raw carries the station's body$`, st.metaRaw)
	sc.Step(`^"([^"]+)" answers every request with 429 and Retry-After (\d+)$`, st.answersEvery429RA)
	sc.Step(`^"([^"]+)" answers every request with (\d{3})$`, st.answersEvery)
	sc.Step(`^"([^"]+)" answers every request with (\d{3}) (\{.*\})$`, st.answersEveryBody)
	sc.Step(`^(\d+) distinct consumers each relay once to "([^"]+)" within the decay window$`, st.distinctConsumersEach)
	sc.Step(`^(\d+) owner_strikes rows of kind "([^"]+)" exist for "([^"]+)"$`, st.strikeRows)
	sc.Step(`^"([^"]+)" relays (\d+) times to "([^"]+)" with provider\.order \["([^"]+)"\] and allow_fallbacks false$`, st.relaysNPinnedNoFallback)
	sc.Step(`^the strike rows for "([^"]+)" all name the payer "([^"]+)"$`, st.strikesNamePayer)
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" is on air for "([^"]+)" and free$`, func(n, op, m string) error {
		s := st
		s.lastModel = m
		s.b.seedFunds = 0.5
		s.node(n, m, 0, 0)
		s.ops[op] = n
		return nil
	})
	sc.Step(`^(\d+) unbound keypairs from one client IP each relay once to "([^"]+)"$`, st.unboundOneIPEach)
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" claims more prompt tokens than the request has bytes$`, st.claimsImpossible)
	sc.Step(`^"([^"]+)" relays once to "([^"]+)"$`, st.relaysOnceTo)
	sc.Step(`^"([^"]+)" is banned as today$`, st.bannedAsToday)
	sc.Step(`^"([^"]+)"'s chain has a \$0 receipt for the attempt with void_reason "([^"]+)"$`, st.chainZeroReceipt)
	sc.Step(`^GET /generation for the request lists the attempt with error_code "([^"]+)"$`, st.generationAttemptCode)

	// #4 cooldowns
	sc.Step(`^"([^"]+)" answers "([^"]+)"'s next request with 429 and Retry-After (\d+)$`, st.answersPayerNext429)
	sc.Step(`^"([^"]+)" relays to "([^"]+)" (\d+) times$`, st.relaysNTimes)
	sc.Step(`^"([^"]+)" is a candidate for "([^"]+)"$`, st.isCandidateFor)
	sc.Step(`^"([^"]+)" is NOT a candidate for "([^"]+)" for the next (\d+) seconds$`, st.notCandidateForSecs)
	sc.Step(`^"([^"]+)", "([^"]+)" and "([^"]+)" each relay once to "([^"]+)" within (\d+) seconds$`, st.threePayersWithin)
	sc.Step(`^"([^"]+)" is cooling for every payer for (\d+) seconds$`, st.coolingForEveryone)
	sc.Step(`^"([^"]+)" is served by "([^"]+)"$`, st.servedByFor)
	sc.Step(`^"([^"]+)" and "([^"]+)" each relay once to "([^"]+)"$`, st.twoPayersEachOnce)
	sc.Step(`^node "([^"]+)" answers every request with 429$`, st.nodeEvery429)
	sc.Step(`^"([^"]+)" relays at t=0, "([^"]+)" at t=(\d+)s and "([^"]+)" at t=(\d+)s$`, st.relaysAtTimes)
	sc.Step(`^"([^"]+)" never cooled for every payer$`, st.neverCooledForEveryone)
	sc.Step(`^"([^"]+)" relays (\d+) times to "([^"]+)" with provider\.order \["([^"]+)"\]$`, st.relaysNPinned)
	sc.Step(`^"([^"]+)"'s pair cooldown on "([^"]+)" never exceeds the cooldown cap$`, st.pairCapNever)
	sc.Step(`^node "([^"]+)" is the only station for "([^"]+)"$`, st.onlyStation)
	sc.Step(`^"([^"]+)"'s pair on "([^"]+)" is cooling for (\d+) more seconds$`, st.pairCoolingFor)
	sc.Step(`^the response is 503 with error code "([^"]+)" and Retry-After (\d+)$`, st.resp503CodeRA)
	sc.Step(`^no attempt was dispatched$`, st.noAttempt)
	sc.Step(`^two broker instances share one store$`, st.twoInstances)
	sc.Step(`^the shared store is unreachable$`, func() error { s := st; s.mr.Close(); return nil })
	sc.Step(`^"([^"]+)" answered "([^"]+)" with 429 and Retry-After (\d+) on instance A$`, st.answeredOnA)
	sc.Step(`^"([^"]+)" relays to "([^"]+)" on instance B$`, st.relaysOnB)
	sc.Step(`^"([^"]+)" is NOT a candidate for "([^"]+)" on instance B$`, st.notCandidateOnB)
	sc.Step(`^"([^"]+)" answers "([^"]+)" with 429 and Retry-After (\d+)$`, st.answersPayerHuge)
	sc.Step(`^"([^"]+)"'s pair cooldown on "([^"]+)" is the cooldown cap$`, st.pairIsCap)
	sc.Step(`^"([^"]+)" answers "([^"]+)" again with Retry-After (\d+) before it expires$`, st.answersAgainBeforeExpiry)
	sc.Step(`^the pair cooldown keeps the later expiry$`, st.pairKeepsLater)
	sc.Step(`^node "([^"]+)" is free and on air for "([^"]+)"$`, st.freeOnAir)
	sc.Step(`^(\d+) unbound keypairs from one client IP each relay once$`, st.unboundOneIPEachNoModel)
	sc.Step(`^they count as one payer toward the station threshold$`, st.countAsOnePayer)
	sc.Step(`^(\d+) distinct consumers relay once each within (\d+) seconds$`, st.distinctRelayWithin)
	sc.Step(`^"([^"]+)" (is cooling|is NOT cooling) for every payer$`, st.coolingState)
	sc.Step(`^"([^"]+)" cooled for every payer (three|five) times in the alert window$`, st.cooledNTimes)
	sc.Step(`^"([^"]+)"'s trust state is unchanged$`, st.trustUnchanged)
	sc.Step(`^the founder is paged once, as the approved cooling alert says$`, st.pagedOnce)
	sc.Step(`^only "([^"]+)"'s pair on the only station is cooling$`, st.onlyPairCooling)
	sc.Step(`^no cooling alert is raised$`, st.noCoolingAlert)
	sc.Step(`^the market still counts the station on air$`, st.marketCountsOnAir)

	// TPM guard
	sc.Step(`^a curated station registers "([^"]+)" with tpm (\d+)$`, st.curatedRegistersTPM)
	sc.Step(`^the registration is accepted and /discover shows tpm (\d+) on the offer$`, st.regAcceptedShowsTPM)
	sc.Step(`^a human station registers "([^"]+)" with tpm (\d+)$`, st.humanRegistersTPM)
	sc.Step(`^the registration is rejected naming "([^"]+)"$`, st.regRejectedNaming)
	sc.Step(`^a curated station "([^"]+)" with tpm (\d+) is the only station for "([^"]+)"$`, st.curatedTPMOnly)
	sc.Step(`^the per-request TPM share is (\d+)%$`, st.tpmShare)
	sc.Step(`^"([^"]+)" relays a request whose measured prompt is (\d+) tokens$`, st.relaysMeasuredPrompt)
	sc.Step(`^the response is 429 with error code "([^"]+)" and a Retry-After$`, st.resp429CodeRA)
	sc.Step(`^"([^"]+)" is not cooling for anyone$`, st.notCoolingForAnyone)
	sc.Step(`^curated station "([^"]+)" with tpm (\d+) and human station "([^"]+)" are on air for "([^"]+)"$`, st.curatedTPMAndHuman)
	sc.Step(`^a curated station "([^"]+)" with no tpm is the only station for "([^"]+)"$`, st.curatedNoTPMOnly)
	sc.Step(`^"([^"]+)" receives the request$`, st.receivesRequest)

	// #7 rate limits
	sc.Step(`^the anonymous per-IP limit is (\d+) rpm with burst (\d+)$`, st.anonLimit)
	sc.Step(`^(\d+) requests from one client IP arrive, each signed by a fresh unbound keypair$`, st.unboundFresh)
	sc.Step(`^at most (\d+) are admitted in the first instant$`, st.atMostAdmitted)
	sc.Step(`^the rest are 429 "([^"]+)"$`, st.rest429)
	sc.Step(`^"([^"]+)"'s keypair is bound to her account$`, st.keypairBound)
	sc.Step(`^"([^"]+)" sends (\d+) requests from one client IP$`, st.sendsFromOneIP)
	sc.Step(`^they draw on "([^"]+)"'s account bucket \(120 rpm, burst 40\), not the per-IP bucket$`, st.drawsOnAccountBucket)
	sc.Step(`^"([^"]+)" and "([^"]+)" are bound accounts behind one client IP$`, st.behindOneIP)
	sc.Step(`^each sends (\d+) requests$`, st.eachSends)
	sc.Step(`^neither is limited by the other's traffic$`, st.neitherLimited)
	sc.Step(`^the free-traffic per-IP limit is (\d+) rpm$`, st.freePerIP)
	sc.Step(`^node "([^"]+)" is free for "([^"]+)"$`, st.nodeFreeFor)
	sc.Step(`^one client IP sends (\d+) requests to "([^"]+)" within a minute$`, st.oneIPSends)
	sc.Step(`^at most (\d+) are dispatched and the rest are 429 with error code "([^"]+)"$`, st.dispatchedAtMostRest)
	sc.Step(`^the free-traffic per \(IP, station\) limit is (\d+) rpm$`, st.freePerIPStation)
	sc.Step(`^nodes "([^"]+)" and "([^"]+)" are free for "([^"]+)"$`, st.twoFree)
	sc.Step(`^one client IP sends (\d+) requests pinned to "([^"]+)" within a minute$`, st.oneIPPinned)
	sc.Step(`^at most (\d+) reach "([^"]+)"$`, st.atMostReach)
	sc.Step(`^the free pin cap is (\d+) per caller per minute$`, st.freePinCap)
	sc.Step(`^"([^"]+)" sends (\d+) requests with provider\.order \["([^"]+)"\] within a minute$`, st.sendsPinnedWithin)
	sc.Step(`^the rest are 429 with error code "([^"]+)"$`, st.rest429Code)
	sc.Step(`^"([^"]+)" sends (\d+) paid requests from one client IP within a minute$`, st.sendsPaid)
	sc.Step(`^none is refused for "([^"]+)"$`, st.noneRefusedFor)
	sc.Step(`^"([^"]+)" owns node "([^"]+)"$`, st.ownsNode)
	sc.Step(`^"([^"]+)" sends (\d+) requests to her own station within a minute$`, st.sendsToOwn)
	sc.Step(`^none is refused for "([^"]+)" or "([^"]+)"$`, st.noneRefusedFor2)
	sc.Step(`^a free grant with rpm (\d+)$`, st.freeGrantRPM)
	sc.Step(`^the grant holder sends (\d+) requests from one client IP within a minute$`, st.grantHolderSends)
	sc.Step(`^one client IP sends (\d+) free requests to each instance within a minute$`, st.freeToEachInstance)
	sc.Step(`^the per-IP free limit is applied to the (\d+) together$`, st.freeLimitTogether)

	// #8 sort band
	sc.Step(`^stations "([^"]+)", "([^"]+)", "([^"]+)" serve "([^"]+)" at estimated request costs \$([0-9.]+), \$([0-9.]+) and \$([0-9.]+)$`, st.threeAtCosts)
	sc.Step(`^each has equal spare capacity$`, st.equalSpare)
	sc.Step(`^(\d+) consumers relay with provider\.sort "([^"]+)"$`, st.consumersSort)
	sc.Step(`^"([^"]+)" and "([^"]+)" each serve between (\d+)% and (\d+)% of requests$`, st.eachServeBetween)
	sc.Step(`^"([^"]+)" (serves none|serves some)$`, st.servesSomeNone)
	sc.Step(`^stations "([^"]+)" and "([^"]+)" serve "([^"]+)" at estimated costs \$([0-9.]+) and \$([0-9.]+)$`, st.twoAtCosts)
	sc.Step(`^both "([^"]+)" and "([^"]+)" serve requests$`, st.bothServe)
	sc.Step(`^stations "([^"]+)" and "([^"]+)" are within the price band$`, st.withinBand)
	sc.Step(`^"([^"]+)" has (\d+) of (\d+) slots busy and "([^"]+)" has (\d+) of (\d+) busy$`, st.slotsBusy)
	sc.Step(`^"([^"]+)" serves more requests than "([^"]+)"$`, st.servesMore)
	sc.Step(`^"([^"]+)" has every slot busy$`, st.everySlotBusy)
	sc.Step(`^a consumer relays with provider\.sort "([^"]+)"$`, st.consumerSort)
	sc.Step(`^the response is served by "([^"]+)"$`, st.servedByName)
	sc.Step(`^stations "([^"]+)" and "([^"]+)" serve "([^"]+)" at identical price, speed and capacity$`, st.identicalStations)
	sc.Step(`^each serves between (\d+)% and (\d+)% of requests$`, st.eachBetween)
	sc.Step(`^the same request id always picks the same station$`, st.sameRequestSame)
	sc.Step(`^stations "([^"]+)", "([^"]+)", "([^"]+)" measure (\d+), (\d+) and (\d+) tok/s for "([^"]+)"$`, st.threeTPS)
	sc.Step(`^"([^"]+)" and "([^"]+)" share the requests and "([^"]+)" serves none$`, st.shareAndNone)
	sc.Step(`^stations "([^"]+)", "([^"]+)", "([^"]+)" measure (\d+) ms, (\d+) ms and (\d+) ms TTFT$`, st.threeTTFT)
	sc.Step(`^station "([^"]+)" measures (\d+) tok/s and "([^"]+)" is unmeasured$`, st.measuredAndUnmeasured)
	sc.Step(`^stations "([^"]+)", "([^"]+)", "([^"]+)", "([^"]+)" at estimated costs \$([0-9.]+), \$([0-9.]+), \$([0-9.]+), \$([0-9.]+)$`, st.fourAtCosts)
	sc.Step(`^every station answers the next request with 429$`, st.everyNext429)
	sc.Step(`^the attempts are the two band stations first, in seeded order, then "([^"]+)", then "([^"]+)" up to the attempt limit$`, st.attemptsBandThen)
	sc.Step(`^a consumer relays with provider\.sort "([^"]+)" and provider\.allow_fallbacks false$`, st.sortNoFallback)
	sc.Step(`^exactly one attempt was dispatched, to "([^"]+)" or "([^"]+)"$`, st.oneAttemptTo)
	sc.Step(`^the sort band is (\d+)% on price and (\d+)% on speed$`, st.sortBand)
	sc.Step(`^stations "([^"]+)" and "([^"]+)" differ by (\d+)% on (price|tps)$`, st.differBy)
	sc.Step(`^(\d+) consumers relay for "([^"]+)"$`, st.consumersForAny)
	sc.Step(`^both serve requests$`, st.bothServeAny)
	sc.Step(`^station "([^"]+)" declares capacity (\d+) but its measured in-flight ceiling is (\d+)$`, st.declaresCapacityMeasured)
	sc.Step(`^station "([^"]+)" declares capacity (\d+)$`, st.declaresCapacity)
	sc.Step(`^(\d+) consumers relay with provider\.sort "([^"]+)" and both are in the band$`, st.consumersSortBand)
	sc.Step(`^"([^"]+)"'s weight is computed from its measured concurrency, not its declaration$`, st.weightFromMeasured)

	// #15 Tower coin
	sc.Step(`^(\d+) direct stations with capacity (\d+) each and one Tower row with capacity (\d+) serve "([^"]+)"$`, st.directsAndTower)
	sc.Step(`^every candidate is Tier A$`, st.everyTierA)
	sc.Step(`^the Tower serves between (\d+)% and (\d+)% of requests$`, st.towerServesBetween)
	sc.Step(`^direct capacity (\d+) and Tower capacity (\d+) are eligible for "([^"]+)"$`, st.capacityEachSide)
	sc.Step(`^each fabric serves between (\d+)% and (\d+)% of requests$`, st.eachFabricBetween)
	sc.Step(`^direct stations with capacity (\d+), of which (\d+) are excluded by the request's filters$`, st.directsExcluded)
	sc.Step(`^a Tower row with capacity (\d+) is eligible$`, st.towerCapEligible)
	sc.Step(`^(\d+) consumers relay for "([^"]+)" with those filters$`, st.consumersWithFilters)
	sc.Step(`^direct capacity (\d+) and Tower capacity (\d+) for "([^"]+)"$`, st.capacityFor)
	sc.Step(`^the same request id is relayed twice$`, st.sameIDTwice)
	sc.Step(`^both go to the same fabric first$`, st.sameFabricFirst)
	sc.Step(`^the direct station is cheaper$`, st.directCheaper)
	sc.Step(`^the direct station is tried first$`, st.directTriedFirst)
	sc.Step(`^"([^"]+)" owns a direct station for "([^"]+)" and a billed Tower also serves "([^"]+)"$`, st.ownsDirectAndTower)
	sc.Step(`^"([^"]+)" relays for "([^"]+)"$`, st.relaysFor)
	sc.Step(`^the Tower is never tried$`, st.towerNeverTried)
	sc.Step(`^a Tower whose station serves "([^"]+)" with capacity (\d+)$`, st.towerStationCap)
	sc.Step(`^one direct station with capacity (\d+)$`, st.oneDirectCap)

	// #16 home first
	sc.Step(`^human station "([^"]+)" and curated station "([^"]+)" serve "([^"]+)"$`, st.humanAndCurated)
	sc.Step(`^"([^"]+)" is faster and cheaper$`, st.fasterCheaper)
	sc.Step(`^(\d+) consumers relay to "([^"]+)" with no routing object$`, st.consumersNoRouting)
	sc.Step(`^every request is served by "([^"]+)"$`, st.everyServedBy)
	sc.Step(`^human station "([^"]+)" with every slot busy and curated station "([^"]+)" serve "([^"]+)"$`, st.humanBusyAndCurated)
	sc.Step(`^a consumer relays to "([^"]+)"$`, st.consumerRelaysTo)
	sc.Step(`^human stations "([^"]+)" and "([^"]+)" and curated "([^"]+)" serve "([^"]+)"$`, st.twoHumansOneCurated)
	sc.Step(`^"([^"]+)" and "([^"]+)" answer the next request with 429$`, st.bothAnswer429)
	sc.Step(`^the attempts are "([^"]+)", "([^"]+)", then "([^"]+)"$`, st.attemptsAre)
	sc.Step(`^curated station "([^"]+)" is the only station for "([^"]+)"$`, st.curatedOnly)
	sc.Step(`^human station "([^"]+)" and a faster curated station "([^"]+)" serve "([^"]+)"$`, st.humanAndFasterCurated)
	sc.Step(`^a consumer relays to "([^"]+)" with (.+)$`, st.consumerRelaysWith)
	sc.Step(`^"([^"]+)" competes on equal terms with "([^"]+)"$`, st.competesEqual)
	sc.Step(`^a consumer relays with provider\.order \["([^"]+)"\]$`, st.consumerOrder)
	sc.Step(`^a consumer relays with roger\.self_hosted_only true$`, st.consumerSelfHosted)
	sc.Step(`^the response is not served by "([^"]+)"$`, st.notServedBy)
	sc.Step(`^every human station for "([^"]+)" is cooling and curated station "([^"]+)" is on air$`, st.humansCoolingCuratedOn)
	sc.Step(`^a Tower row whose node is curated and a human direct station serve "([^"]+)"$`, st.curatedTowerAndHuman)
	sc.Step(`^(\d+) consumers relay to "([^"]+)"$`, st.consumersRelayToEdge)
	sc.Step(`^the human direct station serves every request$`, st.humanDirectEvery)
	sc.Step(`^curated stations "([^"]+)" and "([^"]+)" are the only stations for "([^"]+)"$`, st.twoCuratedOnly)
	sc.Step(`^the pick between them follows the ordinary score$`, st.ordinaryScore)
}

// consumersForAny routes the "N consumers relay for M" step: through the edge buyers when a
// Tower is in the scenario, else through ordinary funded consumers.
func (s *fa6State) consumersForAny(n, model string) error {
	if len(s.towers) > 0 {
		return s.consumersForEdge(n, model)
	}
	return s.consumersFor(n, model)
}

// keep sort imported for helpers added later
var _ = sort.Strings

// lastBodyOf is the last body station `name`'s upstream received (nil when none).
func (s *fa6State) lastBodyOf(name string) []byte {
	s.hitMu.Lock()
	defer s.hitMu.Unlock()
	bs := s.bodies[name]
	if len(bs) == 0 {
		return nil
	}
	return bs[len(bs)-1]
}
