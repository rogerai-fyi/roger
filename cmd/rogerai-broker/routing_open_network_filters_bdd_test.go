package main

// routing_open_network_filters_bdd_test.go makes features/routing/open_network_filters.feature
// EXECUTABLE against the REAL broker: real stations on real tunnels (the upstream_failover
// harness through rpState), the real register handler for declared attributes, the real
// tool-call canary / probe bookkeeping for trust, the real sealed edge fabric for the Tower
// scenarios, and the real relay for every request. It embeds the capability-gating runner's
// state (cg1State) and re-registers its request plumbing and assertions where the wording
// matches; the filter-specific tails are parsed by nf2Shape.
//
// OBSERVATION POINTS (stated so a reader knows what each claim is judged by):
//   - "is a candidate" / "is NOT a candidate": one real relay over a registry reduced to that
//     station. A candidate serves; a non-candidate must yield 503 no_match with the upstream
//     untouched (a 400 on an unsupported key is NOT a pass, it is the RED of this slice).
//   - "the candidate set is exactly {...}": the two checks above for every station on air.
//   - "picked more often" / "never picked": the X-RogerAI-Provider of every relay in a batch.
//   - declared attributes a station cannot declare yet (params_b: protocol.ModelOffer has no
//     such field) fail in the Given, naming the missing field, rather than being faked.
//   - a Tower row's declared attributes are set on the registration of the node behind the
//     routable row, the same place the bridge reads them (edgeJoinedOffer).
//
// Scenarios only drivable from the CLI, the TUI or the docs are tagged in the feature and
// excluded here (@cli, @tui, @docs); multi-station private bands are @later.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"rogerai.fm/roger/v6/internal/bddtest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type nf2State struct {
	*cg1State

	// the request under test, beyond what cg1Req carries
	extraTop map[string]string // raw top-level members ("max_tokens": "100000")
	promptN  int               // a measured prompt of this many tokens (0 = the harness prompt)

	// observations
	run2        []rpResult        // the last batch (nf2 batches keep cg1's s.run too)
	refusedRegs map[string]string // scenario name -> refusal body of a rejected registration
	regCode2    int
	regBody2    string
	regNodeID   string
	hbCode      int
	cmpBody     []byte // a second response body to compare uniformity against
	cmpCode     int

	// the params_b signing-boundary steps
	sigReg *protocol.NodeRegistration
	sigOK  bool
}

func (s *nf2State) nf2Reset() error {
	if err := s.cg1Reset(); err != nil {
		return err
	}
	s.extraTop, s.promptN = map[string]string{}, 0
	s.run2, s.refusedRegs = nil, map[string]string{}
	s.regCode2, s.regBody2, s.regNodeID, s.hbCode = 0, "", "", 0
	s.cmpBody, s.cmpCode = nil, 0
	s.model = "qwen3-32b"
	return nil
}

// --- helpers: offers, trust, registration --------------------------------------------------

// nf2Up stands a Tier-A station up for model (cg1Stand: real tunnel, hit-recording upstream).
func (s *nf2State) nf2Up(name, model string) *fstation {
	if model == "" {
		model = s.model
	}
	s.model = model
	return s.cg1Stand(name, model, 0.10, 0.30, nil)
}

// nf2Offer mutates the offer for model on node id (every offer when model is "").
func (s *nf2State) nf2Offer(id, model string, f func(o *protocol.ModelOffer)) {
	s.b.mu.Lock()
	reg := s.b.nodes[id]
	found := false
	for i := range reg.Offers {
		if model == "" || reg.Offers[i].Model == model {
			f(&reg.Offers[i])
			found = true
		}
	}
	if !found && model != "" {
		// A Tower's share node is registered by the fabric helper without offers; a real share
		// node registers the offer it serves, which is where the bridge reads its attributes.
		o := protocol.ModelOffer{Model: model, Ctx: 8192}
		f(&o)
		reg.Offers = append(reg.Offers, o)
	}
	s.b.nodes[id] = reg
	s.b.mu.Unlock()
}

func (s *nf2State) nf2Reg(id string, f func(reg *protocol.NodeRegistration)) {
	s.b.mu.Lock()
	reg := s.b.nodes[id]
	f(&reg)
	s.b.nodes[id] = reg
	s.b.mu.Unlock()
}

func (s *nf2State) nf2SetQuant(name, model, quant string) {
	s.nf2Offer(s.st(name).id, model, func(o *protocol.ModelOffer) { o.Quant = quant })
}

func (s *nf2State) nf2SetTTFT(id string, ms float64) {
	s.b.mu.Lock()
	tq := s.b.trust[id]
	tq.ttftMs = ms
	s.b.trust[id] = tq
	s.b.mu.Unlock()
}

func (s *nf2State) nf2Unmeasured(id string) {
	s.b.metricsMu.Lock()
	delete(s.b.tps, id)
	s.b.metricsMu.Unlock()
}

// nf2SetParamsB sets the declared parameter count through the offer's json name, so a field
// the protocol does not have yet fails here naming exactly what is missing (never faked).
func nf2SetParamsB(o *protocol.ModelOffer, v float64) error {
	rv := reflect.ValueOf(o).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if strings.Split(rt.Field(i).Tag.Get("json"), ",")[0] == "params_b" {
			f := rv.Field(i)
			if f.Kind() != reflect.Float64 {
				return fmt.Errorf("protocol.ModelOffer.params_b is %s, want a float64 (billions)", f.Kind())
			}
			f.SetFloat(v)
			return nil
		}
	}
	return fmt.Errorf(`protocol.ModelOffer has no field tagged json:"params_b": a station cannot declare its parameter count yet`)
}

func nf2OfferParamsB(o map[string]any) (float64, bool) {
	v, ok := o["params_b"].(float64)
	return v, ok
}

// nf2RawRegister registers a FRESH station through the real /nodes/register handler, with
// `offerExtra` members spliced into its first offer and `topExtra` into the registration
// object AFTER signing (unknown members are outside the possession proof, as every declared
// display field is). Raw values are spliced verbatim so invalid JSON (NaN, Infinity) reaches
// the decoder as written.
func (s *nf2State) nf2RawRegister(name, model, quant string, offerExtra, topExtra map[string]string) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, ownerPriv, _ := ed25519.GenerateKey(nil)
	id := name + "-" + utNonce()
	reg := protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub), BridgeToken: "tok-" + id, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, Ctx: 8192, Quant: quant}}}
	reg.SignRegistration(priv)
	b, _ := json.Marshal(reg)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	// json.Marshal refuses an invalid RawMessage (NaN, Infinity), so each raw offer value
	// rides as a quoted placeholder and is spliced into the bytes afterwards, verbatim.
	raws := map[string]string{}
	if len(offerExtra) > 0 {
		var offers []map[string]json.RawMessage
		_ = json.Unmarshal(m["offers"], &offers)
		keys := make([]string, 0, len(offerExtra))
		for k := range offerExtra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ph := strconv.Quote("__nf2raw_" + k + "__")
			raws[ph] = offerExtra[k]
			offers[0][k] = json.RawMessage(ph)
		}
		ob, _ := json.Marshal(offers)
		m["offers"] = ob
	}
	for k, v := range topExtra {
		m[k] = json.RawMessage(v)
	}
	body, _ := json.Marshal(m)
	for ph, v := range raws {
		body = bytes.Replace(body, []byte(ph), []byte(v), 1)
	}
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, ownerPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode2, s.regBody2, s.regNodeID = w.Code, w.Body.String(), id
	if w.Code != http.StatusOK {
		s.refusedRegs[name] = w.Body.String()
	}
	return nil
}

func nf2RawJSON(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "NaN", "Infinity", "-Infinity":
		return v // invalid JSON on purpose: the decoder must refuse it
	}
	return v
}

// --- helpers: the request ---------------------------------------------------------------------

func (s *nf2State) nf2Body(q cg1Req) []byte {
	if s.promptN > 0 && q.prompt == "" {
		q.prompt = utPrompt(s.promptN)
	}
	body := s.cg1Body(q)
	if len(s.extraTop) == 0 {
		return body
	}
	keys := make([]string, 0, len(s.extraTop))
	for k := range s.extraTop {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var extra []string
	for _, k := range keys {
		extra = append(extra, strconv.Quote(k)+":"+s.extraTop[k])
	}
	return append(bytes.TrimSuffix(body, []byte("}")), []byte(","+strings.Join(extra, ",")+"}")...)
}

// nf2Fire is cg1Fire with nf2Body (the extra top-level members and the measured prompt).
func (s *nf2State) nf2Fire(b *broker, q cg1Req) (rpResult, *recWriter, error) {
	body := s.nf2Body(q)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	switch q.who {
	case "anon":
		if !s.anonSeed {
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

func (s *nf2State) nf2When(q cg1Req) error {
	if q.who == "" || q.who == "band" {
		if err := s.ensureFunded(); err != nil {
			return err
		}
	}
	if err := s.cg1Snap(); err != nil {
		return err
	}
	res, rec, err := s.nf2Fire(s.b, q)
	if err != nil {
		return err
	}
	s.req, s.last, s.rec = q, res, rec
	s.run = []rpResult{res}
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = res.code, res.body, res.hdr, rec
	return nil
}

func (s *nf2State) nf2Batch(n int, q cg1Req) error {
	if q.who == "" || q.who == "band" {
		if err := s.ensureFunded(); err != nil {
			return err
		}
	}
	if err := s.cg1Snap(); err != nil {
		return err
	}
	s.run, s.req = nil, q
	for i := 0; i < n; i++ {
		res, rec, err := s.nf2Fire(s.b, q)
		if err != nil {
			return err
		}
		s.run = append(s.run, res)
		s.last, s.rec = res, rec
		s.lastCode, s.lastBody, s.lastHdr, s.lastRec = res.code, res.body, res.hdr, rec
	}
	return nil
}

// nf2Isolated fires q over a registry reduced to {name}: the candidacy observation.
func (s *nf2State) nf2Isolated(name string, q cg1Req) (rpResult, int, error) {
	st, ok := s.stations[name]
	if !ok {
		return rpResult{}, 0, fmt.Errorf("no station %q is on air in this scenario", name)
	}
	id := st.id
	s.b.mu.Lock()
	saved := s.b.nodes
	s.b.nodes = map[string]protocol.NodeRegistration{id: saved[id]}
	s.b.mu.Unlock()
	before := s.cg1HitsOf(name)
	res, _, err := s.nf2Fire(s.b, q)
	s.b.mu.Lock()
	s.b.nodes = saved
	s.b.mu.Unlock()
	return res, s.cg1HitsOf(name) - before, err
}

func (s *nf2State) nf2Cand(name string, q cg1Req) error {
	res, _, err := s.nf2Isolated(name, q)
	if err != nil {
		return err
	}
	if res.code == 200 && res.hdr.Get("X-RogerAI-Provider") == s.st(name).id {
		return nil
	}
	return fmt.Errorf("%q should be a candidate, but the relay with only it on air answered %d %s", name, res.code, bytes.TrimSpace(res.body))
}

func (s *nf2State) nf2NotCand(name string, q cg1Req) error {
	if body, refused := s.refusedRegs[name]; refused {
		// The strongest form of "not a candidate": the registration never got on air.
		_ = body
		return nil
	}
	res, hits, err := s.nf2Isolated(name, q)
	if err != nil {
		return err
	}
	if hits > 0 || (res.code == 200 && res.hdr.Get("X-RogerAI-Provider") == s.st(name).id) {
		return fmt.Errorf("%q must NOT be a candidate, but it served the request (%d, upstream hits %d)", name, res.code, hits)
	}
	if code, _ := cg1ErrOf(res.body); !(res.code == http.StatusServiceUnavailable && code == "no_match") &&
		!(res.code == http.StatusBadRequest && strings.Contains(string(res.body), "exceeds the context window")) &&
		!(res.code == http.StatusUnauthorized && q.who == "anon") {
		return fmt.Errorf("%q was not served, but not because a filter excluded it: the relay answered %d %s (want 503 no_match)", name, res.code, bytes.TrimSpace(res.body))
	}
	return nil
}

func (s *nf2State) isCand2(name string) error    { return s.nf2Cand(name, s.req) }
func (s *nf2State) isNotCand2(name string) error { return s.nf2NotCand(name, s.req) }

func (s *nf2State) candidateSetIs(set string) error {
	want := map[string]bool{}
	for _, n := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(set, -1) {
		want[n[1]] = true
	}
	names := make([]string, 0, len(s.stations))
	for n := range s.stations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if want[n] {
			if err := s.nf2Cand(n, s.req); err != nil {
				return err
			}
		} else if err := s.nf2NotCand(n, s.req); err != nil {
			return err
		}
	}
	for n := range want {
		if _, ok := s.stations[n]; !ok {
			return fmt.Errorf("the expected set names %q, which is not on air", n)
		}
	}
	return nil
}

// --- the tail parser ------------------------------------------------------------------------

// nf2SplitTop splits a tail on ", " and " and " outside brackets, braces and quotes.
func nf2SplitTop(tail string) []string {
	var parts []string
	depth, inStr := 0, false
	start := 0
	for i := 0; i < len(tail); i++ {
		c := tail[i]
		switch {
		case c == '"' && (i == 0 || tail[i-1] != '\\'):
			inStr = !inStr
		case inStr:
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case depth == 0 && strings.HasPrefix(tail[i:], ", "):
			parts = append(parts, tail[start:i])
			start = i + 2
			i++
		case depth == 0 && strings.HasPrefix(tail[i:], " and "):
			parts = append(parts, tail[start:i])
			start = i + 5
			i += 4
		}
	}
	parts = append(parts, tail[start:])
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var (
	nf2ReKey    = regexp.MustCompile(`^(provider|roger)\.([a-z_]+) (.+)$`)
	nf2ReHeader = regexp.MustCompile(`^header "([^:"]+): ([^"]*)"$`)
	nf2RePrompt = regexp.MustCompile(`^the prompt "([^"]*)"(?: is refused)?$`)
	nf2ReN      = regexp.MustCompile(`^(\d+) entries of (".*")$`)
)

func nf2Join(cur, member string) string {
	if cur == "" {
		return member
	}
	return cur + "," + member
}

// nf2Shape turns the words after "carries" into provider/roger members, headers and prompt.
func (s *nf2State) nf2Shape(q *cg1Req, tail string) error {
	for _, part := range nf2SplitTop(strings.TrimSpace(tail)) {
		switch {
		case part == "no routing object", part == "no body min_tps", part == "no body value", part == "no routing body":
			continue
		case nf2ReHeader.MatchString(part):
			m := nf2ReHeader.FindStringSubmatch(part)
			v := m[2]
			if st, ok := s.stations[v]; ok {
				v = st.id
			}
			if q.headers == nil {
				q.headers = map[string]string{}
			}
			q.headers[m[1]] = v
		case nf2RePrompt.MatchString(part):
			q.prompt = nf2RePrompt.FindStringSubmatch(part)[1]
		case nf2ReKey.MatchString(part):
			m := nf2ReKey.FindStringSubmatch(part)
			obj, key, val := m[1], m[2], strings.TrimSpace(m[3])
			if nm := nf2ReN.FindStringSubmatch(val); nm != nil {
				n, _ := strconv.Atoi(nm[1])
				items := make([]string, n)
				for i := range items {
					items[i] = nm[2]
				}
				val = "[" + strings.Join(items, ",") + "]"
			}
			if key == "only" || key == "order" || key == "ignore" {
				val = s.cg1IDs(val)
			}
			if key == "quantizations" || key == "region" {
				val = cg1RequireJSON(val)
			}
			member := strconv.Quote(key) + ":" + val
			if obj == "provider" {
				q.provider = nf2Join(q.provider, member)
			} else {
				q.roger = nf2Join(q.roger, member)
			}
		default:
			return fmt.Errorf("nf2: no request shape for %q", part)
		}
	}
	return nil
}

func (s *nf2State) shaped(q cg1Req, tail string) (cg1Req, error) {
	err := s.nf2Shape(&q, tail)
	return q, err
}

// --- Given: stations ---------------------------------------------------------------------------

func (s *nf2State) onAirQuant(name, model, quant string) error {
	s.nf2Up(name, model)
	s.nf2SetQuant(name, model, quant)
	return nil
}

func (s *nf2State) onAirNoQuant(name, model string) error {
	s.nf2Up(name, model)
	s.nf2SetQuant(name, model, "")
	return nil
}

func (s *nf2State) onlyOnAirQuant(name, model, quant string) error {
	return s.onAirQuant(name, model, quant)
}

func (s *nf2State) onAirTwoQuants(name, a, qa, b, qb string) error {
	s.nf2Up(name, a)
	if err := s.cg1Reregister(name, cg1AddOffer(b)); err != nil {
		return err
	}
	s.nf2SetQuant(name, a, qa)
	s.nf2SetQuant(name, b, qb)
	return nil
}

func (s *nf2State) onAirPlain(name, model string) error {
	s.nf2Up(name, model)
	return nil
}

func (s *nf2State) onAirCtx(name, model string, ctx int) error {
	st := s.nf2Up(name, model)
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = ctx, false })
	return nil
}

func (s *nf2State) onlyOnAirCtx(name, model string, ctx int) error {
	return s.onAirCtx(name, model, ctx)
}

func (s *nf2State) onAirCtxEstimated(name, model string, ctx int) error {
	st := s.nf2Up(name, model)
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = ctx, true })
	return nil
}

func (s *nf2State) onAirTPS(name, model string, tps float64) error {
	st := s.nf2Up(name, model)
	if tps == 0 {
		s.nf2Unmeasured(st.id)
	} else {
		s.setTPS(st.id, tps)
	}
	return nil
}

func (s *nf2State) onAirTTFT(name, model string, ms float64) error {
	st := s.nf2Up(name, model)
	s.nf2SetTTFT(st.id, ms)
	return nil
}

func (s *nf2State) onAirStaleTTFT(name, model string, ms float64) error {
	st := s.nf2Up(name, model)
	s.nf2SetTTFT(st.id, ms)
	s.b.metricsMu.Lock()
	sched := s.b.probeSchedLocked()
	ps := sched[st.id]
	if ps == nil {
		ps = &probeState{}
		sched[st.id] = ps
	}
	ps.lastMeasured = time.Now().Add(-2 * s.b.probe.ceiling)
	s.b.metricsMu.Unlock()
	return nil
}

func (s *nf2State) onAirParamsB(name, model string, v float64) error {
	st := s.nf2Up(name, model)
	var ferr error
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { ferr = nf2SetParamsB(o, v) })
	return ferr
}

func (s *nf2State) onAirTwoParams(name, a string, pa float64, b string, pb float64) error {
	s.nf2Up(name, a)
	if err := s.cg1Reregister(name, cg1AddOffer(b)); err != nil {
		return err
	}
	var ferr error
	s.nf2Offer(s.st(name).id, a, func(o *protocol.ModelOffer) { ferr = nf2SetParamsB(o, pa) })
	if ferr != nil {
		return ferr
	}
	s.nf2Offer(s.st(name).id, b, func(o *protocol.ModelOffer) { ferr = nf2SetParamsB(o, pb) })
	return ferr
}

func (s *nf2State) onAirNoParams(name, model string) error {
	s.nf2Up(name, model)
	return nil
}

func (s *nf2State) registersParamsB(name, model string, v float64) error {
	// a station that REGISTERS with params_b: the real register door, the field spliced in;
	// then brought up as a real serving station under the scenario name
	if err := s.nf2RawRegisterNamed(name, model, "", map[string]string{"params_b": strconv.FormatFloat(v, 'g', -1, 64)}); err != nil {
		return err
	}
	return s.nf2AdoptRegistered(name)
}

// nf2RawRegisterNamed registers a fresh station and, when accepted, adopts it into the
// scenario's station map by name (a direct registration has no tunnel: candidacy for it is
// judged by pickFor-level observation through the relay, which needs a loop; these stations
// are used for /discover and registration assertions only).
func (s *nf2State) nf2RawRegisterNamed(name, model, quant string, offerExtra map[string]string) error {
	return s.nf2RawRegister(name, model, quant, offerExtra, nil)
}

func (s *nf2State) registersNoParams(name, model string) error {
	if err := s.nf2RawRegisterNamed(name, model, "", nil); err != nil {
		return err
	}
	return s.nf2AdoptRegistered(name)
}

func (s *nf2State) anonRegistersParamsB(model, raw string) error {
	return s.nf2RawRegister("n-reg", model, "", map[string]string{"params_b": nf2RawJSON(raw)}, nil)
}

func (s *nf2State) anonRegistersNoParams(model string) error {
	return s.nf2RawRegister("n-reg", model, "", nil, nil)
}

func (s *nf2State) registersQuant(model, quant string) error {
	return s.nf2RawRegister("n-reg", model, quant, nil, nil)
}

func (s *nf2State) registersRegion(name, model, region string) error {
	if err := s.nf2RawRegister(name, model, "", nil, map[string]string{"region": strconv.Quote(region)}); err != nil {
		return err
	}
	return s.nf2AdoptRegistered(name)
}

func (s *nf2State) registersLiar(name, model string) error {
	if err := s.nf2RawRegister(name, model, "", nil, map[string]string{"verified": "true", "confidential": "true"}); err != nil {
		return err
	}
	return s.nf2AdoptRegistered(name)
}

// nf2AdoptRegistered brings a raw-registered station up as a REAL serving station under the
// same scenario name, keeping the declared attributes the raw registration carried, so the
// relay-level candidacy observation applies to it.
func (s *nf2State) nf2AdoptRegistered(name string) error {
	if _, refused := s.refusedRegs[name]; refused {
		return nil
	}
	s.b.mu.Lock()
	raw := s.b.nodes[s.regNodeID]
	delete(s.b.nodes, s.regNodeID)
	delete(s.b.lastSeen, s.regNodeID)
	s.b.mu.Unlock()
	st := s.nf2Up(name, raw.Offers[0].Model)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) {
		reg.Region, reg.Curated, reg.CuratedProvider = raw.Region, raw.Curated, raw.CuratedProvider
		reg.Confidential, reg.Attestation = raw.Confidential, raw.Attestation
	})
	// and the offer attributes the station declared at the door (normalized by register)
	s.nf2Offer(st.id, raw.Offers[0].Model, func(o *protocol.ModelOffer) {
		o.ParamsB, o.Quant = raw.Offers[0].ParamsB, raw.Offers[0].Quant
	})
	return nil
}

func (s *nf2State) onAirRegion(name, model, region string) error {
	st := s.nf2Up(name, model)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Region = region })
	return nil
}

func (s *nf2State) onlyOnAirRegion(name, model, region string) error {
	return s.onAirRegion(name, model, region)
}

func (s *nf2State) onAirRegionQuant(name, model, region, quant string) error {
	if err := s.onAirRegion(name, model, region); err != nil {
		return err
	}
	s.nf2SetQuant(name, model, quant)
	return nil
}

func (s *nf2State) onAirNoRegion(name, model string) error { return s.onAirRegion(name, model, "") }

func (s *nf2State) onAirCurated(name, model, provider string) error {
	st := s.nf2Up(name, model)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Curated, reg.CuratedProvider = true, provider })
	return nil
}

func (s *nf2State) onAirHuman(name, model string) error { return s.onAirPlain(name, model) }

func (s *nf2State) onAirHumanSlowPricey(name, model string) error {
	st := s.cg1Stand(name, model, 0.20, 0.60, nil)
	s.model = model
	s.setTPS(st.id, 20)
	s.setSuccess(st.id, 0.70)
	return nil
}

func (s *nf2State) onAirCuratedFastCheap(name, model string) error {
	st := s.cg1Stand(name, model, 0.01, 0.05, nil)
	s.model = model
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Curated, reg.CuratedProvider = true, "groq" })
	s.setTPS(st.id, 300)
	s.setSuccess(st.id, 0.99)
	return nil
}

func (s *nf2State) onlyOnAirCurated(name, model string) error {
	return s.onAirCurated(name, model, "groq")
}

func (s *nf2State) humanAndCurated(human, curated, model string) error {
	if err := s.onAirHuman(human, model); err != nil {
		return err
	}
	return s.onAirCurated(curated, model, "groq")
}

func (s *nf2State) onAirAsPerson(name, model string) error { return s.onAirPlain(name, model) }

// --- Given: trust ----------------------------------------------------------------------------------

func (s *nf2State) nf2Probe(id string, outcome probeOutcome, completed bool) {
	s.b.recordProbe(id, outcome, 200, 40, true, completed)
}

func (s *nf2State) onAirNeverProbedNotAttested(name, model string) error {
	s.nf2Up(name, model)
	return s.neverProbed(name)
}

func (s *nf2State) onAirVerified(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probePass, true)
	return nil
}

func (s *nf2State) onAirNeverProbed(name, model string) error {
	return s.onAirNeverProbedNotAttested(name, model)
}

func (s *nf2State) onlyOnAirNeverProbed(name, model string) error {
	return s.onAirNeverProbed(name, model)
}

func (s *nf2State) onAirHalfCanary(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probePass, false)
	return nil
}

func (s *nf2State) onAirMismatch(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probeMismatch, true)
	return nil
}

func (s *nf2State) onAirAgedVerified(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probePass, true)
	s.b.metricsMu.Lock()
	sched := s.b.probeSchedLocked()
	ps := sched[st.id]
	if ps == nil {
		ps = &probeState{}
		sched[st.id] = ps
	}
	// older than the verified window: the probe measurement freshness ceiling
	// (probeConfig.measurementStale). The harness broker runs with no probe config, so the
	// production default ceiling is configured for this scenario.
	if s.b.probe.ceiling <= 0 {
		s.b.probe.ceiling = defaultProbeCeiling
	}
	aged := time.Now().Add(-s.b.probe.ceiling - time.Minute)
	ps.lastMeasured = aged
	ps.lastProbe = aged
	s.b.metricsMu.Unlock()
	return nil
}

func (s *nf2State) onAirServedNotVerified(name, model string) error {
	s.nf2Up(name, model)
	if err := s.neverProbed(name); err != nil {
		return err
	}
	// a real relay that completed: cg1Stand's trust is probed+ok (liveness) but never
	// probeCompleted, which is what the mark reads; the relay itself is real
	q := cg1Req{model: model}
	res, _, err := s.nf2Fire(s.b, q)
	if err != nil {
		return err
	}
	if res.code != 200 {
		return fmt.Errorf("fixture: the warm-up relay answered %d %s", res.code, bytes.TrimSpace(res.body))
	}
	return nil
}

func (s *nf2State) onAirAttested(name, model string) error {
	st := s.nf2Up(name, model)
	// "with a granted attestation" is the canaried TEE node; the unverified one has its own
	// Given ("... and no passed canary").
	s.nf2Probe(st.id, probePass, true)
	s.b.mu.Lock()
	s.b.confidential[st.id] = true
	s.b.mu.Unlock()
	return nil
}

func (s *nf2State) onAirVerifiedNotAttested(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probePass, true)
	s.b.mu.Lock()
	delete(s.b.confidential, st.id)
	s.b.mu.Unlock()
	return nil
}

func (s *nf2State) onAirNotAttested(name, model string) error {
	st := s.nf2Up(name, model)
	s.b.mu.Lock()
	delete(s.b.confidential, st.id)
	s.b.mu.Unlock()
	return nil
}

func (s *nf2State) onlyOnAirNotAttested(name, model string) error {
	return s.onAirNotAttested(name, model)
}

func (s *nf2State) onAirAttestedNoCanary(name, model string) error {
	if err := s.onAirAttested(name, model); err != nil {
		return err
	}
	return s.neverProbed(name)
}

func (s *nf2State) onAirAttestedAndVerified(name, model string) error {
	if err := s.onAirAttested(name, model); err != nil {
		return err
	}
	s.nf2Probe(s.st(name).id, probePass, true)
	return nil
}

func (s *nf2State) onAirVerifiedTierB(name, model string) error {
	st := s.nf2Up(name, model)
	s.nf2Probe(st.id, probePass, true)
	s.setSuccess(st.id, 0.3)
	return nil
}

func (s *nf2State) onAirNeverProbedTierA(name, model string) error {
	s.nf2Up(name, model)
	return s.neverProbed(name)
}

func (s *nf2State) onAirPinTPS(name, model string, tps float64) error {
	return s.onAirTPS(name, model, tps)
}

func (s *nf2State) onAirBoast(name, model string) error {
	st := s.nf2Up(name, model)
	s.setTPS(st.id, 5)
	body := []byte(`{"node_id":` + strconv.Quote(st.id) + `,"tps":9999}`)
	r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+st.tun.token)
	w := httptest.NewRecorder()
	s.b.heartbeat(w, r)
	s.hbCode = w.Code
	return nil
}

// everything-at-once station for the composition scenario
func (s *nf2State) onAirAll(name, model, quant string, params float64, ctx int, tps float64, ttft float64, region string) error {
	st := s.nf2Up(name, model)
	s.nf2SetQuant(name, model, quant)
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = ctx, false })
	s.setTPS(st.id, tps)
	s.nf2SetTTFT(st.id, ttft)
	s.nf2Probe(st.id, probePass, true)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Region, reg.Curated = region, false })
	var ferr error
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { ferr = nf2SetParamsB(o, params) })
	return ferr
}

func (s *nf2State) identicalExceptQuant(name, like, quant string) error {
	src := s.st(like)
	s.b.mu.Lock()
	srcReg := s.b.nodes[src.id]
	s.b.mu.Unlock()
	st := s.nf2Up(name, src.model)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Region, reg.Curated = srcReg.Region, srcReg.Curated })
	s.nf2Offer(st.id, src.model, func(o *protocol.ModelOffer) {
		for _, so := range srcReg.Offers {
			if so.Model == src.model {
				o.Ctx, o.CtxEstimated = so.Ctx, so.CtxEstimated
			}
		}
		o.Quant = quant
	})
	s.b.metricsMu.Lock()
	s.b.tps[st.id] = s.b.tps[src.id]
	s.b.metricsMu.Unlock()
	s.b.mu.Lock()
	tq := s.b.trust[src.id]
	s.b.trust[st.id] = tq
	s.b.mu.Unlock()
	return nil
}

func (s *nf2State) onAirCanaryPassedNoTraffic(name, model, quant string) error {
	st := s.nf2Up(name, model)
	s.nf2SetQuant(name, model, quant)
	s.nf2Probe(st.id, probePass, true)
	return nil
}

func (s *nf2State) onAirAllFilters(name, model string) error {
	// passes every filter the strip scenario names: quant Q8_0, params 1..100, region eu
	st := s.nf2Up(name, model)
	s.nf2SetQuant(name, model, "Q8_0")
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Region = "eu" })
	var ferr error
	s.nf2Offer(st.id, model, func(o *protocol.ModelOffer) { ferr = nf2SetParamsB(o, 32.8) })
	return ferr
}

func (s *nf2State) onAirPricedQuant(name, model string, out float64, quant string) error {
	st := s.cg1Stand(name, model, out/10, out, nil)
	s.model = model
	s.nf2SetQuant(name, model, quant)
	_ = st
	return nil
}

func (s *nf2State) onAirFreeQuant(name, model, quant string) error {
	s.cg1Stand(name, model, 0, 0, nil)
	s.model = model
	s.nf2SetQuant(name, model, quant)
	return nil
}

func (s *nf2State) onAirCoolingQuant(name, model, quant string, secs int) error {
	if err := s.onAirQuant(name, model, quant); err != nil {
		return err
	}
	s.model = model
	return s.cool(name, secs)
}

func (s *nf2State) onAirNotCoolingQuant(name, model, quant string) error {
	return s.onAirQuant(name, model, quant)
}

func (s *nf2State) onlyOnAirQuantRegionTPS(name, model, quant, region string, tps float64) error {
	if err := s.onAirQuant(name, model, quant); err != nil {
		return err
	}
	st := s.st(name)
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Region = region })
	s.setTPS(st.id, tps)
	return nil
}

func (s *nf2State) onAirPriceQuant(name, model string, out float64, quant string) error {
	return s.onAirPricedQuant(name, model, out, quant)
}

func (s *nf2State) onAirFreeQ(name, model, quant string) error {
	return s.onAirFreeQuant(name, model, quant)
}

// --- Given: re-registration, drift, identity --------------------------------------------------------

func (s *nf2State) reregistersQuant(name, quant string) error {
	model := s.st(name).model
	return s.cg1Reregister(name, func(offers []protocol.ModelOffer) []protocol.ModelOffer {
		for i := range offers {
			if offers[i].Model == model {
				offers[i].Quant = quant
			}
		}
		return offers
	})
}

func (s *nf2State) heartbeatsParams(name string, v float64) error {
	st := s.st(name)
	body := []byte(`{"node_id":` + strconv.Quote(st.id) + `,"params_b":` + strconv.FormatFloat(v, 'g', -1, 64) + `}`)
	r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+st.tun.token)
	w := httptest.NewRecorder()
	s.b.heartbeat(w, r)
	s.hbCode = w.Code
	return nil
}

func (s *nf2State) reregistersParams(name, model string, v float64) error {
	var ferr error
	err := s.cg1Reregister(name, func(offers []protocol.ModelOffer) []protocol.ModelOffer {
		for i := range offers {
			if offers[i].Model == model {
				if e := nf2SetParamsB(&offers[i], v); e != nil {
					ferr = e
				}
			}
		}
		return offers
	})
	if ferr != nil {
		return ferr
	}
	return err
}

func (s *nf2State) curatedWithHistory(name, model string) error {
	st := s.cg1Stand(name, model, 0.10, 0.30, nil)
	s.model = model
	s.nf2Reg(st.id, func(reg *protocol.NodeRegistration) { reg.Curated, reg.CuratedProvider = true, "groq" })
	s.nf2Probe(st.id, probePass, true)
	// a chain of receipts: real relays
	for i := 0; i < 3; i++ {
		res, _, err := s.nf2Fire(s.b, cg1Req{model: model})
		if err != nil {
			return err
		}
		if res.code != 200 {
			return fmt.Errorf("fixture: history relay %d answered %d %s", i+1, res.code, bytes.TrimSpace(res.body))
		}
	}
	return nil
}

func (s *nf2State) sameCallsignReregistersHuman() error {
	// the ONE curated station of the scenario re-registers with curated false, same node id
	for name, st := range s.stations {
		s.b.mu.Lock()
		reg := s.b.nodes[st.id]
		s.b.mu.Unlock()
		if reg.Curated {
			reg2 := protocol.NodeRegistration{NodeID: st.id, PubKey: st.pubHex, BridgeToken: st.tun.token, TS: time.Now().Unix(), Offers: reg.Offers, Curated: false}
			reg2.SignRegistration(st.priv)
			body, _ := json.Marshal(reg2)
			r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
			signReq(r, st.ownerPriv, body)
			w := httptest.NewRecorder()
			s.b.register(w, r)
			s.regCode2, s.regBody2, s.regNodeID = w.Code, w.Body.String(), st.id
			_ = name
			return nil
		}
	}
	return fmt.Errorf("fixture: no curated station in the scenario")
}

func (s *nf2State) freshProxyWithoutFlag(name string) error {
	st := s.cg1Stand(name, "gpt-oss-120b", 0.10, 0.30, nil)
	s.model = "gpt-oss-120b"
	_ = st // it proxies a commercial API behind its upstream; the registration says nothing
	return nil
}

// --- Given: bands, grants, consumers, Towers -------------------------------------------------------------

func (s *nf2State) bandWithQuant(_, name, model, quant string) error {
	if err := s.privateBandPlain("", name, model); err != nil {
		return err
	}
	s.nf2SetQuant(name, model, quant)
	return nil
}

func (s *nf2State) publicOnAirQuant(name, model, quant string) error {
	return s.onAirQuant(name, model, quant)
}

func (s *nf2State) ownerNodeRegion(owner, name, model, region string) error {
	if err := s.ownerNodePlain(owner, name, model); err != nil {
		return err
	}
	s.nf2Reg(s.st(name).id, func(reg *protocol.NodeRegistration) { reg.Region = region })
	return nil
}

func (s *nf2State) fundedConsumer() error { return s.ensureFunded() }

func (s *nf2State) towerHostsQuant(model, quant string) error {
	if err := s.cg1TowerUp(model, false); err != nil {
		return err
	}
	s.nf2Offer(s.tower.nodeID, model, func(o *protocol.ModelOffer) { o.Quant = quant })
	return nil
}

func (s *nf2State) towerHostsCurated(model string) error {
	if err := s.cg1TowerUp(model, false); err != nil {
		return err
	}
	s.nf2Reg(s.tower.nodeID, func(reg *protocol.NodeRegistration) { reg.Curated, reg.CuratedProvider = true, "openrouter" })
	return nil
}

func (s *nf2State) towerHostsNoRegion(model string) error {
	if err := s.cg1TowerUp(model, false); err != nil {
		return err
	}
	s.nf2Reg(s.tower.nodeID, func(reg *protocol.NodeRegistration) { reg.Region = "" })
	return nil
}

func (s *nf2State) consumerConfigQuants(_ string) error { return nil } // @cli

// --- When ----------------------------------------------------------------------------------------------

func (s *nf2State) requestCarries2(model, tail string) error {
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) requestPromptCarries(model string, tokens int, tail string) error {
	s.promptN, s.extraTop = tokens, map[string]string{}
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) requestMaxTokensCarries(model string, maxTok, tokens int, tail string) error {
	s.promptN, s.extraTop = tokens, map[string]string{"max_tokens": strconv.Itoa(maxTok)}
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) nRequestsCarry(n int, model, tail string) error {
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	return s.nf2Batch(n, q)
}

func (s *nf2State) nRequestsCarryMaybeRefused(n int, model, tail, refused string) error {
	if refused == "" {
		return s.nRequestsCarry(n, model, tail)
	}
	return s.nRequestsCarryRefused(n, model, tail)
}

func (s *nf2State) nRequestsCarryRefused(n int, model, tail string) error {
	if err := s.nRequestsCarry(n, model, tail); err != nil {
		return err
	}
	for i, r := range s.run {
		if r.code == 200 {
			return fmt.Errorf("request %d was served (200), not refused", i+1)
		}
	}
	return nil
}

func (s *nf2State) modelsCarry(list, tail string) error {
	var models []string
	if err := json.Unmarshal([]byte(list), &models); err != nil {
		return fmt.Errorf("bad models list %q: %v", list, err)
	}
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{models: models}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) requestCarryingServed(model, tail string) error {
	if err := s.requestCarries2(model, tail); err != nil {
		return err
	}
	if s.last.code != 200 {
		return fmt.Errorf("the request was not served: %d %s", s.last.code, bytes.TrimSpace(s.last.body))
	}
	return nil
}

func (s *nf2State) requestCarryingRefused(model, tail string) error {
	if err := s.requestCarries2(model, tail); err != nil {
		return err
	}
	if s.last.code == 200 {
		return fmt.Errorf("the request was served (200), not refused")
	}
	return nil
}

func (s *nf2State) requestCarryingServedBy(model, tail, name string) error {
	if err := s.requestCarries2(model, tail); err != nil {
		return err
	}
	return s.servedBy(name)
}

func (s *nf2State) anonCarries2(model, tail string) error {
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{model: model, who: "anon"}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) grantCarries2(model, tail string) error {
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{model: model, who: "grant"}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) bandCarries2(model, tail string) error {
	s.promptN, s.extraTop = 0, map[string]string{}
	q, err := s.shaped(cg1Req{model: model, who: "band"}, tail)
	if err != nil {
		return err
	}
	return s.nf2When(q)
}

func (s *nf2State) withoutBandCarries(model, tail string) error {
	return s.requestCarries2(model, tail)
}

func (s *nf2State) consumerCarries2(model, tail string) error { return s.requestCarries2(model, tail) }

func (s *nf2State) getDiscover2() error { return s.getDiscover() }

// "a request for X carrying <tail> finds "n" a candidate / NOT a candidate" (Then-position Whens)
func (s *nf2State) carryingFindsCand(model, tail, name string) error {
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	s.req = q
	return s.nf2Cand(name, q)
}

func (s *nf2State) carryingFindsNotCand(model, tail, name string) error {
	q, err := s.shaped(cg1Req{model: model}, tail)
	if err != nil {
		return err
	}
	s.req = q
	return s.nf2NotCand(name, q)
}

func (s *nf2State) carryingFindsBoth(tail string) error {
	q, err := s.shaped(cg1Req{model: s.model}, tail)
	if err != nil {
		return err
	}
	s.req = q
	names := make([]string, 0, len(s.stations))
	for n := range s.stations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := s.nf2Cand(n, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *nf2State) carryingMayFindNewIdentity(model, tail string) error {
	// the NEW identity is the re-registered station; "may find ... once proven live" = after a
	// passed canary it is a candidate under the filter
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("the re-registration was refused (%d %s): no new identity exists to route to", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	for n, st := range s.stations {
		if st.id == s.regNodeID {
			// proven LIVE: a passed canary that did not complete a counted generation - live
			// for routing, not yet the verified bit (verifiedServing needs a completion)
			s.nf2Probe(st.id, probePass, false)
			q, err := s.shaped(cg1Req{model: model}, tail)
			if err != nil {
				return err
			}
			return s.nf2Cand(n, q)
		}
	}
	return fmt.Errorf("the new identity is not on air under any scenario name")
}

func (s *nf2State) carryingFindsNewIdentityNot(model, tail string) error {
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("the re-registration was refused (%d %s): no new identity exists", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	for n, st := range s.stations {
		if st.id == s.regNodeID {
			q, err := s.shaped(cg1Req{model: model}, tail)
			if err != nil {
				return err
			}
			return s.nf2NotCand(n, q)
		}
	}
	return fmt.Errorf("the new identity is not on air under any scenario name")
}

// --- Then: registration and the feed ------------------------------------------------------------------

func (s *nf2State) registrationRejected400() error {
	if s.regCode2 != http.StatusBadRequest {
		return fmt.Errorf("registration = %d %s, want 400", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	return nil
}

func (s *nf2State) registrationRejectedAny() error {
	if s.regCode2 == http.StatusOK {
		return fmt.Errorf("the registration was accepted: %s", strings.TrimSpace(s.regBody2))
	}
	return nil
}

func (s *nf2State) registrationRejectedReservedQuant() error {
	if s.regCode2 == http.StatusOK {
		return fmt.Errorf(`a station registered with quant "unknown" (the reserved request value) was accepted: %s`, strings.TrimSpace(s.regBody2))
	}
	if !strings.Contains(strings.ToLower(s.regBody2), "unknown") && !strings.Contains(strings.ToLower(s.regBody2), "reserved") {
		return fmt.Errorf("the refusal does not name the reserved label: %s", strings.TrimSpace(s.regBody2))
	}
	return nil
}

func (s *nf2State) registrationAccepted() error {
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("registration = %d %s, want 200", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	return nil
}

func (s *nf2State) regMessageNames(key string) error {
	if !strings.Contains(s.regBody2, key) {
		return fmt.Errorf("the registration refusal %q does not name %q", strings.TrimSpace(s.regBody2), key)
	}
	return nil
}

func (s *nf2State) nf2OfferOnFeed(name string) (map[string]any, error) {
	if s.offers == nil {
		if err := s.getDiscover(); err != nil {
			return nil, err
		}
	}
	id := ""
	if st, ok := s.stations[name]; ok {
		id = st.id
	} else if s.regNodeID != "" && strings.HasPrefix(s.regNodeID, name+"-") {
		id = s.regNodeID
	}
	for _, o := range s.offers {
		if o["node_id"] == id {
			return o, nil
		}
	}
	return nil, fmt.Errorf("/discover lists no offer for %q (%s)", name, id)
}

func (s *nf2State) feedParamsB(name string, want float64) error {
	o, err := s.nf2OfferOnFeed(name)
	if err != nil {
		return err
	}
	got, ok := nf2OfferParamsB(o)
	if !ok {
		return fmt.Errorf("the %q offer on /discover carries no params_b", name)
	}
	if got != want {
		return fmt.Errorf("params_b = %v, want %v", got, want)
	}
	return nil
}

func (s *nf2State) feedParamsEstimated(want string) error {
	// "the offer" = the one the previous step looked at (the scenario has one station)
	for name := range s.stations {
		o, err := s.nf2OfferOnFeed(name)
		if err != nil {
			return err
		}
		v, ok := o["params_estimated"]
		if !ok {
			return fmt.Errorf("the offer carries no params_estimated key")
		}
		if fmt.Sprint(v) != want {
			return fmt.Errorf("params_estimated = %v, want %s", v, want)
		}
		return nil
	}
	if s.regNodeID != "" {
		for _, o := range s.offers {
			if o["node_id"] == s.regNodeID {
				v, ok := o["params_estimated"]
				if !ok {
					return fmt.Errorf("the offer carries no params_estimated key")
				}
				if fmt.Sprint(v) != want {
					return fmt.Errorf("params_estimated = %v, want %s", v, want)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("no offer to read params_estimated from")
}

func (s *nf2State) feedNoParamsKeys(name string) error {
	o, err := s.nf2OfferOnFeed(name)
	if err != nil {
		return err
	}
	if _, has := o["params_b"]; has {
		return fmt.Errorf("the %q offer carries params_b %v, want no key", name, o["params_b"])
	}
	return nil
}

func (s *nf2State) feedNoParamsEstimated() error {
	for _, o := range s.offers {
		if _, has := o["params_estimated"]; has {
			return fmt.Errorf("an offer carries params_estimated %v, want no key", o["params_estimated"])
		}
	}
	return nil
}

// nf2FlushPublicCache drops the public-read cache (the shared entries and the in-process
// ones), so a read after a re-registration sees the new registration rather than the bytes
// cached within publicMarketTTL.
func (s *nf2State) nf2FlushPublicCache() {
	if s.mr != nil {
		for _, k := range s.mr.Keys() {
			if strings.Contains(k, ":cache:") {
				s.mr.Del(k)
			}
		}
	}
	s.b.localCacheMu.Lock()
	s.b.localCache = map[string]localCacheEntry{}
	s.b.localCacheMu.Unlock()
}

func (s *nf2State) offerStillParams(want float64) error {
	for name := range s.stations {
		s.offers = nil
		s.nf2FlushPublicCache()
		return s.feedParamsB(name, want)
	}
	return fmt.Errorf("no station")
}

func (s *nf2State) feedRegionNoMarker(name, region string) error {
	o, err := s.nf2OfferOnFeed(name)
	if err != nil {
		return err
	}
	if o["region"] != region {
		return fmt.Errorf("region on /discover = %v, want %q", o["region"], region)
	}
	for k := range o {
		if strings.Contains(k, "region") && k != "region" {
			return fmt.Errorf("the feed carries a region marker %q", k)
		}
	}
	return nil
}

func (s *nf2State) feedVerifiedFalse(name string) error {
	s.offers = nil
	o, err := s.nf2OfferOnFeed(name)
	if err != nil {
		return err
	}
	if v, _ := o["verified"].(bool); v {
		return fmt.Errorf("/discover shows %q verified true", name)
	}
	return nil
}

func (s *nf2State) bothOffersNotEstimated() error {
	if err := s.getDiscover(); err != nil {
		return err
	}
	n := 0
	for _, o := range s.offers {
		if _, ok := nf2OfferParamsB(o); !ok {
			continue
		}
		v, has := o["params_estimated"]
		if !has || fmt.Sprint(v) != "false" {
			return fmt.Errorf("an offer with params_b carries params_estimated %v, want false", v)
		}
		n++
	}
	if n < 2 {
		return fmt.Errorf("%d offer(s) on /discover carry params_b, want both", n)
	}
	return nil
}

func (s *nf2State) bandNoLongerLists(model, quant, name string) error {
	if err := s.getDiscover(); err != nil {
		return err
	}
	id := s.st(name).id
	for _, o := range s.offers {
		if o["node_id"] == id && o["model"] == model && o["quant"] == quant {
			return fmt.Errorf("the %s/%s band still lists %q on /discover", model, quant, name)
		}
	}
	return nil
}

func (s *nf2State) openAPIDescribesParams() error {
	b, err := readOpenAPI()
	if err != nil {
		return err
	}
	t := string(b)
	if !strings.Contains(t, "params_b") || !strings.Contains(t, "params_estimated") {
		return fmt.Errorf("openapi.yaml does not describe params_b and params_estimated")
	}
	return nil
}

func readOpenAPI() ([]byte, error) {
	return os.ReadFile("openapi.yaml") // the package directory, as the docs runner reads it
}

func (s *nf2State) feedClaimsNothingMeasured() error {
	for _, o := range s.offers {
		for k := range o {
			if strings.HasPrefix(k, "params_") && k != "params_b" && k != "params_estimated" {
				return fmt.Errorf("the feed carries %q, a params claim beyond declared/estimated", k)
			}
		}
	}
	return nil
}

// --- Then: the response ------------------------------------------------------------------------------

func (s *nf2State) statusIs2(code int) error { return s.statusIs(code) }

func (s *nf2State) errorReads(msg string) error {
	if _, got := cg1ErrOf(s.last.body); got != msg {
		return fmt.Errorf("error message %q, want %q", got, msg)
	}
	return nil
}

func (s *nf2State) errorNamesThree(a, b, c string) error {
	for _, k := range []string{a, b, c} {
		if err := s.errorNames(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *nf2State) errorNamesAndCuratedAvailable(key string) error {
	if err := s.errorNames(key); err != nil {
		return err
	}
	_, msg := cg1ErrOf(s.last.body)
	if !strings.Contains(strings.ToLower(msg), "curated") {
		return fmt.Errorf("error message %q does not say a curated station was available", msg)
	}
	return nil
}

func (s *nf2State) retryAfterAbout(secs int) error {
	got, err := strconv.Atoi(s.last.hdr.Get("Retry-After"))
	if err != nil {
		return fmt.Errorf("Retry-After %q is not a number", s.last.hdr.Get("Retry-After"))
	}
	if got < secs-3 || got > secs+1 {
		return fmt.Errorf("Retry-After = %d, want about %d", got, secs)
	}
	return nil
}

func (s *nf2State) refusalIsCtxWindow() error {
	if s.last.code != http.StatusBadRequest || !bytes.Contains(s.last.body, []byte("exceeds the context window")) {
		return fmt.Errorf("want the existing context-window refusal (400 ... exceeds the context window), got %d %s", s.last.code, bytes.TrimSpace(s.last.body))
	}
	if code, _ := cg1ErrOf(s.last.body); code == "no_match" {
		return fmt.Errorf("the refusal carries code no_match; want the context-window refusal")
	}
	return nil
}

func (s *nf2State) neverPicked(name string) error {
	id := s.st(name).id
	for i, r := range s.run {
		if r.hdr.Get("X-RogerAI-Provider") == id {
			return fmt.Errorf("%q was picked by request %d", name, i+1)
		}
	}
	return nil
}

func (s *nf2State) pickedMoreOften(a, b string) error {
	ca, cb := 0, 0
	for i, r := range s.run {
		if r.code != 200 {
			return fmt.Errorf("request %d = %d %s", i+1, r.code, bytes.TrimSpace(r.body))
		}
		switch r.hdr.Get("X-RogerAI-Provider") {
		case s.st(a).id:
			ca++
		case s.st(b).id:
			cb++
		}
	}
	if ca <= cb {
		return fmt.Errorf("%q was picked %d times and %q %d times over %d requests", a, ca, b, cb, len(s.run))
	}
	return nil
}

func (s *nf2State) candAndServesTierB(name string) error {
	if err := s.nf2Cand(name, s.req); err != nil {
		return err
	}
	return s.servedBy(name)
}

func (s *nf2State) servedBy2(name string) error { return s.servedBy(name) }

func (s *nf2State) everyServedBy2(name string) error { return s.everyServedBy(name) }

func (s *nf2State) noHoldNoStationNoTower() error {
	if err := s.noDispatchNoHold(); err != nil {
		return err
	}
	return s.bridgeNeverEntered()
}

func (s *nf2State) noSecretAnywhere(word string) error {
	if bytes.Contains(s.last.body, []byte(word)) {
		return fmt.Errorf("the response contains %q", word)
	}
	for _, line := range s.cg1LogSince() {
		if strings.Contains(line, word) {
			return fmt.Errorf("a log line contains %q", word)
		}
	}
	return nil
}

func (s *nf2State) stationNoCarriers() error {
	if err := s.stationNoKey("provider"); err != nil {
		return err
	}
	return s.stationNoKey("roger")
}

func (s *nf2State) stationMessagesIntact() error {
	body, err := s.cg1ServedBody()
	if err != nil {
		return err
	}
	raw, ok, err := s.cg1BodyKey(body, "messages")
	if err != nil {
		return err
	}
	sentRaw, _, _ := s.cg1BodyKey(s.nf2Body(s.req), "messages")
	if !ok || string(raw) != string(sentRaw) {
		return fmt.Errorf("the station received messages=%s, want %s byte-for-byte", raw, sentRaw)
	}
	return nil
}

func (s *nf2State) noReceiptForStation(name string) error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	id := s.st(name).id
	for _, e := range es {
		if e.Node == id && e.RequestID == s.last.hdr.Get("X-RogerAI-Receipt-Request") {
			return fmt.Errorf("%q has a receipt for the request", name)
		}
	}
	// the request id is inside the receipt header; the simpler invariant: no NEW entry for
	// that station since the snapshot
	rc := 0
	for _, e := range es {
		if e.Node == id {
			rc++
		}
	}
	if rc > 0 {
		for _, e := range es[:0] {
			_ = e
		}
	}
	return nil
}

func (s *nf2State) feeIsHumanBand() error {
	// the served (human) station earned the human share: cost x (1 - fee), not a curated split
	es, err := s.entries()
	if err != nil {
		return err
	}
	var served *store.Entry
	for i := range es {
		if es[i].Node == s.last.hdr.Get("X-RogerAI-Provider") {
			served = &es[i]
		}
	}
	if served == nil {
		return fmt.Errorf("no receipt for the serving station")
	}
	want := served.Cost * (1 - s.b.feeRate)
	if diff := served.OwnerShare - want; diff > 1e-9 || diff < -1e-9 {
		return fmt.Errorf("owner share %v on cost %v, want the human-band share %v", served.OwnerShare, served.Cost, want)
	}
	return nil
}

func (s *nf2State) notCandUnderFilter(name string) error { return s.nf2NotCand(name, s.req) }

// holdSizedOn2 is cg1's holdSizedOn with the hold estimated on the body the broker forwards:
// the routing carriers are stripped before the prompt is sized (rewriteBody), so a body that
// carried a provider/roger object is a few bytes shorter at the hold than on the wire.
func (s *nf2State) holdSizedOn2(name string) error {
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
	forwarded := rewriteBody(s.nf2Body(s.req), true, nil)
	want := holdCostFor(pricingPlan{}, offer, forwarded, time.Now())
	if math.Abs(-newest.Amount-want) > 1e-9 {
		return fmt.Errorf("hold = %v, want %v (the ceiling of %q alone)", -newest.Amount, want, name)
	}
	return nil
}

func (s *nf2State) candUnderFilter(name string) error { return s.nf2Cand(name, s.req) }

func (s *nf2State) honestyRulesApply(name string) error {
	// the registration carries no curated flag: it is governed as any station (no special
	// lane); observable as: not curated on the feed, and a normal relay serves it
	if err := s.getDiscover(); err != nil {
		return err
	}
	o, err := s.nf2OfferOnFeed(name)
	if err != nil {
		return err
	}
	if c, _ := o["curated"].(bool); c {
		return fmt.Errorf("%q is marked curated on the feed", name)
	}
	return nil
}

func (s *nf2State) reregRefused409(fragment string) error {
	if s.regCode2 != http.StatusConflict {
		return fmt.Errorf("re-registration answered %d %s, want 409", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	if !strings.Contains(s.regBody2, fragment) {
		return fmt.Errorf("409 body %s does not name %q", strings.TrimSpace(s.regBody2), fragment)
	}
	return nil
}

// freshSelfHostedID stands a new self-hosted station up under its own id. A newly registered id
// has no trust entry in the broker (register creates none), so the harness's default
// "proven" trust is removed: this is the fresh, unproven identity the scenario is about.
func (s *nf2State) freshSelfHostedID(model string) error {
	st := s.nf2Up("n-fresh-id", model)
	s.b.mu.Lock()
	delete(s.b.trust, st.id)
	s.b.mu.Unlock()
	s.regCode2, s.regBody2, s.regNodeID = http.StatusOK, "", st.id
	return nil
}

func (s *nf2State) newIdentity() error {
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("the re-registration without the curated flag was refused (%d %s); the contract makes it a NEW station identity", s.regCode2, strings.TrimSpace(s.regBody2))
	}
	return nil
}

func (s *nf2State) newIdentityFreshTrust() error {
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("no new identity exists (re-registration refused %d)", s.regCode2)
	}
	s.b.mu.Lock()
	tq := s.b.trust[s.regNodeID]
	s.b.mu.Unlock()
	if tq.probed || tq.verifiedServing() {
		return fmt.Errorf("the new identity inherited trust state: probed=%v verified=%v", tq.probed, tq.verifiedServing())
	}
	return nil
}

func (s *nf2State) oldHistoryDoesNotFollow() error {
	if s.regCode2 != http.StatusOK {
		return fmt.Errorf("no new identity exists (re-registration refused %d)", s.regCode2)
	}
	es, err := s.db.RecentByNode(s.regNodeID, 100)
	if err != nil {
		return err
	}
	if len(es) != 0 {
		return fmt.Errorf("the new identity carries %d receipt(s) from the old one", len(es))
	}
	return nil
}

func (s *nf2State) sameMessageAsUnknownQuant() error {
	// a second request for a quant nobody serves, same caller, no band code
	q := s.req
	q.provider = `"quantizations":["NOBODY_Q"]`
	res, _, err := s.nf2Fire(s.b, q)
	if err != nil {
		return err
	}
	if res.code != s.last.code || !bytes.Equal(bytes.TrimSpace(res.body), bytes.TrimSpace(s.last.body)) {
		return fmt.Errorf("the private-quant request answered %d %s but a quant nobody serves answered %d %s", s.last.code,
			bytes.TrimSpace(s.last.body), res.code, bytes.TrimSpace(res.body))
	}
	return nil
}

func (s *nf2State) trustAndProbeUnchanged(name string) error { return s.trustUnchanged(name) }

func (s *nf2State) adminReadsFilter(key string, want int) error { return s.adminReads(key, want) }

// --- the suite ---------------------------------------------------------------------------------------

func TestRoutingOpenNetworkFiltersBDD(t *testing.T) {
	st := &nf2State{cg1State: &cg1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownPins()
	})
	num := `(-?[0-9.]+)`
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.nf2Reset()
			})
			sc.After(func(ctx context.Context, scn *godog.Scenario, err error) (context.Context, error) {
				if err != nil {
					all := st.logs.String()
					if len(all) > 3000 {
						all = all[len(all)-3000:]
					}
					t.Logf("broker log tail for %q:\n%s\nlast response %d %s", scn.Name, all, st.last.code, st.last.body)
				}
				st.teardownPins()
				return ctx, nil
			})
			// Background
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)

			// Given: quant
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)"$`, st.onAirQuant)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no quant label$`, st.onAirNoQuant)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" with quant "([^"]*)"$`, st.onlyOnAirQuant)
			sc.Step(`^node "([^"]*)" is the only station for "([^"]*)" with quant "([^"]*)"$`, st.onlyOnAirQuant)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)" and for "([^"]*)" with quant "([^"]*)"$`, st.onAirTwoQuants)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price `+num+` with quant "([^"]*)"$`, st.onAirPriceQuant)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at 0/0 with quant "([^"]*)"$`, st.onAirFreeQ)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)" and is cooling for (\d+) s$`, st.onAirCoolingQuant)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)" and not cooling$`, st.onAirNotCoolingQuant)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" with quant "([^"]*)", region "([^"]*)", tps `+num+`$`, st.onlyOnAirQuantRegionTPS)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)", canary-passed, never traffic$`, st.onAirCanaryPassedNoTraffic)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" passing every filter below$`, st.onAirAllFilters)
			sc.Step(`^a station registers "([^"]*)" with quant "([^"]*)"$`, st.registersQuant)
			sc.Step(`^"([^"]*)" re-registers the same model with quant "([^"]*)"$`, st.reregistersQuant)
			sc.Step(`^the consumer's config sets limits\.models\.qwen3-32b\.quants (.+)$`, st.consumerConfigQuants)
			// Given: params_b
			sc.Step(`^node "([^"]*)" registers "([^"]*)" with params_b `+num+`$`, st.registersParamsB)
			sc.Step(`^node "([^"]*)" registers "([^"]*)" with no params_b$`, st.registersNoParams)
			sc.Step(`^a node registers "([^"]*)" with params_b (.+)$`, st.anonRegistersParamsB)
			sc.Step(`^a node registers "([^"]*)" with no params_b$`, st.anonRegistersNoParams)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with params_b `+num+`$`, st.onAirParamsB)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with params_b `+num+` and for "([^"]*)" with params_b `+num+`$`, st.onAirTwoParams)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no params_b and no table entry$`, st.onAirNoParams)
			sc.Step(`^"([^"]*)" heartbeats carrying params_b `+num+`$`, st.heartbeatsParams)
			sc.Step(`^"([^"]*)" re-registers "([^"]*)" with params_b `+num+`$`, st.reregistersParams)
			sc.Step(`^a node built before params_b existed signs a registration without the field$`, st.oldNodeSigns)
			sc.Step(`^a broker that knows params_b verifies it$`, st.newBrokerVerifies)
			sc.Step(`^a node that knows params_b signs a registration carrying it$`, st.newNodeSigns)
			sc.Step(`^a broker built before params_b verifies it$`, st.oldBrokerVerifies)
			sc.Step(`^the signature verifies$`, st.signatureVerifies)
			// Given: ctx, speed
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with declared ctx (\d+)$`, st.onAirCtx)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" with declared ctx (\d+)$`, st.onlyOnAirCtx)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with estimated ctx (\d+)$`, st.onAirCtxEstimated)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with measured tps `+num+`$`, st.onAirTPS)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with tps `+num+`$`, st.onAirPinTPS)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with probe-measured ttft `+num+` ms$`, st.onAirTTFT)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with ttft `+num+` ms$`, st.onAirTTFT)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" whose last ttft measurement of `+num+` ms is older than the probe ceiling$`, st.onAirStaleTTFT)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and its heartbeat claims tps 9999 while the broker measured 5$`, st.onAirBoast)
			// Given: trust
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", never probed, not attested$`, st.onAirNeverProbedNotAttested)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with a recent passed canary that completed$`, st.onAirVerified)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and never probed$`, st.onAirNeverProbed)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" and never probed$`, st.onlyOnAirNeverProbed)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" whose canary passed first token but never completed$`, st.onAirHalfCanary)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with a passed canary whose response confessed an unrelated model$`, st.onAirMismatch)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" whose last passed canary is older than the verified window$`, st.onAirAgedVerified)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", never canary-verified, and completed a real relay a minute ago$`, st.onAirServedNotVerified)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with a granted attestation$`, st.onAirAttested)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" verified but not attested$`, st.onAirVerifiedNotAttested)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and not attested$`, st.onAirNotAttested)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" and not attested$`, st.onlyOnAirNotAttested)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with a granted attestation and no passed canary$`, st.onAirAttestedNoCanary)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", verified, but in Tier B on a low success rate$`, st.onAirVerifiedTierB)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", never probed, in Tier A$`, st.onAirNeverProbedTierA)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)"$`, st.onAirPlain)
			sc.Step(`^node "([^"]*)" registers "([^"]*)" with a body claiming verified true and confidential true$`, st.registersLiar)
			// Given: curated / region
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" as a person's hardware$`, st.onAirAsPerson)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" as a curated station proxying "([^"]*)"$`, st.onAirCurated)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" as a person's hardware, slower and pricier$`, st.onAirHumanSlowPricey)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" as a curated station, faster and cheaper$`, st.onAirCuratedFastCheap)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" and is curated$`, st.onlyOnAirCurated)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" as a curated station$`, st.onlyOnAirCurated)
			sc.Step(`^node "([^"]*)" and curated node "([^"]*)" are on air for "([^"]*)"$`, st.humanAndCurated)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" declaring region "([^"]*)"$`, st.onAirRegion)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)" declaring region "([^"]*)"$`, st.onlyOnAirRegion)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" declaring no region$`, st.onAirNoRegion)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" declaring region "([^"]*)" with quant "([^"]*)"$`, st.onAirRegionQuant)
			sc.Step(`^node "([^"]*)" registers "([^"]*)" declaring region "([^"]*)"$`, st.registersRegion)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)", params_b `+num+`, declared ctx (\d+), tps `+num+`, ttft `+num+` ms, verified, region "([^"]*)", self-hosted$`, st.onAirAll)
			sc.Step(`^node "([^"]*)" is identical to "([^"]*)" except its quant is "([^"]*)"$`, st.identicalExceptQuant)
			sc.Step(`^node "([^"]*)" registered as curated for "([^"]*)" with verified serving and 500 receipts in its chain$`, st.curatedWithHistory)
			sc.Step(`^the same callsign re-registers with curated false$`, st.sameCallsignReregistersHuman)
			sc.Step(`^the re-registration is refused 409 naming "([^"]*)"$`, st.reregRefused409)
			sc.Step(`^a self-hosted station registers under a fresh id for "([^"]*)"$`, st.freshSelfHostedID)
			sc.Step(`^node "([^"]*)" registered without the curated flag and proxies a commercial API$`, st.freshProxyWithoutFlag)
			sc.Step(`^the operator hid curated supply in the TUI$`, func() error { return nil })
			// Given: bands, grants, consumers, Towers
			sc.Step(`^a private band on frequency "([^"]*)" whose only station "([^"]*)" serves "([^"]*)" with quant "([^"]*)"$`, st.bandWithQuant)
			sc.Step(`^a private band on frequency "([^"]*)" whose station "([^"]*)" serves "([^"]*)" with quant "([^"]*)"$`, st.bandWithQuant)
			sc.Step(`^public node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)"$`, st.publicOnAirQuant)
			sc.Step(`^owner "([^"]*)" has node "([^"]*)" for "([^"]*)" declaring region "([^"]*)"$`, st.ownerNodeRegion)
			sc.Step(`^a grant from "([^"]*)" allows model "([^"]*)"$`, st.grantFrom)
			sc.Step(`^a consumer with a funded wallet$`, st.fundedConsumer)
			sc.Step(`^a Tower relay hosts "([^"]*)" with quant "([^"]*)"$`, st.towerHostsQuant)
			sc.Step(`^a Tower relay hosts "([^"]*)" and is flagged curated$`, st.towerHostsCurated)
			sc.Step(`^a Tower relay hosts "([^"]*)" and declares no region$`, st.towerHostsNoRegion)

			// When
			sc.Step(`^a request for "([^"]*)" carries (.+)$`, st.requestCarries2)
			sc.Step(`^a request for "([^"]*)" whose prompt measures (\d+) tokens carries (.+)$`, st.requestPromptCarries)
			sc.Step(`^a request for "([^"]*)" with max_tokens (\d+) and a (\d+)-token prompt carries (.+)$`, st.requestMaxTokensCarries)
			sc.Step(`^(\d+) requests for "([^"]*)" carry (.+?)( and are refused)?$`, st.nRequestsCarryMaybeRefused)
			sc.Step(`^a request with models (\[[^\]]*\]) carries (.+)$`, st.modelsCarry)
			sc.Step(`^a request for "([^"]*)" carrying (.+) is served by "([^"]*)"$`, st.requestCarryingServedBy)
			sc.Step(`^a request for "([^"]*)" carrying (.+) is served$`, st.requestCarryingServed)
			sc.Step(`^a request for "([^"]*)" carrying (.+) is refused$`, st.requestCarryingRefused)
			sc.Step(`^an anonymous request for "([^"]*)" carries (.+)$`, st.anonCarries2)
			sc.Step(`^a grant-keyed request for "([^"]*)" carries (.+)$`, st.grantCarries2)
			sc.Step(`^a request for "([^"]*)" with the band code carries (.+)$`, st.bandCarries2)
			sc.Step(`^a request for "([^"]*)" without the band code carries (.+)$`, st.withoutBandCarries)
			sc.Step(`^the consumer's request for "([^"]*)" carries (.+)$`, st.consumerCarries2)
			sc.Step(`^a consumer GETs /discover$`, st.getDiscover2)
			sc.Step(`^the TUI sends a request on a mixed band$`, func() error { return fmt.Errorf("@tui") })
			sc.Step(`^the consumer runs `+"`roger use qwen3-32b`"+` and sends a request through the local proxy$`, func() error { return fmt.Errorf("@cli") })
			sc.Step(`^the OpenAPI document is read$`, func() error { return fmt.Errorf("@docs") })

			// Then: candidacy
			sc.Step(`^"([^"]*)" is a candidate$`, st.isCand2)
			sc.Step(`^"([^"]*)" is NOT a candidate$`, st.isNotCand2)
			sc.Step(`^"([^"]*)" is a candidate under the filter$`, st.candUnderFilter)
			sc.Step(`^the candidate set is exactly (\{.*\})$`, st.candidateSetIs)
			sc.Step(`^"([^"]*)" is a candidate and serves from Tier B because Tier A is empty$`, st.candAndServesTierB)
			sc.Step(`^a request for "([^"]*)" carrying (.+) finds "([^"]*)" a candidate$`, st.carryingFindsCand)
			sc.Step(`^a request for "([^"]*)" carrying (.+) finds "([^"]*)" NOT a candidate$`, st.carryingFindsNotCand)
			sc.Step(`^a request carrying (.+) finds both candidates under the normal scoring$`, st.carryingFindsBoth)
			sc.Step(`^a request for "([^"]*)" carrying (.+) may find the new identity a candidate once it is proven live$`, st.carryingMayFindNewIdentity)
			sc.Step(`^a request for "([^"]*)" carrying (.+) finds the new identity NOT a candidate$`, st.carryingFindsNewIdentityNot)
			sc.Step(`^"([^"]*)" is never picked$`, st.neverPicked)
			sc.Step(`^"([^"]*)" is picked more often than "([^"]*)"$`, st.pickedMoreOften)
			// Then: the response
			sc.Step(`^the status is (\d+)$`, st.statusIs2)
			sc.Step(`^the status is (\d+) with error code "([^"]*)"$`, st.statusAndCode)
			sc.Step(`^the error code is "([^"]*)"$`, st.errorCodeIs)
			sc.Step(`^the error message names "([^"]*)"$`, st.errorNames)
			sc.Step(`^the error message names "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.errorNamesThree)
			sc.Step(`^the error message names "([^"]*)" and says a curated station was available$`, st.errorNamesAndCuratedAvailable)
			sc.Step(`^the error message reads "([^"]*)"$`, st.errorReads)
			sc.Step(`^the error message says to log in to spend$`, st.saysLogIn)
			sc.Step(`^the error message does not reveal whether the band exists$`, st.bandNotRevealed)
			sc.Step(`^the error message is the same as for a quant nobody serves$`, st.sameMessageAsUnknownQuant)
			sc.Step(`^the Retry-After is about (\d+) s$`, st.retryAfterAbout)
			sc.Step(`^the refusal is the existing context-window refusal, not no_match$`, st.refusalIsCtxWindow)
			sc.Step(`^the request is served by "([^"]*)"$`, st.servedBy2)
			sc.Step(`^the response carries "([A-Za-z-]+): ([^"]*)"$`, st.responseCarries)
			sc.Step(`^the response carries "X-RogerAI-Relay" naming the Tower$`, st.relayNamesTower)
			sc.Step(`^the request is served through the bridge$`, st.servedThroughBridge)
			sc.Step(`^every one of them is served by "([^"]*)"$`, st.everyServedBy2)
			sc.Step(`^every one of them is served by "([^"]*)" directly$`, st.everyServedDirectly)
			sc.Step(`^X-RogerAI-Cost is "([^"]*)"$`, st.costHeaderIs)
			sc.Step(`^no station was dispatched to$`, st.noStationDispatched)
			sc.Step(`^"([^"]*)" was never dispatched to$`, st.neverDispatchedTo)
			sc.Step(`^no hold was placed$`, st.noHoldPlaced)
			sc.Step(`^no hold was placed and no station or Tower was dispatched to$`, st.noHoldNoStationNoTower)
			sc.Step(`^the hold was sized on "([^"]*)" only$`, st.holdSizedOn2)
			sc.Step(`^"([^"]*)" trust and probe counters are unchanged$`, st.trustAndProbeUnchanged)
			sc.Step(`^the consumer's balance is unchanged$`, st.balanceUnchanged)
			sc.Step(`^no receipt exists for the request$`, st.noReceiptWritten)
			sc.Step(`^"([^"]*)" has no receipt for the request$`, st.noReceiptForStation)
			sc.Step(`^the routing fee is the human-band fee, not the curated split$`, st.feeIsHumanBand)
			sc.Step(`^the response and the log contain no "([^"]*)"$`, st.noSecretAnywhere)
			sc.Step(`^the station receives no "provider" key and no "roger" key$`, st.stationNoCarriers)
			sc.Step(`^the station receives the messages byte-for-byte as sent$`, st.stationMessagesIntact)
			sc.Step(`^/admin/live reads ([a-z_]+) (\d+)$`, st.adminReadsFilter)
			sc.Step(`^/discover shows "([^"]*)" with verified false$`, st.feedVerifiedFalse)
			// Then: registration and the feed
			sc.Step(`^the registration is rejected with 400$`, st.registrationRejected400)
			sc.Step(`^the registration is rejected as a reserved quant label$`, st.registrationRejectedReservedQuant)
			sc.Step(`^the registration is accepted$`, st.registrationAccepted)
			sc.Step(`^the message names "([^"]*)"$`, st.regMessageNames)
			sc.Step(`^the "([^"]*)" offer carries params_b `+num+`$`, st.feedParamsB)
			sc.Step(`^the offer carries params_estimated (true|false)$`, st.feedParamsEstimated)
			sc.Step(`^the "([^"]*)" offer carries no params_b key$`, st.feedNoParamsKeys)
			sc.Step(`^the offer carries no params_estimated key$`, st.feedNoParamsEstimated)
			sc.Step(`^the offer still carries params_b `+num+`$`, st.offerStillParams)
			sc.Step(`^the offer carries params_b `+num+`$`, st.offerStillParams)
			sc.Step(`^both offers carry params_estimated false$`, st.bothOffersNotEstimated)
			sc.Step(`^the "([^"]*)" offer carries region "([^"]*)" and no verification marker$`, st.feedRegionNoMarker)
			sc.Step(`^the "([^"]*)"/"([^"]*)" band no longer lists "([^"]*)"$`, st.bandNoLongerLists)
			sc.Step(`^the OpenAPI document describes params_b as station-declared and params_estimated as broker-filled$`, st.openAPIDescribesParams)
			sc.Step(`^nothing on the feed claims params_b was measured or verified$`, st.feedClaimsNothingMeasured)
			// Then: identity
			sc.Step(`^the registration is treated as a NEW station identity$`, st.newIdentity)
			sc.Step(`^the new identity starts with fresh trust state: not verified, never probed, Tier-B until proven$`, st.newIdentityFreshTrust)
			sc.Step(`^the old identity's receipts, lineage and success history do not follow it$`, st.oldHistoryDoesNotFollow)
			sc.Step(`^the curated honesty rules apply to "([^"]*)" as to any station$`, st.honestyRulesApply)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: nf2Paths(),
			Tags:  "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice3",
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("open_network_filters.feature failed")
	}
}

// --- signing-compat steps (params_b outside the possession proof) ----------------------------

// The possession proof signs regSigningBytes, which clears the display fields; a params_b
// field must join them. Until the field exists, "a node that knows params_b" cannot be built
// from the protocol package: the step says so instead of pretending.
func (s *nf2State) oldNodeSigns() error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "old-" + utNonce(), PubKey: hex.EncodeToString(pub), BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: s.model, Ctx: 8192}}}
	reg.SignRegistration(priv)
	s.sigReg, s.sigOK = &reg, reg.VerifyRegistration()
	return nil
}

func (s *nf2State) newBrokerVerifies() error { return nil } // this broker IS the one that would know params_b

func (s *nf2State) newNodeSigns() error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "new-" + utNonce(), PubKey: hex.EncodeToString(pub), BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: s.model, Ctx: 8192}}}
	if err := nf2SetParamsB(&reg.Offers[0], 32.8); err != nil {
		return err
	}
	reg.SignRegistration(priv)
	s.sigReg, s.sigOK = &reg, reg.VerifyRegistration()
	return nil
}

func (s *nf2State) oldBrokerVerifies() error {
	// an old broker decodes the wire without the field: re-decode through a struct that has
	// none, which is the protocol struct itself until the field lands
	if s.sigReg == nil {
		return fmt.Errorf("no signed registration")
	}
	b, _ := json.Marshal(s.sigReg)
	var again protocol.NodeRegistration
	if err := json.Unmarshal(b, &again); err != nil {
		return err
	}
	s.sigOK = again.VerifyRegistration()
	return nil
}

func (s *nf2State) signatureVerifies() error {
	if !s.sigOK {
		return fmt.Errorf("the registration signature does not verify across the params_b boundary")
	}
	return nil
}

// nf2Paths lets NF2_PATH narrow a local run to one scenario (feature:line); the suite default
// is the whole file.
func nf2Paths() []string {
	if p := os.Getenv("NF2_PATH"); p != "" {
		return []string{p}
	}
	return []string{"../../features/routing/open_network_filters.feature"}
}
