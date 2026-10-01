package main

// routing_request_shape_bdd_test.go makes features/routing/request_shape.feature and
// features/routing/variant_sugar.feature EXECUTABLE against the REAL broker (slice 1 of the
// routing-expression set; contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md §1-§4).
//
// It rides the slice-0 runner's fixtures (s0State over rpState over foState: a real
// relayBroker + store (Postgres when ROGERAI_TEST_DATABASE_URL is set) + miniredis, real
// stations with scripted upstreams behind real tunnels, the real edge fabric for the Tower
// scenarios, the real cross-instance harness for the bus scenario) - no mocks. Every
// request is a real relay through relay(); what a station was sent is read at its upstream.
//
// Scenarios that can only be driven from another package are tagged in the .feature and
// excluded here (@proxy: the local proxy; @harness: the agent harness client).
//
// OBSERVATION POINTS this runner relies on (stated here because slice 1 has to provide them):
//   - /admin/live counters: routing_pref_<value> (exists), routing_strict_sort,
//     routing_body_requests, routing_body_rejects, variant_free / variant_floor /
//     variant_nitro, edge_coin_flips.
//   - ONE routing log line per routing pass, beginning "routing " and carrying key=value
//     pairs: sort=<price|throughput|latency|none> max_in=<n> max_out=<n> min_tps=<n>
//     reward_out=<n>. variant_sugar.feature names this line ("the request's routing log line
//     names sort ..."); the cap / min-tps / reward-range steps read the same line.
//   - "the failover plan" is observed behaviourally: the same request is replayed with every
//     on-air station answering 429 and ROGERAI_RELAY_ATTEMPTS raised, and the order in which
//     the stations' upstreams are hit IS the plan (rs1Probe). Holds, hits and upstream counts
//     are cached after each scenario relay so a probe replay never disturbs them.
//   - a time-of-use window is exercised by shifting the declared window so the real clock
//     stands where the scenario's "at HH:MM UTC" stands relative to it (no clock seam exists
//     in relay(); the offer is what a station controls).
//   - sync moderation runs against a counting classifier stub on every scenario (the
//     classifier is the external dependency; the broker's screen path is real), so "ran
//     exactly once" / "did not run" / "received the identical prompt text" are countable.

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
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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

const rs1IllegalMarker = "RS1-ILLEGAL-PROMPT"

// rs1Req is one consumer request as a scenario states it.
type rs1Req struct {
	caller       string // "user" | "anon" | "grant" | "origin"
	kind         string // "chat" | "stream" | "illegal"
	model        string
	noModel      bool
	raw          string // a complete raw body (names are mapped, nothing else is added)
	frag         string // raw JSON members appended to the base body
	headers      map[string]string
	maxTokens    int
	promptTokens int
}

// rs1Shot is what one relay left behind (cached so a probe replay cannot disturb it).
type rs1Shot struct {
	code    int
	hdr     http.Header
	body    []byte
	sent    []byte // the body the consumer sent
	base    []byte // the same body WITHOUT the routing fragment (the "stripped" body)
	holdN   int    // holds placed on the payer since the scenario snapshot
	holdAmt float64
	hits    []string       // station names whose upstream was hit since the snapshot, in order
	counts  map[string]int // upstream requests per station since the snapshot
	logFrom int            // log offset when this relay started
	logTo   int
	modN    int // classifier calls during this relay
	modText string
	payer   string
}

type rs1Tower struct {
	*rpTower
	mu     sync.Mutex
	bodies [][]byte
}

func (t *rs1Tower) jobs() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.bodies)
}

type rs1State struct {
	*s0State

	codes    map[string]string // scenario band code ("FREQ-1") -> the real minted code
	idAlias  map[string]string // scenario name -> a foreign id (cross-instance nodes)
	offAir   map[string]bool
	tou      map[string][2]int // station -> declared free window [startMin, endMin)
	rsTowers map[string]*rs1Tower

	grantSecret string
	grantPayer  string // the wallet a priced grant bills ("" = free grant / none)

	last     rs1Req
	shot     rs1Shot // the last scenario relay
	shots    []rs1Shot
	holdBase map[string]int // wallet -> hold rows at the scenario snapshot
	admin0   map[string]any // /admin/live at the scenario snapshot

	modSrv   *httptest.Server
	modMu    sync.Mutex
	modCalls int
	modTexts []string

	scrSeenBeforeRouting *bool // async: was the screening job enqueued before the routing pass

	xi       *xiState
	regModel string
	served1  string // "is served by X" bookkeeping
}

// --- reset / teardown -----------------------------------------------------------------

func (s *rs1State) resetRS1() error {
	s.teardownRS1()
	if err := s.resetSlice0(); err != nil {
		return err
	}
	s.codes, s.idAlias, s.offAir = map[string]string{}, map[string]string{}, map[string]bool{}
	s.tou, s.rsTowers = map[string][2]int{}, map[string]*rs1Tower{}
	s.grantSecret, s.grantPayer = "", ""
	s.last, s.shot, s.shots = rs1Req{}, rs1Shot{}, nil
	s.holdBase, s.admin0 = nil, nil
	s.scrSeenBeforeRouting = nil
	s.regModel, s.served1 = "", ""
	s.modMu.Lock()
	s.modCalls, s.modTexts = 0, nil
	s.modMu.Unlock()
	// The counting classifier: flags a prompt carrying the illegal marker, passes the rest.
	s.modSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(raw, &in)
		s.modMu.Lock()
		s.modCalls++
		s.modTexts = append(s.modTexts, in.Input)
		s.modMu.Unlock()
		if strings.Contains(in.Input, rs1IllegalMarker) {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"S1":true}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"categories":{}}]}`))
	}))
	s.b.mod = moderation{provider: "url", url: s.modSrv.URL, client: s.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeSync}
	return nil
}

func (s *rs1State) teardownRS1() {
	if s.xi != nil {
		s.xi.cleanup()
		s.xi = nil
	}
	if s.modSrv != nil {
		s.modSrv.Close()
		s.modSrv = nil
	}
}

// --- ids and name mapping ---------------------------------------------------------------

func (s *rs1State) id(name string) string {
	if v, ok := s.idAlias[name]; ok {
		return v
	}
	return s.idOf(name)
}

// mapFrag rewrites scenario names inside a raw JSON fragment to what the broker knows:
// station / Tower names to their ids, scenario band codes to the minted codes.
func (s *rs1State) mapFrag(frag string) string {
	names := make([]string, 0, len(s.stations)+len(s.towers)+len(s.idAlias))
	seen := map[string]string{}
	for n, st := range s.stations {
		seen[n] = st.id
	}
	for n, tw := range s.towers {
		seen[n] = tw.id
	}
	for n, v := range s.idAlias {
		seen[n] = v
	}
	for n, c := range s.codes {
		seen[n] = c
	}
	for n := range seen {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		frag = strings.ReplaceAll(frag, strconv.Quote(n), strconv.Quote(seen[n]))
	}
	return frag
}

func (s *rs1State) mapHeader(name, value string) string {
	switch name {
	case "X-Roger-Node":
		return s.id(value)
	case "X-Roger-Exclude-Nodes":
		parts := strings.Split(value, ",")
		for i, p := range parts {
			parts[i] = s.id(strings.TrimSpace(p))
		}
		return strings.Join(parts, ",")
	case "X-Roger-Freq":
		if c, ok := s.codes[value]; ok {
			return c
		}
	}
	return value
}

// --- the relay --------------------------------------------------------------------------

func (s *rs1State) payerOf(q rs1Req) string {
	switch q.caller {
	case "anon":
		return protocol.UserIDFromPubkey(hex.EncodeToString(s.anonPriv.Public().(ed25519.PublicKey)))
	case "grant":
		if s.grantPayer != "" {
			return s.grantPayer
		}
		return "g_" + s.grantID
	}
	return s.wallet
}

func (s *rs1State) holdRowsOf(wallet string) (n int, newest float64) {
	rows, err := s.db.LedgerOf(wallet, []string{store.KindHold}, 5000)
	if err != nil || len(rows) == 0 {
		return 0, 0
	}
	top := rows[0]
	for _, r := range rows[1:] {
		if r.ID > top.ID {
			top = r
		}
	}
	return len(rows), -top.Amount
}

func (s *rs1State) snapOnce() error {
	if s.holdBase != nil {
		return nil
	}
	if err := s.snapshotNowOnce(); err != nil {
		return err
	}
	s.holdBase = map[string]int{}
	wallets := []string{s.wallet, s.payerOf(rs1Req{caller: "anon"})}
	if s.grantID != "" {
		wallets = append(wallets, s.payerOf(rs1Req{caller: "grant"}))
	}
	for _, w := range wallets {
		n, _ := s.holdRowsOf(w)
		s.holdBase[w] = n
	}
	if err := s.readAdminLive(); err == nil {
		s.admin0 = s.adminResp
	}
	return nil
}

func (s *rs1State) buildBody(q rs1Req) (sent, base []byte) {
	if q.raw != "" {
		b := []byte(s.mapFrag(q.raw))
		return b, b
	}
	tokens := q.promptTokens
	if tokens == 0 {
		tokens = 50
	}
	content := utPrompt(tokens)
	if q.kind == "illegal" {
		content = rs1IllegalMarker + " " + content
	}
	m := map[string]any{"messages": []map[string]string{{"role": "user", "content": content}}}
	if !q.noModel {
		m["model"] = q.model
	}
	if q.maxTokens > 0 {
		m["max_tokens"] = q.maxTokens
	}
	if q.kind == "stream" {
		m["stream"] = true
	}
	base, _ = json.Marshal(m)
	frag := s.mapFrag(q.frag)
	if strings.TrimSpace(frag) == "" {
		return base, base
	}
	sent = append(bytes.TrimSuffix(bytes.TrimSpace(base), []byte("}")), []byte(","+frag+"}")...)
	return sent, base
}

func (s *rs1State) httpRequest(q rs1Req, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	switch q.caller {
	case "grant":
		r.Header.Set("Authorization", "Bearer "+s.grantSecret)
	case "anon":
		// The anonymous wallet is seeded exactly as production seeds first use, so the ~$0
		// free-offer hold can land; it is still not logged in and cannot spend.
		s.b.seedFunds = 0.5
		signReq(r, s.anonPriv, body)
	default:
		signReq(r, s.consumerPriv, body)
	}
	if q.caller == "origin" {
		r.Header.Set("Origin", "https://rogerai.fm")
	}
	for k, v := range q.headers {
		r.Header.Set(k, s.mapHeader(k, v))
	}
	return r
}

// relayRaw runs one real relay and returns what came back (no state is recorded).
func (s *rs1State) relayRaw(q rs1Req) (rs1Shot, *recWriter) {
	sent, base := s.buildBody(q)
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.modMu.Lock()
	mod0 := s.modCalls
	s.modMu.Unlock()
	from := len(s.logs.String())
	s.b.relay(w, s.httpRequest(q, sent))
	s.modMu.Lock()
	shot := rs1Shot{code: w.Code, hdr: w.Header(), body: w.Body.Bytes(), sent: sent, base: base,
		logFrom: from, logTo: len(s.logs.String()), modN: s.modCalls - mod0}
	if n := len(s.modTexts); n > 0 && shot.modN > 0 {
		shot.modText = s.modTexts[n-1]
	}
	s.modMu.Unlock()
	return shot, w
}

// fire is a SCENARIO relay: it is recorded as "the response" and its ledger / upstream
// footprint is cached for the Then steps.
func (s *rs1State) fire(q rs1Req) error {
	if s.xi != nil {
		return s.fireXI(q)
	}
	if err := s.snapOnce(); err != nil {
		return err
	}
	shot, w := s.relayRaw(q)
	shot.payer = s.payerOf(q)
	n, amt := s.holdRowsOf(shot.payer)
	shot.holdN, shot.holdAmt = n-s.holdBase[shot.payer], amt
	shot.hits = s.hitList()
	shot.counts = map[string]int{}
	for name, st := range s.stations {
		shot.counts[name] = st.upstreamCount() - s.reqBefore[name]
	}
	s.last, s.shot = q, shot
	s.shots = append(s.shots, shot)
	s.batch = append(s.batch, rpResult{code: shot.code, hdr: shot.hdr, body: shot.body})
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = shot.code, shot.body, shot.hdr, w
	return nil
}

// fireXI sends the request over real HTTP to instance A of the cross-instance fabric.
func (s *rs1State) fireXI(q rs1Req) error {
	sent, base := s.buildBody(q)
	inst := s.xi.inst["A"]
	req, _ := http.NewRequest(http.MethodPost, inst.url()+"/v1/chat/completions", bytes.NewReader(sent))
	req.Header.Set("Content-Type", "application/json")
	_, priv, _ := ed25519.GenerateKey(nil)
	pub, ts, sig := protocol.SignRequest(priv, req.Method, "/v1/chat/completions", sent)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("cross-instance relay: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s.last = q
	s.shot = rs1Shot{code: resp.StatusCode, hdr: resp.Header, body: body, sent: sent, base: base}
	s.shots = append(s.shots, s.shot)
	s.lastCode, s.lastBody, s.lastHdr = resp.StatusCode, body, resp.Header
	return nil
}

// replay re-runs a request WITHOUT recording it as the scenario's response.
func (s *rs1State) replay(q rs1Req) rs1Shot {
	code, body, hdr, rec, batch := s.lastCode, s.lastBody, s.lastHdr, s.lastRec, s.batch
	shot, _ := s.relayRaw(q)
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec, s.batch = code, body, hdr, rec, batch
	return shot
}

func (s *rs1State) clearCooling() {
	s.b.mu.Lock()
	for k := range s.b.cooling {
		delete(s.b.cooling, k)
	}
	s.b.mu.Unlock()
	if s.mr != nil {
		s.mr.FastForward(3 * time.Minute) // expire the shared cooldown keys too
	}
	s.heartbeatOnAir()
}

func (s *rs1State) heartbeatOnAir() {
	s.b.mu.Lock()
	for name, st := range s.stations {
		if !s.offAir[name] {
			s.b.lastSeen[st.id] = time.Now()
		}
	}
	s.b.mu.Unlock()
}

// rs1Probe replays the last request with every on-air station answering 429 and the attempt
// budget raised: the order the upstreams are hit in is the failover plan, and the body each
// one received is what the plan carried for it.
func (s *rs1State) probePlan() (order []string, bodies map[string][]byte, final rs1Shot) {
	s.clearCooling()
	var mu sync.Mutex
	bodies = map[string][]byte{}
	saved := map[string]func(int, http.ResponseWriter, *http.Request){}
	for name, st := range s.stations {
		name, st := name, st
		st.mu.Lock()
		saved[name] = st.script
		st.mu.Unlock()
		st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			order = append(order, name)
			bodies[name] = b
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
		})
	}
	prev, had := os.LookupEnv("ROGERAI_RELAY_ATTEMPTS")
	_ = os.Setenv("ROGERAI_RELAY_ATTEMPTS", "12")
	final = s.replay(s.last)
	if had {
		_ = os.Setenv("ROGERAI_RELAY_ATTEMPTS", prev)
	} else {
		_ = os.Unsetenv("ROGERAI_RELAY_ATTEMPTS")
	}
	for name, st := range s.stations {
		st.set(saved[name])
	}
	s.clearCooling()
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), order...), bodies, final
}

// --- Given: stations --------------------------------------------------------------------

func rs1f(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }
func rs1i(v string) int     { n, _ := strconv.Atoi(v); return n }

// onAir brings a Tier-A station up for model at the price, or re-prices it when the
// scenario names a station the Background already stood up.
func (s *rs1State) onAir(name, model string, in, out float64) *fstation {
	s.model = model
	if st, ok := s.stations[name]; ok {
		s.setPrice(name, in, out)
		delete(s.offAir, name)
		s.b.mu.Lock()
		s.b.lastSeen[st.id] = time.Now()
		s.b.mu.Unlock()
		return st
	}
	return s.stand(name, in, out)
}

func (s *rs1State) nodeOnAir(name, model, in, out string) error {
	s.onAir(name, model, rs1f(in), rs1f(out))
	return nil
}

func (s *rs1State) nodeOnAirTPS(name, model, in, out, tps string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.setTPS(st.id, rs1f(tps))
	return nil
}

func (s *rs1State) nodeOnAirUnmeasured(name, model, in, out string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.b.metricsMu.Lock()
	delete(s.b.tps, st.id)
	s.b.metricsMu.Unlock()
	return nil
}

func (s *rs1State) nodeOnAirAttested(name, model, in, out string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.b.mu.Lock()
	s.b.confidential[st.id] = true
	s.b.mu.Unlock()
	return nil
}

func (s *rs1State) nodeTEE(name, model string) error {
	return s.nodeOnAirAttested(name, model, "0.10", "0.30")
}

func (s *rs1State) nodeNotAttested(name, model string) error {
	st := s.onAir(name, model, 0.10, 0.30)
	s.b.mu.Lock()
	delete(s.b.confidential, st.id)
	s.b.mu.Unlock()
	return nil
}

func rs1hhmm(min int) string {
	min = ((min % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

func (s *rs1State) nodeOnAirTOU(name, model, in, out, h1, m1, h2, m2 string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	start, end := rs1i(h1)*60+rs1i(m1), rs1i(h2)*60+rs1i(m2)
	s.tou[name] = [2]int{start, end}
	s.setOffer(st.id, func(o *protocol.ModelOffer) {
		o.Schedule = []protocol.PriceWindow{{Start: rs1hhmm(start), End: rs1hhmm(end), Free: true}}
	})
	return nil
}

// shiftClockTo re-declares every time-of-use window so the REAL clock stands where
// hh:mm stands relative to the declared window.
func (s *rs1State) shiftClockTo(hh, mm int) {
	now := time.Now().UTC()
	shift := now.Hour()*60 + now.Minute() - (hh*60 + mm)
	for name, win := range s.tou {
		st := s.st(name)
		a, b := win[0]+shift, win[1]+shift
		s.setOffer(st.id, func(o *protocol.ModelOffer) {
			o.Schedule = []protocol.PriceWindow{{Start: rs1hhmm(a), End: rs1hhmm(b), Free: true}}
		})
	}
}

func (s *rs1State) goOffAir(name string) {
	st := s.st(name)
	s.b.mu.Lock()
	s.b.lastSeen[st.id] = time.Now().Add(-2 * nodeTTL)
	s.b.mu.Unlock()
	s.offAir[name] = true
}

func (s *rs1State) nodeGoesOffAir(name string) error { s.goOffAir(name); return nil }

func (s *rs1State) alone(name, model string) {
	for n, st := range s.stations {
		if n != name && st.model == model {
			s.goOffAir(n)
		}
	}
}

func (s *rs1State) nodeAlonePriced(name, model, in, out string) error {
	s.onAir(name, model, rs1f(in), rs1f(out))
	s.alone(name, model)
	return nil
}

func (s *rs1State) nodeAloneCtx(name, model, ctx string) error {
	st := s.onAir(name, model, 0.10, 0.30)
	s.alone(name, model)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = rs1i(ctx), false })
	return nil
}

func (s *rs1State) everyStationCtx(model, ctx string) error {
	for _, st := range s.stations {
		if st.model == model {
			s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = rs1i(ctx), false })
		}
	}
	return nil
}

func (s *rs1State) nodeTPS(name, tps string) error {
	s.setTPS(s.st(name).id, rs1f(tps))
	return nil
}

func (s *rs1State) coolNode(name string, secs int) error {
	s.model = s.st(name).model
	return s.cool(name, secs)
}

func (s *rs1State) nodeCooling(name, secs string) error { return s.coolNode(name, rs1i(secs)) }

func (s *rs1State) everyStationCooling(model string) error {
	for n, st := range s.stations {
		if st.model == model && !s.offAir[n] {
			if err := s.coolNode(n, 15); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *rs1State) everyStationCoolingSoonest(model, secs string) error {
	soonest := rs1i(secs)
	names := []string{}
	for n, st := range s.stations {
		if st.model == model && !s.offAir[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for i, n := range names {
		if err := s.coolNode(n, soonest+i*11); err != nil {
			return err
		}
	}
	return nil
}

func (s *rs1State) nodeAnswers429(name string) error { s.script429For(name, ""); return nil }

func (s *rs1State) nodeTierB(name string) error {
	s.setSuccess(s.st(name).id, 0.3) // a measured success average under the Tier-A gate
	return nil
}

func (s *rs1State) nodeDeadProbe(name string) error {
	return s.stationState(name, "past the dead-probe streak")
}

func (s *rs1State) everyNodeTierA() error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for name, st := range s.stations {
		tq := s.b.trust[st.id]
		if !tq.probed || !tq.probeOK {
			return fmt.Errorf("%q has not passed its canary", name)
		}
	}
	return nil
}

// nodeAnswersWithExtraBody: the station's completion body also carries the given members.
func (s *rs1State) nodeAnswersWithExtraBody(name, frag string) error {
	extra := s.mapFrag(frag)
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20},` + extra + `}`))
	})
	return nil
}

// nodeAnswersWithExtraReceiptField takes the station's tunnel over with a loop that posts a
// correctly signed receipt whose JSON also carries the extra member (the node signature
// covers the canonical fields only, so the injected key rides outside it - which is the
// whole attack surface this scenario is about).
func (s *rs1State) nodeAnswersWithExtraReceiptField(name, frag string) error {
	st := s.st(name)
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+frag+"}"), &extra); err != nil {
		return fmt.Errorf("bad receipt fragment %q: %v", frag, err)
	}
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
	s.b.mu.Lock()
	s.b.tunnels[st.id] = tun
	s.b.mu.Unlock()
	stop := s.stationStop
	go func() {
		for {
			select {
			case <-stop:
				return
			case job := <-tun.jobs:
				resp, err := http.Post(st.up.URL, "application/json", bytes.NewReader(job.Body))
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var d struct {
					Usage struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				_ = json.Unmarshal(body, &d)
				rec := protocol.UsageReceipt{
					RequestID: job.ID, NodeID: st.id, User: job.User, Model: st.model,
					PromptTokens: d.Usage.PromptTokens, CompletionTokens: d.Usage.CompletionTokens,
					PriceIn: st.priceIn, PriceOut: st.priceOut, TS: time.Now().Unix(), LineageMethod: "p0-upstream-usage",
				}
				st.mu.Lock()
				rec.PrevHash = st.lastHash
				rec.SignNode(st.priv)
				st.lastHash = rec.Hash()
				st.mu.Unlock()
				wire, _ := json.Marshal(protocol.JobResult{ID: job.ID, Status: resp.StatusCode, Body: body, Receipt: rec})
				var m map[string]any
				_ = json.Unmarshal(wire, &m)
				if r, ok := m["receipt"].(map[string]any); ok {
					for k, v := range extra {
						var any1 any
						_ = json.Unmarshal(v, &any1)
						r[k] = any1
					}
				}
				wire, _ = json.Marshal(m)
				s.deliverRaw(st, wire)
			}
		}
	}()
	return nil
}

func (s *rs1State) nothingElse() error { return nil }

// --- Given: consumers, grants, bands ----------------------------------------------------

func (s *rs1State) loggedIn(name, bal string) error {
	if name == "u-1" {
		return s.fund(rs1f(bal))
	}
	// A second consumer becomes the scenario's caller (its wallet is what the hold and
	// ledger steps read).
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: name + "-" + s.nonce, Pubkey: pub}); err != nil {
		return err
	}
	s.consumerPriv, s.wallet = priv, "u_gh_"+strconv.FormatInt(gid.Int64()+10, 10)
	s.funded = false
	return s.fund(rs1f(bal))
}

func (s *rs1State) accountWallet(pubHex string) string {
	wallet := protocol.UserIDFromPubkey(pubHex)
	if o, ok, err := s.db.OwnerByPubkey(pubHex); err == nil && ok {
		if w, ok := accountWalletForOwner(o); ok {
			wallet = w
		}
	}
	return wallet
}

// standOwned re-stands a station under another account. Node->account bindings are TOFU, so
// the station comes back under a fresh node id (the scenario name maps to it from here on).
func (s *rs1State) standOwned(name string, owner *fstation) *fstation {
	old := s.st(name)
	old.mu.Lock()
	in, out, model := old.priceIn, old.priceOut, old.model
	old.mu.Unlock()
	s.b.mu.Lock()
	delete(s.b.nodes, old.id)
	delete(s.b.tunnels, old.id)
	delete(s.b.lastSeen, old.id)
	s.b.mu.Unlock()
	delete(s.stations, name)
	nonce := s.nonce
	s.nonce = nonce + "o"
	st := s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out, owner: owner})
	s.nonce = nonce
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, probeFails: 0, ttftMs: 200}
	s.b.mu.Unlock()
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) { utRealCompletion(w) })
	s.model = model
	return st
}

func (s *rs1State) mint(owner *fstation, models []string, priceOut float64) error {
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID, s.grantSecret = "grant_"+s.nonce, secret
	g := store.Grant{ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct, Label: "rs1",
		Models: models, Free: priceOut == 0, PriceIn: priceOut / 3, PriceOut: priceOut, CreatedAt: time.Now().Unix()}
	if priceOut > 0 {
		// A priced grant is SPONSORED: the issuing owner's account wallet pays.
		s.grantPayer = s.accountWallet(owner.acct)
		if _, err := s.db.AddCredits(s.grantPayer, 5); err != nil {
			return err
		}
	}
	return s.db.CreateGrant(g)
}

func (s *rs1State) ownerGrant(_, node, _, model string) error {
	return s.mint(s.st(node), []string{model}, 0)
}

func (s *rs1State) ownerGrantPriced(_, node, _, model, price string) error {
	return s.mint(s.st(node), []string{model}, rs1f(price))
}

func (s *rs1State) ownerGrantTwoModels(_, node, m1, m2, _, allowed string) error {
	st := s.st(node)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	for _, m := range []string{m1, m2} {
		has := false
		for _, o := range reg.Offers {
			has = has || o.Model == m
		}
		if !has {
			reg.Offers = append(reg.Offers, protocol.ModelOffer{Model: m, PriceIn: st.priceIn, PriceOut: st.priceOut, Ctx: 32768})
		}
	}
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return s.mint(st, []string{allowed}, 0)
}

func (s *rs1State) ownerGrantTwoNodes(_, a, b, _, model string) error {
	owner := s.st(a)
	s.standOwned(b, owner)
	return s.mint(owner, []string{model}, 0)
}

func (s *rs1State) userOwnsNode(_, node string) error {
	pub := hex.EncodeToString(s.consumerPriv.Public().(ed25519.PublicKey))
	s.standOwned(node, &fstation{ownerPriv: s.consumerPriv, acct: pub})
	return nil
}

func (s *rs1State) nodeGoesOffAirGrant(name string) error { return s.nodeGoesOffAir(name) }

func (s *rs1State) band(band, alias, name, model string, models []string) (*fstation, error) {
	s.model = model
	st := s.stand(name, 0, 0)
	tag := strings.ToUpper(regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(alias, ""))
	for len(tag) < 4 {
		tag += "X"
	}
	code := "147.520 MHz · " + tag[len(tag)-4:] + "-" + strings.ToUpper(s.nonce[:4])
	s.codes[alias] = code
	s.b.mu.Lock()
	s.b.private[st.id] = true
	s.b.mu.Unlock()
	rec := store.Band{ID: "band_" + band + "_" + s.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, Models: models, CreatedAt: time.Now().Unix()}
	return st, s.db.CreateBand(rec)
}

func (s *rs1State) privateBandOnly(band, alias, name, model string) error {
	_, err := s.band(band, alias, name, model, nil)
	return err
}

func (s *rs1State) privateBandOnlyTPS(band, alias, name, model, tps string) error {
	st, err := s.band(band, alias, name, model, nil)
	if err == nil {
		s.setTPS(st.id, rs1f(tps))
	}
	return err
}

func (s *rs1State) privateBandDenying(band, alias, name, model, _, _ string) error {
	_, err := s.band(band, alias, name, model, []string{model}) // an allow-list of one = every other model denied
	return err
}

func (s *rs1State) monthlyCapSpent(_, capv, _ string) error {
	// The cap gate refuses when spend + this request's own ceiling exceeds the cap; a cap
	// this small is over on any request to a paid station, whatever was spent before.
	return s.db.SetMonthlyCap(s.wallet, rs1f(capv))
}

func (s *rs1State) rateLimitExhausted(_ string) error {
	s.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 0.0001, burst: 1}
	if ok, _ := s.b.rl.allow(s.wallet); !ok {
		return fmt.Errorf("the first token was already gone")
	}
	return nil
}

func (s *rs1State) moderationMode(mode string) error {
	if mode == "sync" {
		s.b.mod.mode = modeSync
		return nil
	}
	s.b.mod.mode = modeAsync
	scr := newScreener(s.b, loadScreenerConfig())
	// Clock seam: the moment a job is enqueued, note whether any routing pass has run yet
	// for this broker (the routing_pref counters only move in the routing pass).
	scr.now = func() time.Time {
		if s.scrSeenBeforeRouting == nil {
			var n int64
			for i := range s.b.stats.routingPref {
				n += s.b.stats.routingPref[i].Load()
			}
			before := n == 0
			s.scrSeenBeforeRouting = &before
		}
		return time.Now()
	}
	s.b.scr = scr
	return nil
}

// --- Given: the edge fabric and the peer instance -----------------------------------------

// tower stands a real sealed fabric up like rpState.standUpTower, with an upstream that
// keeps every body the Tower's share node was handed.
func (s *rs1State) tower(name, model string, priceOut, tps float64) (*rs1Tower, error) {
	if tw, ok := s.rsTowers[name]; ok {
		return tw, nil
	}
	if err := s.ensureTower(); err != nil {
		return nil, err
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &rs1Tower{rpTower: &rpTower{name: name}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		tw.mu.Lock()
		tw.bodies = append(tw.bodies, reqBody)
		tw.mu.Unlock()
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
	if priceOut > 0 {
		routableEdgePriced(t, b, lt.id, stationID, model, liveEndpointOf(t, b, lt.id), int64(priceOut*1e6), int64(priceOut*1e6))
	}
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("tower %s: no routable row (%v)", name, err)
	}
	tw.nodeID = rows[0].NodeID
	if tps > 0 {
		s.setTPS(tw.nodeID, tps)
	}
	s.towers[name] = tw.rpTower
	s.rsTowers[name] = tw
	s.heartbeatOnAir()
	return tw, nil
}

func (s *rs1State) towerBridge(name, model string) error {
	_, err := s.tower(name, model, 0, 0)
	return err
}

func (s *rs1State) towerBridgeTPS(name, model, tps string) error {
	_, err := s.tower(name, model, 0, rs1f(tps))
	return err
}

func (s *rs1State) towerBridgePriced(name, model, out string) error {
	_, err := s.tower(name, model, rs1f(out), 0)
	return err
}

func (s *rs1State) noDirect(model string) error {
	for _, st := range s.stations {
		if st.model == model {
			return fmt.Errorf("a direct station serves %q", model)
		}
	}
	return nil
}

// twoInstances stands the real cross-instance fabric up (two brokers on one store and one
// bus) with a free node registered on A whose long-poll is held by B, and routes the
// scenario's request to A from here on.
func (s *rs1State) twoInstances(name string) error {
	model := "qwen3-32b"
	if st, ok := s.stations[name]; ok {
		model = st.model
	}
	xi := &xiState{}
	xi.reset(s.t)
	if err := xi.twoBrokers("ON"); err != nil {
		return err
	}
	n := xi.mkNode(name, model, 0, false)
	if err := n.register(s.t, xi.inst["A"]); err != nil {
		return err
	}
	n.beat(s.t, xi.inst["A"])
	xi.sweepAll()
	if err := xi.nodePolls("B"); err != nil {
		return err
	}
	s.xi = xi
	s.idAlias[name] = n.id
	return nil
}

// --- Given / When: registrations, persisted nodes, prices ---------------------------------

func (s *rs1State) stationRegistersAt(model, out string) error {
	s.regModel = model
	return s.nodeRegistersAt(model, rs1f(out))
}

func (s *rs1State) stationRegistersOffer(model string) error {
	s.regModel = model
	return s.nodeRegistersAt(model, 0.30)
}

func (s *rs1State) persisted(model string, out float64) error {
	pub, _, _ := ed25519.GenerateKey(nil)
	id := "n-persisted-" + s.nonce
	reg := protocol.NodeRegistration{NodeID: id, PubKey: hexOf(pub), BridgeToken: "tok-" + id, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: 0, PriceOut: out, Ctx: 4096}}}
	return s.db.UpsertNode(store.NodeRecord{NodeID: id, Reg: reg, LastSeen: time.Now().Unix()})
}

func (s *rs1State) persistedOffering(model string) error { return s.persisted(model, 0) }
func (s *rs1State) persistedNegative(model, out string) error {
	return s.persisted(model, -rs1f(out))
}

func (s *rs1State) rehydrates() error { s.b.rehydrateNodes(); return nil }

func (s *rs1State) ownerRaisesOut(name, out string) error {
	st := s.st(name)
	s.setPrice(name, st.priceIn, rs1f(out))
	return nil
}

// --- When: the request family -----------------------------------------------------------

func (s *rs1State) req(caller, kind, model string) rs1Req {
	q := rs1Req{caller: "user", kind: "chat", model: model, headers: map[string]string{}}
	switch caller {
	case "an anonymous caller":
		q.caller = "anon"
	case "the grant holder":
		q.caller = "grant"
	case "the Playbox origin":
		q.caller = "origin"
	}
	switch kind {
	case "a STREAMING chat completion":
		q.kind = "stream"
	case "an illegal prompt":
		q.kind = "illegal"
	}
	return q
}

func (s *rs1State) posts(c, k, model string) error { return s.fire(s.req(c, k, model)) }

func (s *rs1State) postsBody(c, k, model, frag string) error {
	q := s.req(c, k, model)
	q.frag = frag
	return s.fire(q)
}

func (s *rs1State) postsDoc(c, k, model string, doc *godog.DocString) error {
	return s.postsBody(c, k, model, doc.Content)
}

func (s *rs1State) postsHeaderBody(c, k, model, h, v, frag string) error {
	q := s.req(c, k, model)
	q.headers[h], q.frag = v, frag
	return s.fire(q)
}

func (s *rs1State) postsHeader(c, k, model, h, v string) error {
	return s.postsHeaderBody(c, k, model, h, v, "")
}

func (s *rs1State) postsMaxBody(c, k, model, max, frag string) error {
	q := s.req(c, k, model)
	q.maxTokens, q.frag = rs1i(max), frag
	return s.fire(q)
}

func (s *rs1State) postsMax(c, k, model, max string) error {
	return s.postsMaxBody(c, k, model, max, "")
}

func (s *rs1State) postsPromptBody(c, k, model, tokens, frag string) error {
	q := s.req(c, k, model)
	q.promptTokens, q.frag = rs1i(tokens), frag
	return s.fire(q)
}

func (s *rs1State) postsPrompt(c, k, model, tokens string) error {
	return s.postsPromptBody(c, k, model, tokens, "")
}

func (s *rs1State) postsPromptMaxBody(c, k, model, tokens, max, frag string) error {
	q := s.req(c, k, model)
	q.promptTokens, q.maxTokens, q.frag = rs1i(tokens), rs1i(max), frag
	return s.fire(q)
}

func rs1ListFrag(path string, n int) string {
	entries := make([]string, n)
	for i := range entries {
		switch path {
		case "roger.require": // the closed set has two values; the length rule is what is under test
			entries[i] = []string{"tools", "vision"}[i%2]
		case "roger.region":
			entries[i] = string([]byte{'a' + byte(i/26), 'a' + byte(i%26)})
		case "provider.quantizations":
			entries[i] = fmt.Sprintf("Q%d_0", i)
		default:
			entries[i] = fmt.Sprintf("x%d", i)
		}
	}
	list, _ := json.Marshal(entries)
	parts := strings.SplitN(path, ".", 2)
	return fmt.Sprintf(`%q: {%q: %s}`, parts[0], parts[1], list)
}

func (s *rs1State) postsList(c, k, model, path, n string) error {
	return s.postsBody(c, k, model, rs1ListFrag(path, rs1i(n)))
}

func (s *rs1State) postsRawFor(c, k, _, raw string) error { return s.postsRaw(c, k, raw) }

func (s *rs1State) postsRaw(c, k, raw string) error {
	q := s.req(c, k, "")
	q.raw = raw
	return s.fire(q)
}

func (s *rs1State) postsNoModel(c, k, _, frag string) error {
	q := s.req(c, k, "")
	q.noModel, q.frag = true, frag
	return s.fire(q)
}

func (s *rs1State) postsNonObjectRS1(c, k string) error { return s.postsRaw(c, k, `[1, 2, 3]`) }

func (s *rs1State) postsPadded(c, k, model, _, mib string) error {
	return s.postsBody(c, k, model, `"roger": {"pref": "cheap", "pad": "`+strings.Repeat("a", rs1i(mib)<<20)+`"}`)
}

func (s *rs1State) postsBigIgnore(c, k, model, n, chars string) error {
	ids := make([]string, rs1i(n))
	for i := range ids {
		ids[i] = fmt.Sprintf("%0*d", rs1i(chars), i)
	}
	list, _ := json.Marshal(ids)
	return s.postsBody(c, k, model, `"provider": {"ignore": `+string(list)+`}`)
}

func (s *rs1State) postsTimes(c, k, model, n string) error {
	s.batch = nil
	for i := 0; i < rs1i(n); i++ {
		if err := s.posts(c, k, model); err != nil {
			return err
		}
	}
	return nil
}

func (s *rs1State) postsServedBy(c, k, model, name, out string) error {
	// "is served by X": the scenario fixes WHICH station opens the price lock; the pin
	// header is how a consumer says that today.
	if err := s.postsHeader(c, k, model, "X-Roger-Node", name); err != nil {
		return err
	}
	if err := s.servedNode(name); err != nil {
		return err
	}
	return s.settledPriceOut(out)
}

func (s *rs1State) postsAt(hh, mm, c, k, model string) error {
	s.shiftClockTo(rs1i(hh), rs1i(mm))
	return s.posts(c, k, model)
}

// receiptClaims makes every on-air station claim n completion tokens and re-runs the
// scenario's request, so the settle under test is the over-claimed one.
func (s *rs1State) receiptClaims(n string) error {
	for name := range s.stations {
		if s.offAir[name] {
			continue
		}
		s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":` + n + `}}`))
		})
	}
	return s.fire(s.last)
}

func (s *rs1State) publicFeedFetched() error {
	r := httptest.NewRequest(http.MethodGet, "/discover", nil)
	w := httptest.NewRecorder()
	s.b.discover(w, r)
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	return nil
}

// --- Then: the response -----------------------------------------------------------------

func (s *rs1State) errParts() (code, msg string) { return s.errBody() }

func (s *rs1State) responseIsNot(code string) error {
	if s.lastCode == rs1i(code) {
		return fmt.Errorf("status %d (%s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *rs1State) monthlyCapRefusal() error {
	if s.lastCode != http.StatusPaymentRequired {
		return fmt.Errorf("status %d, want 402 (%s)", s.lastCode, s.lastBody)
	}
	if _, msg := s.errParts(); !strings.Contains(msg, "monthly spend limit") {
		return fmt.Errorf("message %q is not the monthly-cap refusal", msg)
	}
	return nil
}

func (s *rs1State) moderationRefusal() error {
	if s.lastCode != http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("status %d, want 451 (%s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *rs1State) errorCodeIsNot(code string) error {
	if got, _ := s.errParts(); got == code {
		return fmt.Errorf("error.code is %q (%s)", got, s.lastBody)
	}
	return nil
}

func (s *rs1State) errorNames(key string) error {
	_, msg := s.errParts()
	if strings.Contains(msg, key) || strings.Contains(msg, s.id(key)) {
		return nil
	}
	return fmt.Errorf("message %q does not name %q", msg, key)
}

func (s *rs1State) errorMessageIs(want string) error {
	if _, msg := s.errParts(); msg != want {
		return fmt.Errorf("message %q, want %q", msg, want)
	}
	return nil
}

func (s *rs1State) errorMessageContains(part string) error {
	if _, msg := s.errParts(); !strings.Contains(msg, part) {
		return fmt.Errorf("message %q does not contain %q", msg, part)
	}
	return nil
}

func (s *rs1State) errorMessageStarts(part string) error {
	if _, msg := s.errParts(); !strings.HasPrefix(msg, part) {
		return fmt.Errorf("message %q does not start with %q", msg, part)
	}
	return nil
}

func (s *rs1State) errorMessageEnds(part string) error {
	if _, msg := s.errParts(); !strings.HasSuffix(msg, part) {
		return fmt.Errorf("message %q does not end with %q", msg, part)
	}
	return nil
}

func (s *rs1State) errorSaysLogIn() error { return s.errorMessageContains("log in to spend") }

func (s *rs1State) errorNamesOnlyAsOrderEntry(name string) error {
	_, msg := s.errParts()
	if !strings.Contains(msg, name) && !strings.Contains(msg, s.id(name)) {
		return fmt.Errorf("message %q does not name %q", msg, name)
	}
	if !strings.Contains(msg, "provider.order") {
		return fmt.Errorf("message %q does not say it is the ORDER entry", msg)
	}
	low := strings.ToLower(msg)
	for _, leak := range []string{"exist", "unknown node", "not found", "no such", "not registered", "offline", "on air"} {
		if strings.Contains(low, leak) {
			return fmt.Errorf("message %q speaks to whether the node exists (%q)", msg, leak)
		}
	}
	return nil
}

func (s *rs1State) errorByteIdenticalTo(frag string) error {
	_, got := s.errParts()
	q := s.last
	q.frag = frag
	other := s.replay(q)
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(other.body, &env)
	if other.code != s.lastCode || env.Error.Message != got {
		return fmt.Errorf("private-station answer %d %q differs from the unknown-id answer %d %q", s.lastCode, got, other.code, env.Error.Message)
	}
	return nil
}

func (s *rs1State) headerIs(name, want string) error {
	if got := s.lastHdr.Get(name); got != want {
		return fmt.Errorf("%s = %q, want %q (status %d %s)", name, got, want, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *rs1State) modelHeaderIs(want string) error { return s.headerIs("X-RogerAI-Model", want) }
func (s *rs1State) costHeaderIs(want string) error  { return s.headerIs("X-RogerAI-Cost", want) }
func (s *rs1State) retryAfterHeaderIs(want string) error {
	return s.headerIs("Retry-After", want)
}

func (s *rs1State) carriesNoHeader(name string) error {
	if got := s.lastHdr.Get(name); got != "" {
		return fmt.Errorf("the response carries %s = %q", name, got)
	}
	return nil
}

func (s *rs1State) contentTypeIs(want string) error {
	if got := s.lastHdr.Get("Content-Type"); !strings.HasPrefix(got, want) {
		return fmt.Errorf("Content-Type %q, want %q", got, want)
	}
	return nil
}

func (s *rs1State) exposeIncludes(name string) error {
	for _, v := range s.lastHdr.Values("Access-Control-Expose-Headers") {
		for _, h := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(h), name) {
				return nil
			}
		}
	}
	return fmt.Errorf("Access-Control-Expose-Headers %q does not include %s (a browser cannot read it)", s.lastHdr.Values("Access-Control-Expose-Headers"), name)
}

func (s *rs1State) errorEnvelopeTwo(obj, f1, f2 string) error {
	if err := s.bodyIsErrorObject(obj, f1); err != nil {
		return err
	}
	return s.bodyIsErrorObject(obj, f2)
}

// --- Then: who served -------------------------------------------------------------------

func (s *rs1State) servedNode(name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	if got := s.lastHdr.Get("X-RogerAI-Provider"); got != s.id(name) {
		return fmt.Errorf("served node is %q, want %q", s.nameOfID(got), name)
	}
	return nil
}

func (s *rs1State) servedOneOf(names ...string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	got := s.lastHdr.Get("X-RogerAI-Provider")
	for _, n := range names {
		if got == s.id(n) {
			return nil
		}
	}
	return fmt.Errorf("served node is %q, want one of %v", s.nameOfID(got), names)
}

func (s *rs1State) servedOneOf2(a, b string) error    { return s.servedOneOf(a, b) }
func (s *rs1State) servedOneOf3(a, b, c string) error { return s.servedOneOf(a, b, c) }

func (s *rs1State) everyServedBy(name string) error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	for i, r := range s.batch {
		if r.code != 200 || r.hdr.Get("X-RogerAI-Provider") != s.id(name) {
			return fmt.Errorf("relay %d/%d: %d served by %q (relay=%q), want %q", i+1, len(s.batch), r.code,
				s.nameOfID(r.hdr.Get("X-RogerAI-Provider")), r.hdr.Get("X-RogerAI-Relay"), name)
		}
	}
	return nil
}

func (s *rs1State) neverCandidate(name string) error {
	if got := s.lastHdr.Get("X-RogerAI-Provider"); got != "" && got == s.id(name) {
		return fmt.Errorf("%q served", name)
	}
	if n := s.shot.counts[name]; n != 0 {
		return fmt.Errorf("%q received %d upstream request(s); it must never have been a candidate", name, n)
	}
	// A station the request never reached could still have been in the plan: the plan
	// itself must not hold it either.
	order, _, _ := s.probePlan()
	for _, n := range order {
		if n == name {
			return fmt.Errorf("%q is in the failover plan %v", name, order)
		}
	}
	return nil
}

func (s *rs1State) noStationDispatched() error {
	for name, n := range s.shot.counts {
		if n != 0 {
			return fmt.Errorf("%q received %d upstream request(s), want none", name, n)
		}
	}
	for _, tw := range s.rsTowers {
		if tw.jobs() != 0 {
			return fmt.Errorf("Tower %q received %d job(s), want none", tw.name, tw.jobs())
		}
	}
	return nil
}

// rs1ReceivedNothing: the named station saw no upstream request for the last shot.
func (s *rs1State) rs1ReceivedNothing(name string) error {
	if n := s.shot.counts[name]; n != 0 {
		return fmt.Errorf("%q received %d upstream request(s), want none", name, n)
	}
	return nil
}

// rs1NoBandCodeLeaks: no band code of the scenario (alias, the real minted code, or its
// tail) appears in the response body or in any broker log line.
func (s *rs1State) rs1NoBandCodeLeaks() error {
	if len(s.codes) == 0 {
		return fmt.Errorf("fixture: the scenario minted no band code to look for")
	}
	out := string(s.lastBody) + "\n" + s.logs.String()
	for alias, code := range s.codes {
		for _, needle := range []string{alias, code, protocol.CanonicalBandTail(code)} {
			if needle != "" && strings.Contains(out, needle) {
				return fmt.Errorf("band code %q appears in the response or a log line", alias)
			}
		}
	}
	return nil
}

func (s *rs1State) noHoldRS1() error {
	if s.shot.holdN != 0 {
		return fmt.Errorf("%d hold(s) placed on %s, want none", s.shot.holdN, s.shot.payer)
	}
	return nil
}

func (s *rs1State) noHoldNoDispatch() error {
	if err := s.noHoldRS1(); err != nil {
		return err
	}
	return s.noStationDispatched()
}

func (s *rs1State) noFailoverPlanned() error {
	served := s.nameOfID(s.lastHdr.Get("X-RogerAI-Provider"))
	st, ok := s.stations[served]
	if !ok {
		return fmt.Errorf("no station served (status %d)", s.lastCode)
	}
	// Make the served station refuse and replay: with no failover planned the consumer sees
	// that refusal and no other station is touched.
	s.clearCooling()
	st.mu.Lock()
	prev := st.script
	st.mu.Unlock()
	st.script429("")
	before := map[string]int{}
	for n, o := range s.stations {
		before[n] = o.upstreamCount()
	}
	shot := s.replay(s.last)
	st.set(prev)
	s.clearCooling()
	if shot.code != 429 {
		return fmt.Errorf("with %q refusing, the consumer saw %d (%s): a failover WAS planned", served, shot.code, shot.body)
	}
	for n, o := range s.stations {
		if n != served && o.upstreamCount() != before[n] {
			return fmt.Errorf("%q was tried after %q refused: a failover WAS planned", n, served)
		}
	}
	return nil
}

func (s *rs1State) planContains(names ...string) error {
	order, _, _ := s.probePlan()
	for _, want := range names {
		found := false
		for _, n := range order {
			found = found || n == want
		}
		if !found {
			return fmt.Errorf("the failover plan %v does not contain %q", order, want)
		}
	}
	return nil
}

func (s *rs1State) planContains2(a, b string) error    { return s.planContains(a, b) }
func (s *rs1State) planContains3(a, b, c string) error { return s.planContains(a, b, c) }

func (s *rs1State) planLacks(name string) error {
	order, _, _ := s.probePlan()
	for _, n := range order {
		if n == name {
			return fmt.Errorf("the failover plan %v contains %q", order, name)
		}
	}
	return nil
}

func (s *rs1State) orderedPlanIs(a, b, c string) error {
	order, _, _ := s.probePlan()
	want := []string{a, b, c}
	if len(order) < 3 {
		return fmt.Errorf("the plan is %v, want %v", order, want)
	}
	for i, n := range want {
		if order[i] != n {
			return fmt.Errorf("the plan is %v, want it to begin %v", order, want)
		}
	}
	return nil
}

func (s *rs1State) lastInPlan(name string) error {
	order, _, _ := s.probePlan()
	if len(order) == 0 || order[len(order)-1] != name {
		return fmt.Errorf("the ordered plan is %v, want %q last", order, name)
	}
	return nil
}

func rs1MaxTokens(body []byte) (float64, bool) {
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	v, ok := m["max_tokens"].(float64)
	return v, ok
}

func (s *rs1State) planMaxTokensSmaller(a, b string) error {
	_, bodies, _ := s.probePlan()
	ma, oka := rs1MaxTokens(bodies[a])
	mb, okb := rs1MaxTokens(bodies[b])
	if !oka || !okb {
		return fmt.Errorf("the plan carried no max_tokens for %q (%v) / %q (%v)", a, oka, b, okb)
	}
	if ma >= mb {
		return fmt.Errorf("max_tokens for %q is %v, not smaller than %v for %q", a, ma, mb, b)
	}
	return nil
}

func (s *rs1State) repeatsPickSame(n string) error {
	first := ""
	for i := 0; i < rs1i(n); i++ {
		shot := s.replay(s.last)
		if shot.code != 200 {
			return fmt.Errorf("repeat %d = %d (%s)", i+1, shot.code, shot.body)
		}
		got := shot.hdr.Get("X-RogerAI-Provider")
		if first == "" {
			first = got
		} else if got != first {
			return fmt.Errorf("repeat %d picked %q after %q: the pick is spread, not strictly sorted", i+1, s.nameOfID(got), s.nameOfID(first))
		}
	}
	return nil
}

func (s *rs1State) noFailedAttemptFor(model string) error {
	for _, name := range s.shot.hits {
		if st := s.stations[name]; st != nil && st.model == model {
			return fmt.Errorf("a station serving %q (%s) was attempted", model, name)
		}
	}
	return nil
}

// --- Then: the routing pass (counters and the routing log line) ---------------------------

func (s *rs1State) liveNum(key string) (float64, bool, error) {
	if err := s.readAdminLive(); err != nil {
		return 0, false, err
	}
	v, ok := s.adminResp[key].(float64)
	return v, ok, nil
}

func (s *rs1State) liveDelta(key string) (float64, error) {
	v, ok, err := s.liveNum(key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("/admin/live carries no %s counter", key)
	}
	before, _ := s.admin0[key].(float64)
	return v - before, nil
}

func (s *rs1State) ranWithPref(pref string) error {
	d, err := s.liveDelta("routing_pref_" + pref)
	if err != nil {
		return err
	}
	if d < 1 {
		return fmt.Errorf("routing_pref_%s moved by %v after the request; the routing pass did not run with %q", pref, d, pref)
	}
	for _, other := range []string{"cheap", "balanced", "fast", "reliable"} {
		if other == pref {
			continue
		}
		if od, err := s.liveDelta("routing_pref_" + other); err == nil && od > 0 {
			return fmt.Errorf("routing_pref_%s moved by %v, but the request asked for %q", other, od, pref)
		}
	}
	return nil
}

func (s *rs1State) noRoutingPass() error {
	for _, p := range []string{"cheap", "balanced", "fast", "reliable"} {
		if d, err := s.liveDelta("routing_pref_" + p); err == nil && d != 0 {
			return fmt.Errorf("a routing pass ran (routing_pref_%s moved by %v)", p, d)
		}
	}
	return nil
}

var rs1KV = regexp.MustCompile(`([a-z_]+)=([^\s;,]+)`)

// routingLine returns the key=value pairs of the routing log line the last scenario relay
// wrote ("routing ... sort=... max_in=... max_out=... min_tps=... reward_out=...").
func (s *rs1State) routingLine() (map[string]string, error) {
	all := s.logs.String()
	from, to := s.shot.logFrom, s.shot.logTo
	if to > len(all) {
		to = len(all)
	}
	if from > to {
		from = to
	}
	for _, line := range strings.Split(all[from:to], "\n") {
		i := strings.Index(line, "routing ")
		if i < 0 || !strings.Contains(line[i:], "=") {
			continue
		}
		if i > 0 && line[i-1] != ' ' && line[i-1] != ']' {
			continue
		}
		kv := map[string]string{}
		for _, m := range rs1KV.FindAllStringSubmatch(line[i:], -1) {
			kv[m[1]] = m[2]
		}
		if _, ok := kv["sort"]; ok {
			return kv, nil
		}
	}
	return nil, fmt.Errorf("the relay wrote no routing log line (a line beginning \"routing \" with sort= / max_in= / max_out= / min_tps= / reward_out=)")
}

func (s *rs1State) routingNum(key string, want float64) error {
	kv, err := s.routingLine()
	if err != nil {
		return err
	}
	raw, ok := kv[key]
	if !ok {
		return fmt.Errorf("the routing log line carries no %s= (%v)", key, kv)
	}
	if got, perr := strconv.ParseFloat(raw, 64); perr != nil || math.Abs(got-want) > 1e-9 {
		return fmt.Errorf("the routing pass ran with %s=%s, want %v", key, raw, want)
	}
	return nil
}

func (s *rs1State) ranWithOutCap(v string) error    { return s.routingNum("max_out", rs1f(v)) }
func (s *rs1State) ranWithInCap(v string) error     { return s.routingNum("max_in", rs1f(v)) }
func (s *rs1State) ranWithMinTPS(v string) error    { return s.routingNum("min_tps", rs1f(v)) }
func (s *rs1State) ranWithRewardOut(v string) error { return s.routingNum("reward_out", rs1f(v)) }

func (s *rs1State) routingLineSort(want string) error {
	kv, err := s.routingLine()
	if err != nil {
		return err
	}
	if kv["sort"] != want {
		return fmt.Errorf("the routing log line names sort %q, want %q", kv["sort"], want)
	}
	return nil
}

func (s *rs1State) strictSortIncreased(n string) error {
	d, err := s.liveDelta("routing_strict_sort")
	if err != nil {
		return err
	}
	if d != rs1f(n) {
		return fmt.Errorf("routing_strict_sort moved by %v, want %s", d, n)
	}
	return nil
}

func (s *rs1State) coinFlipsUnchanged() error {
	d, err := s.liveDelta("edge_coin_flips")
	if err != nil {
		return err
	}
	if d != 0 {
		return fmt.Errorf("edge_coin_flips moved by %v: the fan-out coin was consulted under a strict sort", d)
	}
	return nil
}

func (s *rs1State) variantCounters(free, floor, nitro string) error {
	for key, want := range map[string]string{"variant_free": free, "variant_floor": floor, "variant_nitro": nitro} {
		d, err := s.liveDelta(key)
		if err != nil {
			return err
		}
		if d != rs1f(want) {
			return fmt.Errorf("%s moved by %v, want %s", key, d, want)
		}
	}
	return nil
}

func (s *rs1State) bodyCounters(reqs, rejects string) error {
	for key, want := range map[string]string{"routing_body_requests": reqs, "routing_body_rejects": rejects} {
		d, err := s.liveDelta(key)
		if err != nil {
			return err
		}
		if d != rs1f(want) {
			return fmt.Errorf("%s moved by %v, want %s", key, d, want)
		}
	}
	return nil
}

func (s *rs1State) adminEchoesNothing() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	for k, v := range s.adminResp {
		if !strings.HasPrefix(k, "routing_") && !strings.HasPrefix(k, "variant_") {
			continue
		}
		if _, isNum := v.(float64); !isNum {
			return fmt.Errorf("/admin/live %s carries a non-numeric value %v (a request value echoed)", k, v)
		}
	}
	return nil
}

// --- Then: what the station / Tower / bus received -----------------------------------------

func (s *rs1State) upstreamBody() (map[string]json.RawMessage, []byte, error) {
	body := s.lastUpstreamBody()
	if body == nil {
		return nil, nil, fmt.Errorf("no station received a body (status %d %s)", s.lastCode, s.lastBody)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, body, fmt.Errorf("the station body is not a JSON object: %v", err)
	}
	return m, body, nil
}

func (s *rs1State) stationOnlyKeys(a, b string) error {
	m, body, err := s.upstreamBody()
	if err != nil {
		return err
	}
	if len(m) != 2 || m[a] == nil || m[b] == nil {
		return fmt.Errorf("the station received keys other than %q, %q: %s", a, b, body)
	}
	return nil
}

func (s *rs1State) stationNoKey(key string) error {
	m, body, err := s.upstreamBody()
	if err != nil {
		return err
	}
	if _, has := m[key]; has {
		return fmt.Errorf("the station received top-level key %q: %.300s", key, body)
	}
	return nil
}

func (s *rs1State) stationKeyByteIdentical(key string) error {
	m, _, err := s.upstreamBody()
	if err != nil {
		return err
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(s.shot.sent, &sent); err != nil {
		return fmt.Errorf("the sent body does not decode: %v", err)
	}
	if !bytes.Equal(m[key], sent[key]) {
		return fmt.Errorf("the station received %s=%s, the consumer sent %s", key, m[key], sent[key])
	}
	return nil
}

func (s *rs1State) stationObjectTrue(obj, field string) error {
	m, body, err := s.upstreamBody()
	if err != nil {
		return err
	}
	var o map[string]any
	_ = json.Unmarshal(m[obj], &o)
	if v, _ := o[field].(bool); !v {
		return fmt.Errorf("the station body has no %s.%s true: %.300s", obj, field, body)
	}
	return nil
}

func (s *rs1State) stationOwnMaxTokens(n string) error {
	_, body, err := s.upstreamBody()
	if err != nil {
		return err
	}
	if got, ok := rs1MaxTokens(body); !ok || got != rs1f(n) {
		return fmt.Errorf("the station received max_tokens %v (present=%v), want %s", got, ok, n)
	}
	return nil
}

func (s *rs1State) stationMaxTokensWithin(key, capUSD, outPrice string) error {
	_, body, err := s.upstreamBody()
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	got, ok := m[key].(float64)
	if !ok {
		return fmt.Errorf("the station received no %s", key)
	}
	buys := rs1f(capUSD) / rs1f(outPrice) * 1e6 // before the prompt's input cost: an upper bound
	if got > buys {
		return fmt.Errorf("the station was told %s=%v, more than the %v tokens $%s buys at $%s/1M", key, got, buys, capUSD, outPrice)
	}
	if sent, _ := rs1MaxTokens(s.shot.sent); got >= sent {
		return fmt.Errorf("the station was told %s=%v, not lowered from the request's %v", key, got, sent)
	}
	return nil
}

func (s *rs1State) oneTower() (*rs1Tower, error) {
	for _, tw := range s.rsTowers {
		return tw, nil
	}
	return nil, fmt.Errorf("no Tower in this scenario")
}

func (s *rs1State) towerLastBody() (map[string]json.RawMessage, error) {
	tw, err := s.oneTower()
	if err != nil {
		return nil, err
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.bodies) == 0 {
		return nil, fmt.Errorf("the Tower received no job (status %d %s)", s.lastCode, s.lastBody)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(tw.bodies[len(tw.bodies)-1], &m); err != nil {
		return nil, fmt.Errorf("the Tower body is not a JSON object: %v", err)
	}
	return m, nil
}

func (s *rs1State) towerNoKey(key string) error {
	m, err := s.towerLastBody()
	if err != nil {
		return err
	}
	if _, has := m[key]; has {
		return fmt.Errorf("the Tower received top-level key %q", key)
	}
	return nil
}

func (s *rs1State) towerKeyValue(key, want string) error {
	m, err := s.towerLastBody()
	if err != nil {
		return err
	}
	var got string
	_ = json.Unmarshal(m[key], &got)
	if got != want {
		return fmt.Errorf("the Tower received %s=%s, want %q", key, m[key], want)
	}
	return nil
}

func (s *rs1State) towerNoJob() error {
	tw, err := s.oneTower()
	if err != nil {
		return err
	}
	if n := tw.jobs(); n != 0 {
		return fmt.Errorf("the Tower received %d job(s), want none", n)
	}
	return nil
}

func (s *rs1State) towerEveryJob() error {
	tw, err := s.oneTower()
	if err != nil {
		return err
	}
	if n := tw.jobs(); n != len(s.batch) || n == 0 {
		return fmt.Errorf("the Tower received %d of %d job(s)", n, len(s.batch))
	}
	return nil
}

func (s *rs1State) busJobNoKey(key string) error {
	if s.xi == nil || s.xi.node == nil {
		return fmt.Errorf("no cross-instance fabric in this scenario")
	}
	s.xi.node.pollWG.Wait()
	s.xi.node.mu.Lock()
	job := s.xi.node.polled
	s.xi.node.mu.Unlock()
	if job == nil {
		return fmt.Errorf("the peer's poller received no job (relay %d %s)", s.lastCode, s.lastBody)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(job.Body, &m); err != nil {
		return fmt.Errorf("the job body is not a JSON object: %v", err)
	}
	if _, has := m[key]; has {
		return fmt.Errorf("the job that crossed the bus carried top-level key %q: %.300s", key, job.Body)
	}
	return nil
}

// --- Then: money --------------------------------------------------------------------------

func (s *rs1State) holdEqualsPriciest(name string) error {
	if s.shot.holdN != 1 {
		return fmt.Errorf("%d hold(s) placed, want exactly one", s.shot.holdN)
	}
	want := holdCostFor(pricingPlan{}, s.offerOf(name), s.shot.base, time.Now())
	if math.Abs(s.shot.holdAmt-want) > 1e-9 {
		return fmt.Errorf("hold %.9f, want %.9f (estimateMaxCost of the stripped body at %s)", s.shot.holdAmt, want, name)
	}
	return nil
}

func (s *rs1State) holdIs(amount string) error {
	if s.shot.holdN != 1 {
		return fmt.Errorf("%d hold(s) placed, want exactly one", s.shot.holdN)
	}
	if want := rs1f(amount); math.Abs(s.shot.holdAmt-want) > 1e-9 {
		return fmt.Errorf("hold %.9f, want %v", s.shot.holdAmt, want)
	}
	return nil
}

func (s *rs1State) holdAtMost(amount string) error {
	if s.shot.holdN != 1 {
		return fmt.Errorf("%d hold(s) placed on %s, want exactly one", s.shot.holdN, s.shot.payer)
	}
	if max := rs1f(amount); s.shot.holdAmt > max+1e-9 {
		return fmt.Errorf("hold %.9f exceeds %v", s.shot.holdAmt, max)
	}
	return nil
}

func (s *rs1State) settledCost() (float64, error) {
	if s.lastCode != 200 {
		return 0, fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	raw := s.lastHdr.Get("X-RogerAI-Cost")
	if raw == "" {
		return 0, fmt.Errorf("the response carries no X-RogerAI-Cost")
	}
	return strconv.ParseFloat(raw, 64)
}

func (s *rs1State) settledCostAtMost(amount string) error {
	c, err := s.settledCost()
	if err != nil {
		return err
	}
	if c > rs1f(amount)+1e-9 {
		return fmt.Errorf("settled cost %v exceeds %s", c, amount)
	}
	return nil
}

func (s *rs1State) settledCostIs(amount string) error {
	c, err := s.settledCost()
	if err != nil {
		return err
	}
	if math.Abs(c-rs1f(amount)) > 1e-9 {
		return fmt.Errorf("settled cost %v, want %s", c, amount)
	}
	return nil
}

var rs1PriceOut = regexp.MustCompile(`out=([0-9.eE+-]+)`)

func (s *rs1State) priceOut() (float64, error) {
	if s.lastCode != 200 {
		return 0, fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	m := rs1PriceOut.FindStringSubmatch(s.lastHdr.Get("X-RogerAI-Price"))
	if m == nil {
		return 0, fmt.Errorf("X-RogerAI-Price %q carries no out=", s.lastHdr.Get("X-RogerAI-Price"))
	}
	return strconv.ParseFloat(m[1], 64)
}

func (s *rs1State) settledPriceOut(want string) error {
	got, err := s.priceOut()
	if err != nil {
		return err
	}
	if math.Abs(got-rs1f(want)) > 1e-9 {
		return fmt.Errorf("settled price out %v, want %s", got, want)
	}
	return nil
}

func (s *rs1State) settledPriceOutAtMost(want string) error {
	got, err := s.priceOut()
	if err != nil {
		return err
	}
	if got > rs1f(want)+1e-9 {
		return fmt.Errorf("settled price out %v exceeds %s", got, want)
	}
	return nil
}

func (s *rs1State) settledPriceIsOf(name string) error {
	got, err := s.priceOut()
	if err != nil {
		return err
	}
	if want := s.offerOf(name).PriceOut; math.Abs(got-want) > 1e-9 {
		return fmt.Errorf("settled price out %v, want %s's %v", got, name, want)
	}
	return nil
}

func (s *rs1State) receipt() (protocol.UsageReceipt, error) {
	enc := s.lastHdr.Get("X-RogerAI-Receipt")
	if enc == "" {
		return protocol.UsageReceipt{}, fmt.Errorf("the response carries no X-RogerAI-Receipt (status %d %s)", s.lastCode, s.lastBody)
	}
	return protocol.DecodeReceipt(enc)
}

func (s *rs1State) receiptNamesModel(model string) error {
	rec, err := s.receipt()
	if err != nil {
		return err
	}
	if rec.Model != model {
		return fmt.Errorf("the receipt names model %q, want %q", rec.Model, model)
	}
	return nil
}

func (s *rs1State) receiptBrokerSigBare() error {
	rec, err := s.receipt()
	if err != nil {
		return err
	}
	pub := hex.EncodeToString(s.b.priv.Public().(ed25519.PublicKey))
	if !rec.VerifyBroker(pub) {
		return fmt.Errorf("the receipt's broker signature does not verify")
	}
	if strings.Contains(rec.Model, ":free") || strings.Contains(rec.Model, ":floor") || strings.Contains(rec.Model, ":nitro") {
		return fmt.Errorf("the signed receipt names the suffixed model %q", rec.Model)
	}
	return nil
}

func (s *rs1State) receiptChainTwo(model string) error {
	var recs []protocol.UsageReceipt
	for _, shot := range s.shots {
		enc := shot.hdr.Get("X-RogerAI-Receipt")
		if shot.code != 200 || enc == "" {
			return fmt.Errorf("a request was not served with a receipt (%d %s)", shot.code, shot.body)
		}
		rec, err := protocol.DecodeReceipt(enc)
		if err != nil {
			return err
		}
		recs = append(recs, rec)
	}
	if len(recs) != 2 {
		return fmt.Errorf("%d receipts, want two", len(recs))
	}
	for _, rec := range recs {
		if rec.Model != model {
			return fmt.Errorf("a receipt names model %q, want %q", rec.Model, model)
		}
	}
	if recs[0].NodeID == recs[1].NodeID && recs[1].PrevHash != recs[0].Hash() {
		return fmt.Errorf("the second receipt on %s does not chain onto the first", recs[0].NodeID)
	}
	return nil
}

func (s *rs1State) usageChunkNamesModel(model string) error {
	chunk, err := s.usageChunk()
	if err != nil {
		return err
	}
	usage, _ := chunk["usage"].(map[string]any)
	rog, _ := usage["rogerai"].(map[string]any)
	if got, _ := rog["model"].(string); got != model {
		return fmt.Errorf("the usage chunk names model %q, want %q (%v)", got, model, chunk)
	}
	return nil
}

func (s *rs1State) consoleListsModel(_, model string) error {
	rid := ""
	if rec, err := s.receipt(); err == nil {
		rid = rec.RequestID
	}
	r := httptest.NewRequest(http.MethodGet, "/console", nil)
	signReq(r, s.consumerPriv, nil)
	w := httptest.NewRecorder()
	s.b.console(w, r)
	if w.Code != 200 {
		return fmt.Errorf("GET /console = %d: %s", w.Code, w.Body.String())
	}
	var doc any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		return err
	}
	found := false
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if m, _ := t["model"].(string); m == model {
				if id, _ := t["request_id"].(string); rid == "" || id == "" || id == rid {
					found = true
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(doc)
	if !found {
		return fmt.Errorf("/console lists no request under model %q: %.600s", model, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(model+":")) {
		return fmt.Errorf("/console carries a suffixed spelling of %q", model)
	}
	return nil
}

// --- Then: moderation, registration, discovery, the rest -----------------------------------

func (s *rs1State) moderationRanOnce() error {
	if s.shot.modN != 1 {
		return fmt.Errorf("the classifier was called %d time(s) for the request, want exactly once", s.shot.modN)
	}
	return nil
}

func (s *rs1State) moderationDidNotRun() error {
	if s.shot.modN != 0 {
		return fmt.Errorf("the classifier was called %d time(s); a malformed routing body must not reach it", s.shot.modN)
	}
	return nil
}

func (s *rs1State) moderationIdenticalText() error {
	if s.shot.modN == 0 {
		return fmt.Errorf("the classifier was not called for the request")
	}
	q := s.last
	q.frag = ""
	plain := s.replay(q)
	if plain.modN == 0 {
		return fmt.Errorf("the classifier was not called for the request without the routing body")
	}
	if plain.modText != s.shot.modText {
		return fmt.Errorf("the screened text differs with the routing body (%d vs %d bytes)", len(s.shot.modText), len(plain.modText))
	}
	return nil
}

func (s *rs1State) promptEstimateWithin(pct string) error {
	with, without := approxPromptTokens(s.shot.sent), approxPromptTokens(s.shot.base)
	if without == 0 {
		return fmt.Errorf("no prompt estimate for the plain body")
	}
	if d := math.Abs(float64(with-without)) / float64(without) * 100; d > rs1f(pct) {
		return fmt.Errorf("prompt estimate %d with the routing body vs %d without (%.1f%% apart)", with, without, d)
	}
	// The stripped body the station was sent must carry the same messages.
	m, _, err := s.upstreamBody()
	if err != nil {
		return err
	}
	var base map[string]json.RawMessage
	_ = json.Unmarshal(s.shot.base, &base)
	var a, b any
	_ = json.Unmarshal(m["messages"], &a)
	_ = json.Unmarshal(base["messages"], &b)
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(ab, bb) {
		return fmt.Errorf("the station received different messages than the consumer sent")
	}
	return nil
}

func (s *rs1State) screenerBeforeRouting() error {
	if s.b.scr == nil {
		return fmt.Errorf("no off-path screener is installed")
	}
	s.b.scr.mu.Lock()
	queued := len(s.b.scr.q)
	s.b.scr.mu.Unlock()
	if queued == 0 || s.scrSeenBeforeRouting == nil {
		return fmt.Errorf("the off-path screener received no job")
	}
	if !*s.scrSeenBeforeRouting {
		return fmt.Errorf("the screening job was enqueued AFTER a routing pass had run")
	}
	return s.ranWithPref("cheap")
}

func (s *rs1State) screeningJobModel(model string) error {
	if err := s.moderationMode("async"); err != nil {
		return err
	}
	s.replay(s.last)
	s.b.scr.mu.Lock()
	defer s.b.scr.mu.Unlock()
	if len(s.b.scr.q) == 0 {
		// The screener drops the job of a request nothing was dispatched for.
		return fmt.Errorf("no screening job is queued for the request (it answered %d %.200s)", s.lastCode, s.lastBody)
	}
	if got := s.b.scr.q[len(s.b.scr.q)-1].model; got != model {
		return fmt.Errorf("the screening job was submitted with model %q, want %q", got, model)
	}
	return nil
}

func (s *rs1State) nextRequestRoutedByScore(_, model, pinned string) error {
	// A pin would return the pinned station's refusal; a scored pick fails over past it.
	st := s.st(pinned)
	st.mu.Lock()
	prev := st.script
	st.mu.Unlock()
	st.script429("")
	q := rs1Req{caller: "user", kind: "chat", model: model, headers: map[string]string{}}
	var served string
	for i := 0; i < 6; i++ {
		s.clearCooling()
		shot := s.replay(q)
		if shot.code != 200 {
			st.set(prev)
			return fmt.Errorf("the next request = %d (%s): it behaved as pinned to %q", shot.code, shot.body, pinned)
		}
		served = shot.hdr.Get("X-RogerAI-Provider")
	}
	st.set(prev)
	s.clearCooling()
	if served == st.id {
		return fmt.Errorf("the next request was served by the refusing %q", pinned)
	}
	return nil
}

func (s *rs1State) bodyCarriesStationKeysVerbatim() error {
	if len(s.shots) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	first := s.shots[0]
	var m map[string]json.RawMessage
	if err := json.Unmarshal(first.body, &m); err != nil {
		return fmt.Errorf("the response body is not a JSON object: %s", first.body)
	}
	if m["provider"] == nil || m["roger"] == nil {
		return fmt.Errorf("the station's own provider/roger keys did not pass through: %s", first.body)
	}
	return nil
}

func (s *rs1State) noBodyMakesOfferAppear() error {
	s.b.mu.Lock()
	for id, n := range s.b.nodes {
		for _, o := range n.Offers {
			if o.PriceOut > maxPriceOutCeiling() {
				s.b.mu.Unlock()
				return fmt.Errorf("node %s offers %s at $%v/1M, above the ceiling", id, o.Model, o.PriceOut)
			}
		}
	}
	s.b.mu.Unlock()
	q := rs1Req{caller: "user", kind: "chat", model: s.regModel, headers: map[string]string{},
		frag: `"provider": {"max_price": {"completion": 1000}}`}
	if err := s.fire(q); err != nil {
		return err
	}
	if s.lastHdr.Get("X-RogerAI-Provider") == "n-dear-reg-"+s.nonce {
		return fmt.Errorf("the rejected registration served a request")
	}
	if out, err := s.priceOut(); err == nil && out > maxPriceOutCeiling() {
		return fmt.Errorf("a request settled at $%v/1M out, above the ceiling", out)
	}
	return nil
}

func (s *rs1State) registrationAccepted() error {
	if s.regCode != http.StatusOK {
		return fmt.Errorf("registration of %q = %d: %s", s.regModel, s.regCode, s.regBody)
	}
	return nil
}

func (s *rs1State) registrationRejectedSuffix() error {
	if s.regCode < 400 || s.regCode >= 500 {
		return fmt.Errorf("registration of %q = %d, want a 4xx refusal: %s", s.regModel, s.regCode, s.regBody)
	}
	suffix := s.regModel[strings.LastIndex(s.regModel, ":"):]
	if !strings.Contains(s.regBody, suffix) {
		return fmt.Errorf("the refusal %q does not name the reserved suffix %q", s.regBody, suffix)
	}
	return nil
}

func (s *rs1State) noOfferOnAir(model string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for id, n := range s.b.nodes {
		for _, o := range n.Offers {
			if o.Model == model {
				return fmt.Errorf("node %s carries an offer for %q", id, model)
			}
		}
	}
	return nil
}

func (s *rs1State) requestIsNoMatch(model string) error {
	if err := s.fire(rs1Req{caller: "user", kind: "chat", model: model, headers: map[string]string{}}); err != nil {
		return err
	}
	if s.lastCode != 503 {
		return fmt.Errorf("status %d, want 503 (%s)", s.lastCode, s.lastBody)
	}
	return s.errorCodeIs("no_match")
}

func (s *rs1State) noOfferEndsIn(a, b, c string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("GET /discover = %d", s.lastCode)
	}
	var doc struct {
		Offers []struct {
			Model string `json:"model"`
		} `json:"offers"`
	}
	if err := json.Unmarshal(s.lastBody, &doc); err != nil {
		return err
	}
	if len(doc.Offers) == 0 {
		return fmt.Errorf("the public feed lists no offers")
	}
	for _, o := range doc.Offers {
		for _, suf := range []string{a, b, c} {
			if strings.HasSuffix(o.Model, suf) {
				return fmt.Errorf("the public feed lists the suffixed id %q", o.Model)
			}
		}
	}
	return nil
}

func (s *rs1State) rawCodeInNoLog(alias string) error {
	all := s.logs.String()
	for _, code := range []string{alias, s.codes[alias]} {
		if code == "" {
			continue
		}
		if strings.Contains(all, code) {
			return fmt.Errorf("the band code %q appears in a log line", code)
		}
		if tail := code[strings.LastIndex(code, " ")+1:]; len(tail) >= 8 && strings.Contains(all, tail) {
			return fmt.Errorf("the band code tail %q appears in a log line", tail)
		}
	}
	return nil
}

func (s *rs1State) uniformMessage(want string) error { return s.errorMessageIs(want) }

// --- the runners --------------------------------------------------------------------------

const (
	rs1C = `("[^"]*"|an anonymous caller|the grant holder|the Playbox origin)`
	rs1K = `(a STREAMING chat completion|a chat completion|an illegal prompt)`
	rs1M = ` for "([^"]*)"`
	rs1F = "`(.*)`"
)

func rs1Steps(sc *godog.ScenarioContext, st *rs1State) {
	// Background
	sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
	sc.Step(`^the fee rate is 10%$`, st.feeRate10)
	sc.Step(`^the consumer default out-price cap is \$10/1M$`, st.defaultOutCap10)
	sc.Step(`^the operator ceilings are \$100/1M out and \$50/1M in$`, st.ceilings100and50)
	sc.Step(`^a logged-in consumer "([^"]*)" with a \$([0-9.]+) balance$`, st.loggedIn)
	sc.Step(`^every node above has passed its canary and sits in Tier A$`, st.everyNodeTierA)
	sc.Step(`^nothing else$`, st.nothingElse)

	// Given: stations
	const node = `^node "([^"]*)"\s+is on air for "([^"]*)" at in \$([0-9.]+) out \$([0-9.]+)`
	sc.Step(node+` per 1M, seen just now$`, st.nodeOnAir)
	sc.Step(node+`$`, st.nodeOnAir)
	sc.Step(node+` per 1M, confidential-attested, seen just now$`, st.nodeOnAirAttested)
	sc.Step(node+` per 1M with (\d+) tok/s, seen just now$`, st.nodeOnAirTPS)
	sc.Step(node+` per 1M with no measured throughput, seen just now$`, st.nodeOnAirUnmeasured)
	sc.Step(node+` per 1M with a scheduled free window (\d\d):(\d\d)-(\d\d):(\d\d) UTC$`, st.nodeOnAirTOU)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and is TEE-attested$`, st.nodeTEE)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and is not attested$`, st.nodeNotAttested)
	sc.Step(`^node "([^"]*)" alone is on air for "([^"]*)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, st.nodeAlonePriced)
	sc.Step(`^node "([^"]*)" alone is on air for "([^"]*)" with a declared context of (\d+)$`, st.nodeAloneCtx)
	sc.Step(`^every station for "([^"]*)" declares a context of (\d+)$`, st.everyStationCtx)
	sc.Step(`^node "([^"]*)" has a measured throughput of (\d+) tok/s$`, st.nodeTPS)
	sc.Step(`^node "([^"]*)" measures (\d+) tok/s$`, st.nodeTPS)
	sc.Step(`^node "([^"]*)" is in a 429 cooldown for (\d+) seconds$`, st.nodeCooling)
	sc.Step(`^every station for "([^"]*)" is in a 429 cooldown$`, st.everyStationCooling)
	sc.Step(`^every station for "([^"]*)" is in a 429 cooldown, the soonest expiring in (\d+) seconds$`, st.everyStationCoolingSoonest)
	sc.Step(`^node "([^"]*)" goes off air$`, st.nodeGoesOffAir)
	sc.Step(`^node "([^"]*)" answers 429$`, st.nodeAnswers429)
	sc.Step(`^node "([^"]*)" is in Tier B \(failing canaries\)$`, st.nodeTierB)
	sc.Step(`^node "([^"]*)" has failed the dead-probe streak$`, st.nodeDeadProbe)
	sc.Step("^node \"([^\"]*)\" answers every job with a body that also carries "+rs1F+"$", st.nodeAnswersWithExtraBody)
	sc.Step("^node \"([^\"]*)\" answers with a receipt carrying an extra field "+rs1F+"$", st.nodeAnswersWithExtraReceiptField)

	// Given: consumers, grants, bands, modes, fabrics
	sc.Step(`^a private band "([^"]*)" with code "([^"]*)" whose only station is "([^"]*)" on air for "([^"]*)" at in \$0 out \$0$`, st.privateBandOnly)
	sc.Step(`^a private band "([^"]*)" with code "([^"]*)" whose only station is "([^"]*)" on air for "([^"]*)" at in \$0 out \$0 with (\d+) tok/s$`, st.privateBandOnlyTPS)
	sc.Step(`^a private band "([^"]*)" with code "([^"]*)" whose station "([^"]*)" serves "([^"]*)", and the band denies "([^"]*)" and "([^"]*)"$`, st.privateBandDenying)
	sc.Step(`^owner "([^"]*)" owns node "([^"]*)" and minted grant "([^"]*)" for "([^"]*)"$`, st.ownerGrant)
	sc.Step(`^owner "([^"]*)" owns node "([^"]*)" and minted grant "([^"]*)" for "([^"]*)" at price_out \$([0-9.]+)$`, st.ownerGrantPriced)
	sc.Step(`^owner "([^"]*)" owns node "([^"]*)" serving "([^"]*)" and "([^"]*)", and minted grant "([^"]*)" allowing only "([^"]*)"$`, st.ownerGrantTwoModels)
	sc.Step(`^owner "([^"]*)" owns nodes "([^"]*)" and "([^"]*)" and minted grant "([^"]*)" for "([^"]*)"$`, st.ownerGrantTwoNodes)
	sc.Step(`^"([^"]*)" owns node "([^"]*)"$`, st.userOwnsNode)
	sc.Step(`^"([^"]*)" has a monthly cap of \$([0-9.]+) and has spent \$([0-9.]+) this month$`, st.monthlyCapSpent)
	sc.Step(`^"([^"]*)" has exhausted the per-identity rate limit$`, st.rateLimitExhausted)
	sc.Step(`^the broker runs moderation in (sync|async) mode$`, st.moderationMode)
	sc.Step(`^the broker runs as two instances with the poller for "([^"]*)" on the peer$`, st.twoInstances)
	sc.Step(`^no direct node is on air for "([^"]*)"$`, st.noDirect)
	sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" through the edge bridge$`, st.towerBridge)
	sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" at (\d+) tok/s through the edge bridge$`, st.towerBridgeTPS)
	sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" at out \$([0-9.]+)/1M through the edge bridge$`, st.towerBridgePriced)
	sc.Step(`^the store holds a persisted node offering "([^"]*)"$`, st.persistedOffering)
	sc.Step(`^the store holds a persisted node offering "([^"]*)" at out -\$([0-9.]+)/1M$`, st.persistedNegative)

	// When
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+`$`, st.posts)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+" with body "+rs1F+"$", st.postsBody)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with body:$`, st.postsDoc)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+` with header (X-Roger-[A-Za-z-]+) "([^"]*)" and body `+rs1F+"$", st.postsHeaderBody)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with header (X-Roger-[A-Za-z-]+) "([^"]*)" and no routing body$`, st.postsHeader)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with header (X-Roger-[A-Za-z-]+) "([^"]*)"$`, st.postsHeader)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with no routing body and no routing headers$`, st.posts)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+` with max_tokens (\d+) and body `+rs1F+"$", st.postsMaxBody)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with max_tokens (\d+)$`, st.postsMax)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+` with a (\d+)-token prompt and body `+rs1F+"$", st.postsPromptBody)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+` with a (\d+)-token prompt, max_tokens (\d+) and body `+rs1F+"$", st.postsPromptMaxBody)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with a (\d+)-token prompt$`, st.postsPrompt)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` with body where "([^"]*)" is a list of (\d+) distinct valid entries$`, st.postsList)
	sc.Step("^"+rs1C+" posts "+rs1K+rs1M+" with raw body "+rs1F+"$", st.postsRawFor)
	sc.Step("^"+rs1C+" posts "+rs1K+" with raw body "+rs1F+"$", st.postsRaw)
	sc.Step("^"+rs1C+" posts "+rs1K+` with no "([^"]*)" field and body `+rs1F+"$", st.postsNoModel)
	sc.Step(`^`+rs1C+` posts `+rs1K+` with a body that is not a JSON object$`, st.postsNonObjectRS1)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` whose "([^"]*)" object is padded to (\d+) MiB$`, st.postsPadded)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` whose "provider\.ignore" holds (\d+) node ids of (\d+) characters each$`, st.postsBigIgnore)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` (\d+) times$`, st.postsTimes)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` (\d+) times with distinct request ids$`, st.postsTimes)
	sc.Step(`^`+rs1C+` posts `+rs1K+rs1M+` and is served by "([^"]*)" at out \$([0-9.]+)/1M$`, st.postsServedBy)
	sc.Step(`^at (\d\d):(\d\d) UTC `+rs1C+` posts `+rs1K+rs1M+`$`, st.postsAt)
	sc.Step(`^the station's receipt claims (\d+) completion tokens$`, st.receiptClaims)
	sc.Step(`^a station registers "([^"]*)" at out \$([0-9.]+)/1M$`, st.stationRegistersAt)
	sc.Step(`^a station registers an offer for "([^"]*)"$`, st.stationRegistersOffer)
	sc.Step(`^the broker re-hydrates its registry$`, st.rehydrates)
	sc.Step(`^the owner of "([^"]*)" raises the out price to \$([0-9.]+)/1M$`, st.ownerRaisesOut)
	sc.Step(`^the public feed is fetched$`, st.publicFeedFetched)

	// Then: the response
	sc.Step(`^the response is (\d+)$`, st.responseIs)
	sc.Step(`^the response is not (\d+)$`, st.responseIsNot)
	sc.Step(`^the response is the monthly-cap refusal$`, st.monthlyCapRefusal)
	sc.Step(`^the response is the moderation refusal$`, st.moderationRefusal)
	sc.Step(`^the error code is "([^"]*)"$`, st.errorCodeIs)
	sc.Step(`^the error code is not "([^"]*)"$`, st.errorCodeIsNot)
	sc.Step(`^the error message names "([^"]*)"$`, st.errorNames)
	sc.Step(`^the error message names "([^"]*)" only as the offending ORDER entry, and says nothing about whether it exists$`, st.errorNamesOnlyAsOrderEntry)
	sc.Step(`^the error message is "([^"]*)"$`, st.errorMessageIs)
	sc.Step(`^the error message is the uniform "([^"]*)"$`, st.uniformMessage)
	sc.Step("^the error message is byte-identical to the message for "+rs1F+"$", st.errorByteIdenticalTo)
	sc.Step(`^the error message contains "([^"]*)"$`, st.errorMessageContains)
	sc.Step(`^the error message starts with "([^"]*)"$`, st.errorMessageStarts)
	sc.Step(`^the error message ends with "([^"]*)"$`, st.errorMessageEnds)
	sc.Step(`^the error message says to log in to spend$`, st.errorSaysLogIn)
	sc.Step(`^the response header X-RogerAI-Model is "([^"]*)"$`, st.modelHeaderIs)
	sc.Step(`^the response header X-RogerAI-Cost is "([^"]*)"$`, st.costHeaderIs)
	sc.Step(`^the response carries X-RogerAI-Cost "([^"]*)"$`, st.costHeaderIs)
	sc.Step(`^the response header X-RogerAI-Price reports out ([0-9.]+)$`, st.settledPriceOut)
	sc.Step(`^the response carries no (X-RogerAI-Receipt|X-RogerAI-Provider|Retry-After) header$`, st.carriesNoHeader)
	sc.Step(`^the Retry-After header is "([^"]*)"$`, st.retryAfterHeaderIs)
	sc.Step(`^the response Content-Type is "([^"]*)"$`, st.contentTypeIs)
	sc.Step(`^the response header Access-Control-Expose-Headers includes "([^"]*)"$`, st.exposeIncludes)
	sc.Step(`^the response body is a JSON object with an "([^"]*)" object holding string "([^"]*)"$`, st.bodyIsErrorObject)
	sc.Step(`^the response body is a JSON object with an "([^"]*)" object holding string "([^"]*)" and string "([^"]*)"$`, st.errorEnvelopeTwo)

	// Then: who served, the plan
	sc.Step(`^the served node is "([^"]*)"$`, st.servedNode)
	sc.Step(`^the served node is one of "([^"]*)", "([^"]*)"$`, st.servedOneOf2)
	sc.Step(`^the served node is one of "([^"]*)", "([^"]*)", "([^"]*)"$`, st.servedOneOf3)
	sc.Step(`^every request was served by "([^"]*)"$`, st.everyServedBy)
	sc.Step(`^"([^"]*)" was never a candidate$`, st.neverCandidate)
	sc.Step(`^"([^"]*)" was the last candidate in the ordered plan$`, st.lastInPlan)
	sc.Step(`^no broker-side failover was planned$`, st.noFailoverPlanned)
	sc.Step(`^no hold was placed$`, st.noHoldRS1)
	sc.Step(`^no station was dispatched$`, st.noStationDispatched)
	sc.Step(`^no station received anything$`, st.noStationDispatched)
	sc.Step(`^"([^"]*)" received nothing$`, st.rs1ReceivedNothing)
	sc.Step(`^neither band code appears in the response or in a log line$`, st.rs1NoBandCodeLeaks)
	sc.Step(`^no hold was placed and no station was dispatched$`, st.noHoldNoDispatch)
	sc.Step(`^the failover plan contains "([^"]*)" and "([^"]*)"$`, st.planContains2)
	sc.Step(`^the failover plan contains "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.planContains3)
	sc.Step(`^the failover plan does not contain "([^"]*)"$`, st.planLacks)
	sc.Step(`^the ordered plan is "([^"]*)", "([^"]*)", "([^"]*)"$`, st.orderedPlanIs)
	sc.Step(`^the max_tokens the plan carries for "([^"]*)" is smaller than for "([^"]*)"$`, st.planMaxTokensSmaller)
	sc.Step(`^(\d+) repeats of the request with distinct request ids all pick the same node \(a strict sort, no power-of-two spread\)$`, st.repeatsPickSame)
	sc.Step(`^no failed attempt was recorded for "([^"]*)"$`, st.noFailedAttemptFor)

	// Then: the routing pass
	sc.Step(`^the routing pass saw no value for "([^"]*)"$`, st.sawNoValue)
	sc.Step(`^the routing pass ran with pref "([^"]*)"$`, st.ranWithPref)
	sc.Step(`^no routing pass ran$`, st.noRoutingPass)
	sc.Step(`^the routing pass ran with an out-price cap of \$([0-9.]+)/1M$`, st.ranWithOutCap)
	sc.Step(`^the routing pass ran with an in-price cap of \$([0-9.]+)/1M$`, st.ranWithInCap)
	sc.Step(`^the routing pass ran with min-tps (\d+)$`, st.ranWithMinTPS)
	sc.Step(`^the routing pass ran with a reward range ceiling of \$([0-9.]+)/1M out$`, st.ranWithRewardOut)
	sc.Step(`^the request.{1,2}s routing log line names sort "([^"]*)"$`, st.routingLineSort)
	sc.Step(`^/admin/live routing_strict_sort increased by (\d+)$`, st.strictSortIncreased)
	sc.Step(`^/admin/live edge_coin_flips did not change$`, st.coinFlipsUnchanged)
	sc.Step(`^/admin/live reports variant_free (\d+), variant_floor (\d+), variant_nitro (\d+)$`, st.variantCounters)
	sc.Step(`^/admin/live reports routing_body_requests (\d+) and routing_body_rejects (\d+)$`, st.bodyCounters)
	sc.Step(`^/admin/live echoes no node id, price, or band code from either request$`, st.adminEchoesNothing)

	// Then: what the station / Tower / bus received
	sc.Step(`^the station received a body whose only top-level keys are "([^"]*)", "([^"]*)"$`, st.stationOnlyKeys)
	sc.Step(`^the station received no top-level key "([^"]*)"$`, st.stationNoKey)
	sc.Step(`^the station received top-level key "([^"]*)" with value (.+)$`, st.stationReceivedKey)
	sc.Step(`^the station received top-level key "([^"]*)" byte-identical to what was sent$`, st.stationKeyByteIdentical)
	sc.Step(`^the station received a "([^"]*)" object with "([^"]*)" true$`, st.stationObjectTrue)
	sc.Step(`^the station received the request's own max_tokens (\d+)$`, st.stationOwnMaxTokens)
	sc.Step(`^the station received a "([^"]*)" no larger than the token count \$([0-9.]+) buys at \$([0-9.]+)/1M out after the prompt estimate$`, st.stationMaxTokensWithin)
	sc.Step(`^the Tower received no top-level key "([^"]*)"$`, st.towerNoKey)
	sc.Step(`^the Tower received top-level key "([^"]*)" with value "([^"]*)"$`, st.towerKeyValue)
	sc.Step(`^the Tower received no job$`, st.towerNoJob)
	sc.Step(`^the Tower received every job$`, st.towerEveryJob)
	sc.Step(`^the job that crossed the bus carried a body with no top-level key "([^"]*)"$`, st.busJobNoKey)

	// Then: money
	sc.Step(`^the hold placed equals estimateMaxCost at the priciest station in the plan, "([^"]*)"$`, st.holdEqualsPriciest)
	sc.Step(`^the hold placed equals estimateMaxCost of the STRIPPED body at the priciest station in the plan, "([^"]*)"$`, st.holdEqualsPriciest)
	sc.Step(`^the hold placed covers "([^"]*)"'s price$`, st.holdEqualsPriciest)
	sc.Step(`^the hold placed is \$([0-9.]+)$`, st.holdIs)
	sc.Step(`^the hold placed is at most \$([0-9.]+)$`, st.holdAtMost)
	sc.Step(`^the settled cost is at most \$([0-9.]+)$`, st.settledCostAtMost)
	sc.Step(`^the settled cost is \$([0-9.]+)$`, st.settledCostIs)
	sc.Step(`^the settled price out is \$([0-9.]+)/1M$`, st.settledPriceOut)
	sc.Step(`^the settled price out is at most \$([0-9.]+)/1M$`, st.settledPriceOutAtMost)
	sc.Step(`^the settled price is "([^"]*)"'s$`, st.settledPriceIsOf)
	sc.Step(`^the receipt names model "([^"]*)"$`, st.receiptNamesModel)
	sc.Step(`^the receipt's broker signature verifies over the bare id$`, st.receiptBrokerSigBare)
	sc.Step(`^the station's receipt chain has two consecutive entries for model "([^"]*)"$`, st.receiptChainTwo)
	sc.Step(`^the final usage chunk names model "([^"]*)"$`, st.usageChunkNamesModel)
	sc.Step(`^/console for "([^"]*)" lists the request under model "([^"]*)"$`, st.consoleListsModel)

	// Then: moderation, registration, discovery, the rest
	sc.Step(`^the moderation screen ran exactly once$`, st.moderationRanOnce)
	sc.Step(`^the moderation screen did not run$`, st.moderationDidNotRun)
	sc.Step(`^the moderation screen received the identical prompt text$`, st.moderationIdenticalText)
	sc.Step(`^the broker's prompt estimate is within (\d+)% of the estimate for the same request without the routing body$`, st.promptEstimateWithin)
	sc.Step(`^the off-path screener received the prompt before the routing pass ran$`, st.screenerBeforeRouting)
	sc.Step(`^the screening job was submitted with model "([^"]*)"$`, st.screeningJobModel)
	sc.Step(`^the next request from "([^"]*)" for "([^"]*)" with no routing body is routed by score, not pinned to "([^"]*)"$`, st.nextRequestRoutedByScore)
	sc.Step(`^the consumer response body carries the station's keys verbatim \(pass-through\) but the broker read none of them$`, st.bodyCarriesStationKeysVerbatim)
	sc.Step(`^no consumer request body can make that offer appear$`, st.noBodyMakesOfferAppear)
	sc.Step(`^the registration is rejected$`, st.registrationRejected)
	sc.Step(`^the registration is accepted$`, st.registrationAccepted)
	sc.Step(`^the registration is rejected with a message naming the reserved suffix$`, st.registrationRejectedSuffix)
	sc.Step(`^no offer for "([^"]*)" is on air$`, st.noOfferOnAir)
	sc.Step(`^a request for "([^"]*)" is a 503 no_match$`, st.requestIsNoMatch)
	sc.Step(`^no offer's model ends in "([^"]*)", "([^"]*)" or "([^"]*)"$`, st.noOfferEndsIn)
	sc.Step(`^the raw code "([^"]*)" appears in no log line$`, st.rawCodeInNoLog)
}

func rs1Run(t *testing.T, feature string) {
	st := &rs1State{s0State: &s0State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownRS1()
		st.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.resetRS1()
			})
			sc.After(func(ctx context.Context, scn *godog.Scenario, err error) (context.Context, error) {
				if err != nil {
					all := st.logs.String()
					if len(all) > 2500 {
						all = all[len(all)-2500:]
					}
					t.Logf("broker log tail for %q:\n%s\nlast response %d %.600s", scn.Name, all, st.lastCode, st.lastBody)
				}
				st.teardownRS1()
				st.teardownPins()
				return ctx, nil
			})
			rs1Steps(sc, st)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{feature}, TestingT: t, Strict: true,
			Tags: "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later",
		},
	}
	if suite.Run() != 0 {
		t.Fatal("godog suite failed")
	}
}

func TestRoutingRequestShapeBDD(t *testing.T) {
	rs1Run(t, "../../features/routing/request_shape.feature")
}

func TestRoutingVariantSugarBDD(t *testing.T) {
	rs1Run(t, "../../features/routing/variant_sugar.feature")
}
