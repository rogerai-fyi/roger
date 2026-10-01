package main

// routing_slice0_bdd_test.go makes the @slice0-tagged scenarios of
// features/routing/node_preference.feature and features/routing/request_shape.feature
// EXECUTABLE against the REAL broker: the provider.order / provider.ignore /
// provider.allow_fallbacks code paths, the 400 routing errors, null-means-absent, and the
// carrier strip. These are the approved scenarios whose bodies use only the keys slice 0
// honours (the rest of both files is slice 1 and stays untagged). It rides the pins runner's
// fixtures (rpState over foState: real relayBroker + store + miniredis, real stations with
// scripted upstreams behind real tunnels) so every step is a real relay through relay() -
// no mocks. Per-attempt order is observed at the upstreams themselves (hitScript records
// which station's upstream was hit, in order).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type s0State struct {
	*rpState

	hitsMu sync.Mutex
	hits   []string // station names in upstream-hit order, since the last snapshot

	// request shaping for this runner (names resolve to ids at relay time)
	hdrPin, hdrExclude string
	bodyFrag           string // raw JSON members appended to the body ("" = none)
	bodyConfidential   bool
	bodyFreq           string
	anon               bool

	// pre-relay snapshots (taken once, by the first relay of the scenario)
	snapped     bool
	holdsBefore int
	reqBefore   map[string]int

	// request_shape: what each station's upstream received
	bodiesMu sync.Mutex
	bodies   map[string][]byte // station name -> last upstream body

	// private bands by scenario name
	bands map[string]string // "B" -> code
}

// --- fixtures ----------------------------------------------------------------------

func (s *s0State) resetSlice0() error {
	if err := s.resetPins(); err != nil {
		return err
	}
	s.hits, s.hdrPin, s.hdrExclude, s.bodyFrag, s.bodyFreq = nil, "", "", "", ""
	s.bodyConfidential, s.anon = false, false
	s.holdsBefore, s.reqBefore, s.snapped = 0, nil, false
	s.bodies = map[string][]byte{}
	s.bands = map[string]string{}
	_ = os.Unsetenv("ROGERAI_RELAY_ATTEMPTS")
	return nil
}

// stand brings a Tier-A station up for the scenario model at in=out=price and installs the
// hit-recording real-completion script.
func (s *s0State) stand(name string, priceIn, priceOut float64) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	st := s.station(name, priceIn, priceOut)
	s.hitScript(name, func(_ int, w http.ResponseWriter, r *http.Request) { utRealCompletion(w) })
	return st
}

// hitScript wraps an upstream script so each upstream hit is recorded in order and the body
// the station received is kept (request_shape's "the station received ..." steps).
func (s *s0State) hitScript(name string, inner func(n int, w http.ResponseWriter, r *http.Request)) {
	st := s.st(name)
	st.set(func(n int, w http.ResponseWriter, r *http.Request) {
		s.hitsMu.Lock()
		s.hits = append(s.hits, name)
		s.hitsMu.Unlock()
		if r != nil && r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			s.bodiesMu.Lock()
			s.bodies[name] = b
			s.bodiesMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		inner(n, w, r)
	})
}

func (s *s0State) script429For(name, retryAfter string) {
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(429)
		_, _ = w.Write([]byte(utDefaultBody(429)))
	})
}

func (s *s0State) scriptServes(name string) {
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) { utRealCompletion(w) })
}

func (s *s0State) snapshotNow() error {
	h, _, _, _, err := s.holdRows()
	if err != nil {
		return err
	}
	s.holdsBefore = h
	s.reqBefore = map[string]int{}
	for n, st := range s.stations {
		s.reqBefore[n] = st.upstreamCount()
	}
	s.hitsMu.Lock()
	s.hits = nil
	s.hitsMu.Unlock()
	return nil
}

func (s *s0State) hitList() []string {
	s.hitsMu.Lock()
	defer s.hitsMu.Unlock()
	return append([]string(nil), s.hits...)
}

// --- Background --------------------------------------------------------------------

func (s *s0State) threeStations() error {
	s.model = "m"
	for _, n := range []string{"s1", "s2", "s3"} {
		s.stand(n, 1, 1)
	}
	return nil
}

func (s *s0State) threePrices() error {
	for n, p := range map[string]float64{"s1": 1, "s2": 2, "s3": 3} {
		s.setPrice(n, p, p)
	}
	return nil
}

func (s *s0State) setPrice(name string, in, out float64) {
	st := s.st(name)
	st.mu.Lock()
	st.priceIn, st.priceOut = in, out
	st.mu.Unlock()
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.PriceIn, o.PriceOut = in, out })
}

func (s *s0State) fundedConsumer() error { return s.ensureFunded() }
func (s *s0State) feeRate10() error      { s.b.feeRate = 0.10; return nil }
func (s *s0State) ceilings100and50() error {
	_ = os.Unsetenv("ROGERAI_MAX_PRICE_OUT")
	_ = os.Unsetenv("ROGERAI_MAX_PRICE_IN")
	if maxPriceOutCeiling() != 100 {
		return fmt.Errorf("out ceiling = %v, want 100", maxPriceOutCeiling())
	}
	return nil
}

func (s *s0State) nodeOnAirPriced(name, model, in, out string) error {
	s.model = model
	pin, _ := strconv.ParseFloat(in, 64)
	pout, _ := strconv.ParseFloat(out, 64)
	s.stand(name, pin, pout)
	return nil
}

func (s *s0State) loggedInConsumer(_ string, bal string) error {
	amt, _ := strconv.ParseFloat(bal, 64)
	return s.fund(amt)
}

// --- Given: station states ----------------------------------------------------------

func (s *s0State) is429sAndServes(a, b string) error {
	s.script429For(a, "")
	s.scriptServes(b)
	return nil
}

func (s *s0State) is429sRetryAfterAndServes(a, ra, b string) error {
	s.script429For(a, ra)
	s.scriptServes(b)
	return nil
}

func (s *s0State) threeWay429(a, b, c string) error { // "a" 429s, "b" 429s, and "c" serves
	s.script429For(a, "")
	s.script429For(b, "")
	s.scriptServes(c)
	return nil
}

func (s *s0State) a429sBandCServe(a, b, c string) error {
	s.script429For(a, "")
	s.scriptServes(b)
	s.scriptServes(c)
	return nil
}

func (s *s0State) a429sB429sRAcServes(a, b, ra, c string) error {
	s.script429For(a, "")
	s.script429For(b, ra)
	s.scriptServes(c)
	return nil
}

func (s *s0State) twiceThenServes(a, b string) error {
	s.hitScript(a, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
			return
		}
		utRealCompletion(w)
	})
	s.scriptServes(b)
	return nil
}

func (s *s0State) coolingAndServes(a, secs, b string) error {
	n, _ := strconv.Atoi(secs)
	s.scriptServes(b)
	return s.cool(a, n)
}

func (s *s0State) bothCooling(a, sa, b, sb string) error {
	na, _ := strconv.Atoi(sa)
	nb, _ := strconv.Atoi(sb)
	if err := s.cool(a, na); err != nil {
		return err
	}
	return s.cool(b, nb)
}

func (s *s0State) coolingAndBanned(a, secs, b string) error {
	n, _ := strconv.Atoi(secs)
	if err := s.cool(a, n); err != nil {
		return err
	}
	return s.ban(b)
}

func (s *s0State) bannedAndCooling(a, b string) error {
	if err := s.ban(a); err != nil {
		return err
	}
	return s.cool(b, 15)
}

func (s *s0State) ban(name string) error {
	s.b.mu.Lock()
	s.b.banned[s.st(name).id] = true
	s.b.mu.Unlock()
	return nil
}

func (s *s0State) stale(name string) error {
	s.b.mu.Lock()
	s.b.lastSeen[s.st(name).id] = time.Now().Add(-2 * nodeTTL)
	s.b.mu.Unlock()
	return nil
}

func (s *s0State) pricesOut(name, out string) error {
	p, _ := strconv.ParseFloat(out, 64)
	s.setPrice(name, p/10, p)
	return nil
}

func (s *s0State) onlyAttested(a, b string) error {
	s.b.mu.Lock()
	for _, n := range []string{a, b} {
		s.b.confidential[s.st(n).id] = true
	}
	s.b.mu.Unlock()
	return nil
}

func (s *s0State) freeStations(a, b string) error {
	for _, n := range []string{a, b} {
		s.stand(n, 0, 0)
	}
	// An anonymous wallet is seeded exactly as production seeds first use (free credits),
	// so the ~$0 free-offer hold can land; it is still NOT logged in and cannot spend
	// (the same fixture the approved upstream_failover anonymous scenario uses).
	s.b.seedFunds = 0.5
	return nil
}

func (s *s0State) balanceCovers(a, b string) error {
	// Top the wallet up to exactly what covers a's max cost but not b's. The wallet starts
	// funded ($10, the Background) so we drain and re-add precisely.
	body := s.foState.body(false)
	costA := holdCostFor(pricingPlan{}, s.offerOf(a), body, time.Now())
	costB := holdCostFor(pricingPlan{}, s.offerOf(b), body, time.Now())
	if costB <= costA {
		return fmt.Errorf("%s (%v) is not pricier than %s (%v)", b, costB, a, costA)
	}
	bal, err := s.balance()
	if err != nil {
		return err
	}
	target := (costA + costB) / 2
	if _, err := s.db.AddCredits(s.wallet, target-bal); err != nil {
		return err
	}
	s.funded = true
	return nil
}

func (s *s0State) offerOf(name string) protocol.ModelOffer {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.nodes[s.st(name).id].Offers[0]
}

func (s *s0State) fiveAll429() error {
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		s.stand(n, 1, 1)
		s.script429For(n, "")
	}
	return nil
}

func (s *s0State) idleAndAtCapacity(_, _, c string) error {
	s.b.metricsMu.Lock()
	s.b.inflight[s.st(c).id] = 64
	s.b.metricsMu.Unlock()
	return nil
}

func (s *s0State) privateBand(band, names string) error {
	var stations []string
	for _, q := range strings.Split(names, " and ") {
		stations = append(stations, strings.Trim(strings.TrimSpace(q), `"`))
	}
	first := s.stand(stations[0], 1, 1)
	code := "147.520 MHz · " + strings.ToUpper(band) + "BCD-" + strings.ToUpper(s.nonce[:4])
	s.bands[band] = code
	for _, n := range stations {
		s.stand(n, 1, 1)
	}
	s.b.mu.Lock()
	for _, n := range stations {
		s.b.private[s.st(n).id] = true
	}
	s.b.mu.Unlock()
	rec := store.Band{ID: "band_" + band + "_" + s.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: first.acct, NodeID: first.id, CreatedAt: time.Now().Unix()}
	if len(stations) > 1 {
		// A multi-station band: every member shares the band owner's account.
		for _, n := range stations[1:] {
			if err := s.db.BindNode(s.st(n).id, first.acct); err != nil {
				return err
			}
		}
	}
	return s.db.CreateBand(rec)
}

func (s *s0State) privateStationNoCode(name, _ string) error {
	s.stand(name, 1, 1)
	s.b.mu.Lock()
	s.b.private[s.st(name).id] = true
	s.b.mu.Unlock()
	return nil
}

// stationState applies one row of the "every kind of ineligible listed station" tables.
func (s *s0State) stationState(name, state string) error {
	switch {
	case state == "banned":
		return s.ban(name)
	case state == "owned by a banned operator":
		s.b.metricsMu.Lock()
		if s.b.bannedOwners == nil {
			s.b.bannedOwners = map[string]bool{}
		}
		s.b.bannedOwners[s.st(name).acct] = true
		s.b.metricsMu.Unlock()
		return nil
	case strings.HasPrefix(state, "stale"):
		return s.stale(name)
	case state == "past the dead-probe streak":
		s.b.mu.Lock()
		tq := s.b.trust[s.st(name).id]
		tq.probeFails = probeDeadStreak
		s.b.trust[s.st(name).id] = tq
		s.b.mu.Unlock()
		return nil
	case state == "cooling":
		return s.cool(name, 15)
	case strings.HasPrefix(state, "private (band-only)"):
		s.b.mu.Lock()
		s.b.private[s.st(name).id] = true
		s.b.mu.Unlock()
		return nil
	case strings.HasPrefix(state, "not offering"):
		s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Model = "other-model" })
		return nil
	case strings.HasPrefix(state, "priced out 12.00") || state == "priced over the default out-cap":
		return s.pricesOut(name, "12")
	case strings.HasPrefix(state, "declaring ctx 4096"):
		s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Ctx = 4096 })
		s.tokens = 8000
		return nil
	case state == "below the request's min_tps":
		s.setTPS(s.st(name).id, 5)
		s.minTPS = "20"
		return nil
	case state == "lacking a required capability":
		// The implicit rule: a tools body requires the verified tools bit, which only the
		// OTHER listed station carries.
		s.bodyShape = "tools"
		for n := range s.stations {
			if n != name {
				s.b.recordToolProbe(s.st(n).id, s.model, true, false, true)
			}
		}
		return nil
	case state == "not TEE-attested on a confidential request":
		s.bodyConfidential = true
		s.b.mu.Lock()
		for n := range s.stations {
			if n != name {
				s.b.confidential[s.st(n).id] = true
			}
		}
		s.b.mu.Unlock()
		return nil
	}
	return fmt.Errorf("unknown station state %q", state)
}

// --- When: relays --------------------------------------------------------------------

// ids maps a JSON array of scenario station names to broker ids, leaving unknown names
// (foreign ids, wrong case) as they are.
func (s *s0State) ids(list string) []string {
	var names []string
	if err := json.Unmarshal([]byte(list), &names); err != nil {
		s.t.Fatalf("bad station list %q: %v", list, err)
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = s.idOf(n)
	}
	return out
}

// listValue renders a scenario list value as the JSON the body carries: a well-formed array
// of names is mapped to ids; anything else (a malformed table value) goes verbatim.
func (s *s0State) listValue(value string) string {
	if value == "33 distinct station ids" {
		return thirtyThreeIDs()
	}
	var names []string
	if err := json.Unmarshal([]byte(value), &names); err == nil {
		b, _ := json.Marshal(s.ids(value))
		return string(b)
	}
	return value
}

func (s *s0State) providerFrag(order []string, ignore []string, fallbacks *bool) string {
	m := map[string]any{}
	if order != nil {
		m["order"] = order
	}
	if ignore != nil {
		m["ignore"] = ignore
	}
	if fallbacks != nil {
		m["allow_fallbacks"] = *fallbacks
	}
	b, _ := json.Marshal(m)
	return `"provider": ` + string(b)
}

// fire runs one real relay with the runner's shaping. The body is the harness prompt plus
// bodyFrag's raw members (so malformed fragments reach the broker verbatim).
func (s *s0State) fire(stream bool) error {
	if err := s.snapshotNowOnce(); err != nil {
		return err
	}
	priv := s.consumerPriv
	if s.anon {
		priv = s.anonPriv
	} else if err := s.ensureFunded(); err != nil {
		return err
	}
	base := s.requestBody(stream)
	body := base
	frag := s.bodyFrag
	if s.bodyConfidential {
		frag = joinFrag(frag, `"roger": {"confidential": true}`)
	}
	if s.bodyFreq != "" {
		frag = joinFrag(frag, `"roger": {"freq": `+strconv.Quote(s.bodyFreq)+`}`)
	}
	if frag != "" {
		body = append(bytes.TrimSuffix(bytes.TrimSpace(base), []byte("}")), []byte(","+frag+"}")...)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	signReq(r, priv, body)
	if s.hdrPin != "" {
		r.Header.Set("X-Roger-Node", s.hdrPin)
	}
	if s.hdrExclude != "" {
		r.Header.Set("X-Roger-Exclude-Nodes", s.hdrExclude)
	}
	if s.minTPS != "" {
		r.Header.Set("X-Roger-Min-TPS", s.minTPS)
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	res := rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()}
	s.batch = append(s.batch, res)
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = w.Code, w.Body.Bytes(), w.Header(), w
	return nil
}

// joinFrag merges two `"roger": {...}` fragments into one object (a body may carry only one
// roger key); other fragments are simply concatenated.
func joinFrag(a, b string) string {
	if a == "" {
		return b
	}
	if strings.HasPrefix(a, `"roger": {`) && strings.HasPrefix(b, `"roger": {`) {
		inner := strings.TrimSuffix(strings.TrimPrefix(b, `"roger": {`), "}")
		return strings.TrimSuffix(a, "}") + ", " + inner + "}"
	}
	return a + ", " + b
}

func (s *s0State) snapshotNowOnce() error {
	if s.snapped {
		return nil
	}
	s.snapped = true
	return s.snapshotNow()
}

func (s *s0State) relayN(n string, how func() error) error {
	count, _ := strconv.Atoi(n)
	s.batch = nil
	for i := 0; i < count; i++ {
		if err := how(); err != nil {
			return err
		}
	}
	return nil
}

func (s *s0State) relaysIgnore(n, list string) error {
	s.bodyFrag = `"provider": {"ignore": ` + s.listValue(list) + `}`
	return s.relayN(n, func() error { return s.fire(false) })
}

func (s *s0State) relayIgnore(list string) error { return s.relaysIgnore("1", list) }

func (s *s0State) relayFallbacksRaw(value string) error {
	s.bodyFrag = `"provider": {"allow_fallbacks": ` + value + `}`
	return s.fire(false)
}

func thirtyThreeIDs() string {
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = fmt.Sprintf("x%d", i)
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func (s *s0State) relayExcludeAndIgnore(hdr, list string) error {
	s.hdrExclude = s.idOf(hdr)
	s.bodyFrag = s.providerFrag(nil, s.ids(list), nil)
	s.batch = nil
	return s.fire(false)
}

func (s *s0State) relayIgnoreLandsOn(list, name string) error {
	s.landOn(name)
	return s.relayIgnore(list)
}

func (s *s0State) relayFreqIgnore(band, list string) error {
	s.bodyFreq = s.bands[band]
	return s.relayIgnore(list)
}

func (s *s0State) relayFreqOrder(band, list string) error {
	s.bodyFreq = s.bands[band]
	return s.relayOrder(list)
}

func (s *s0State) relaysOrder(n, list string) error {
	s.bodyFrag = `"provider": {"order": ` + s.listValue(list) + `}`
	return s.relayN(n, func() error { return s.fire(false) })
}

func (s *s0State) relayOrder(list string) error { return s.relaysOrder("1", list) }

func (s *s0State) relayOrderNoMaxPrice(list string) error { return s.relayOrder(list) }

func (s *s0State) relayOrderIgnore(order, ignore string) error {
	s.bodyFrag = s.providerFrag(s.ids(order), s.ids(ignore), nil)
	return s.fire(false)
}

func (s *s0State) relayPinAndOrder(pin, list string) error {
	s.hdrPin = s.idOf(pin)
	return s.relayOrder(list)
}

func (s *s0State) relayConfidentialOrder(list string) error {
	s.bodyConfidential = true
	return s.relayOrder(list)
}

func (s *s0State) anonRelayOrder(list string) error {
	s.anon = true
	return s.relayOrder(list)
}

func (s *s0State) relayOrderFallbacks(list, v string) error {
	fb := v == "true"
	s.bodyFrag = s.providerFrag(s.ids(list), nil, &fb)
	return s.fire(false)
}

func (s *s0State) relayOrderNoFallbackAttempts(list, n string) error {
	if err := os.Setenv("ROGERAI_RELAY_ATTEMPTS", n); err != nil {
		return err
	}
	return s.relayOrderFallbacks(list, "false")
}

func (s *s0State) relayNoFallbackLandsOn(name string) error {
	s.landOn(name)
	return s.relayNoFallback()
}

func (s *s0State) relayNoFallback() error {
	fb := false
	s.bodyFrag = s.providerFrag(nil, nil, &fb)
	return s.fire(false)
}

func (s *s0State) relayPin(pin string) error {
	s.hdrPin = s.idOf(pin)
	return s.fire(false)
}

func (s *s0State) relayPinFallbacksTrue(pin string) error {
	s.hdrPin = s.idOf(pin)
	fb := true
	s.bodyFrag = s.providerFrag(nil, nil, &fb)
	return s.fire(false)
}

func (s *s0State) relayPinOrderNoFallbacks(pin, list string) error {
	s.hdrPin = s.idOf(pin)
	return s.relayOrderFallbacks(list, "false")
}

// request_shape: "u-1" posts ... with body `fragment`
func (s *s0State) mapNames(frag string) string {
	for n, st := range s.stations {
		frag = strings.ReplaceAll(frag, strconv.Quote(n), strconv.Quote(st.id))
	}
	return frag
}

func (s *s0State) postsBody(_, model, frag string) error {
	s.model = model
	s.bodyFrag = s.mapNames(frag)
	return s.fire(false)
}

func (s *s0State) postsHeaderExcludeBody(_, model, hdr, frag string) error {
	s.hdrExclude = s.idOf(hdr)
	return s.postsBody("", model, frag)
}

func (s *s0State) postsStreamingPinBody(_, model, pin, frag string) error {
	s.model = model
	s.hdrPin = s.idOf(pin)
	s.bodyFrag = s.mapNames(frag)
	return s.fire(true)
}

func (s *s0State) postsNonObject(_ string) error {
	if err := s.snapshotNowOnce(); err != nil {
		return err
	}
	body := []byte(`[1, 2, 3]`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	signReq(r, s.consumerPriv, body)
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	return nil
}

// --- Then ----------------------------------------------------------------------------

func (s *s0State) provider() string { return s.lastHdr.Get("X-RogerAI-Provider") }

func (s *s0State) nameOfID(id string) string {
	for n, st := range s.stations {
		if st.id == id {
			return n
		}
	}
	return id
}

func (s *s0State) pickIs(name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	if got := s.provider(); got != s.idOf(name) {
		return fmt.Errorf("pick is %q, want %q", s.nameOfID(got), name)
	}
	return nil
}

func (s *s0State) pickIsOneOf(names ...string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	got := s.nameOfID(s.provider())
	for _, n := range names {
		if got == n {
			return nil
		}
	}
	return fmt.Errorf("pick is %q, want one of %v", got, names)
}

func (s *s0State) pickIsOneOf2(a, b string) error    { return s.pickIsOneOf(a, b) }
func (s *s0State) pickIsOneOf3(a, b, c string) error { return s.pickIsOneOf(a, b, c) }

func (s *s0State) everyPick(names ...string) error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay fired")
	}
	for i, r := range s.batch {
		if r.code != 200 {
			return fmt.Errorf("relay %d: status %d (%s)", i+1, r.code, r.body)
		}
		got := s.nameOfID(r.hdr.Get("X-RogerAI-Provider"))
		ok := false
		for _, n := range names {
			ok = ok || got == n
		}
		if !ok {
			return fmt.Errorf("relay %d picked %q, want one of %v", i+1, got, names)
		}
	}
	return nil
}

func (s *s0State) everyPickIs(a string) error      { return s.everyPick(a) }
func (s *s0State) everyPickIsOr(a, b string) error { return s.everyPick(a, b) }
func (s *s0State) pickedAtLeastOnce(name string) error {
	for _, r := range s.batch {
		if r.hdr.Get("X-RogerAI-Provider") == s.idOf(name) {
			return nil
		}
	}
	return fmt.Errorf("%q was never picked in %d relays", name, len(s.batch))
}

// distributionMatches: a no-op ignore / unknown order must leave the POOL unchanged. Picks
// are a seeded power-of-two-choices sample, so a 20-relay histogram is noise; the pool is
// observed directly - every station's candidacy under the request's deny set equals its
// candidacy with no routing object - and every relay in the batch served.
func (s *s0State) distributionMatches(_ string) error {
	for i, r := range s.batch {
		if r.code != 200 {
			return fmt.Errorf("relay %d: status %d (%s)", i+1, r.code, r.body)
		}
	}
	var frag struct {
		Ignore []string `json:"ignore"`
		Order  []string `json:"order"`
	}
	_ = json.Unmarshal([]byte(strings.TrimPrefix(s.bodyFrag, `"provider": `)), &frag)
	deny := map[string]bool{}
	for _, id := range frag.Ignore {
		deny[id] = true
	}
	for name, st := range s.stations {
		with, without := s.candidateUnder(st.id, deny), s.candidateUnder(st.id, nil)
		if with != without {
			return fmt.Errorf("%q candidacy with the routing object = %v, without = %v", name, with, without)
		}
		if !without {
			return fmt.Errorf("%q is not a candidate even without a routing object", name)
		}
	}
	return nil
}

// candidateUnder judges one station through the real pickFor with the registry narrowed to
// it (the pins runner's isCandidateByPick, with an explicit deny set).
func (s *s0State) candidateUnder(id string, deny map[string]bool) bool {
	s.b.mu.Lock()
	saved := s.b.nodes
	s.b.nodes = map[string]protocol.NodeRegistration{id: saved[id]}
	n, _, ok := s.b.pickFor(s.model, false, 0, 0, effectiveRelayMaxOut(0), "", deny, nil, nil, s.pickReqFor(0))
	s.b.nodes = saved
	s.b.mu.Unlock()
	return ok && n.NodeID == id
}

func (s *s0State) receivedNothing(name string) error {
	if st, ok := s.stations[name]; ok {
		if got := st.upstreamCount() - s.reqBefore[name]; got != 0 {
			return fmt.Errorf("%q received %d upstream request(s), want none", name, got)
		}
	}
	for _, h := range s.hitList() {
		if h == name {
			return fmt.Errorf("%q was hit", name)
		}
	}
	return nil
}

func (s *s0State) bothReceivedNothing(a, b string) error {
	if err := s.receivedNothing(a); err != nil {
		return err
	}
	return s.receivedNothing(b)
}

func (s *s0State) noStationReceivedAnything() error {
	for n := range s.stations {
		if err := s.receivedNothing(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *s0State) responseIs(code string) error {
	want, _ := strconv.Atoi(code)
	if s.lastCode != want {
		return fmt.Errorf("status %d, want %s (%s)", s.lastCode, code, s.lastBody)
	}
	return nil
}

func (s *s0State) errBody() (code, msg string) {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(s.lastBody, &env)
	return env.Error.Code, env.Error.Message
}

func (s *s0State) responseIsWithCode(status, code string) error {
	if err := s.responseIs(status); err != nil {
		return err
	}
	if got, _ := s.errBody(); got != code {
		return fmt.Errorf("error.code %q, want %q (%s)", got, code, s.lastBody)
	}
	return nil
}

func (s *s0State) responseIsWithCodeRetryAfter(status, code, ra string) error {
	if err := s.responseIsWithCode(status, code); err != nil {
		return err
	}
	if got := s.lastHdr.Get("Retry-After"); got != ra {
		return fmt.Errorf("Retry-After %q, want %q", got, ra)
	}
	return nil
}

func (s *s0State) response400Naming(code, key string) error {
	if err := s.responseIsWithCode("400", code); err != nil {
		return err
	}
	if _, msg := s.errBody(); !strings.Contains(msg, key) {
		return fmt.Errorf("message %q does not name %q", msg, key)
	}
	return nil
}

func (s *s0State) response503Message(msg string) error {
	if err := s.responseIs("503"); err != nil {
		return err
	}
	if _, got := s.errBody(); got != msg {
		return fmt.Errorf("message %q, want %q", got, msg)
	}
	return nil
}

func (s *s0State) noErrorCode() error {
	var m map[string]any
	_ = json.Unmarshal(s.lastBody, &m)
	if e, _ := m["error"].(map[string]any); e != nil {
		if _, has := e["code"]; has {
			return fmt.Errorf("body carries error.code: %s", s.lastBody)
		}
	}
	return nil
}

func (s *s0State) errorCodeIs(code string) error {
	if got, _ := s.errBody(); got != code {
		return fmt.Errorf("error.code %q, want %q (%s)", got, code, s.lastBody)
	}
	return nil
}

func (s *s0State) errorMessageNames(key string) error {
	if _, msg := s.errBody(); !strings.Contains(msg, key) {
		return fmt.Errorf("message %q does not name %q", msg, key)
	}
	return nil
}

func (s *s0State) failoverGoesTo(name string) error {
	hits := s.hitList()
	if len(hits) < 2 {
		return fmt.Errorf("no failover happened: hits %v", hits)
	}
	if err := s.pickIs(name); err != nil {
		return err
	}
	if hits[len(hits)-1] != name {
		return fmt.Errorf("last attempt hit %q, want %q (hits %v)", hits[len(hits)-1], name, hits)
	}
	return nil
}

func (s *s0State) failoverGoesToNever(a, b string) error {
	if err := s.failoverGoesTo(a); err != nil {
		return err
	}
	return s.receivedNothing(b)
}

func (s *s0State) attemptsHit(a, b, c string) error {
	hits := s.hitList()
	want := []string{a, b, c}
	if len(hits) != 3 {
		return fmt.Errorf("attempts %v, want %v", hits, want)
	}
	for i := range want {
		if hits[i] != want[i] {
			return fmt.Errorf("attempt %d hit %q, want %q (all: %v)", i+1, hits[i], want[i], hits)
		}
	}
	return nil
}

func (s *s0State) attempt1And2(a, b string) error {
	hits := s.hitList()
	if len(hits) != 2 || hits[0] != a || hits[1] != b {
		return fmt.Errorf("attempts %v, want [%s %s]", hits, a, b)
	}
	return nil
}

func (s *s0State) attempt1And2Either(a, b, c string) error {
	hits := s.hitList()
	if len(hits) != 2 || hits[0] != a || (hits[1] != b && hits[1] != c) {
		return fmt.Errorf("attempts %v, want [%s, %s|%s]", hits, a, b, c)
	}
	return nil
}

func (s *s0State) consumerSees200Provider(name string) error {
	if err := s.responseIs("200"); err != nil {
		return err
	}
	return s.pickIs(name)
}

func (s *s0State) consumerSees(code string) error { return s.responseIs(code) }

func (s *s0State) exactlyOneAttempt() error {
	if hits := s.hitList(); len(hits) != 1 {
		return fmt.Errorf("attempts %v, want exactly one", hits)
	}
	return nil
}

func (s *s0State) exactlyOneAttemptTo(name string) error {
	if hits := s.hitList(); len(hits) != 1 || hits[0] != name {
		return fmt.Errorf("attempts %v, want exactly [%s]", hits, name)
	}
	return nil
}

func (s *s0State) exactlyNAttemptsTo(n, a, b, c string) error {
	want, _ := strconv.Atoi(n)
	hits := s.hitList()
	if len(hits) != want {
		return fmt.Errorf("attempts %v, want %d", hits, want)
	}
	return s.attemptsHit(a, b, c)
}

func (s *s0State) receivedExactlyOne(name string) error {
	count := 0
	for _, h := range s.hitList() {
		if h == name {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("%q received %d attempt(s), want exactly one (hits %v)", name, count, s.hitList())
	}
	return nil
}

func (s *s0State) responseWithRetryAfter(code, ra string) error {
	if err := s.responseIs(code); err != nil {
		return err
	}
	if got := s.lastHdr.Get("Retry-After"); got != ra {
		return fmt.Errorf("Retry-After %q, want %q", got, ra)
	}
	return nil
}

func (s *s0State) responseHasRetryAfter(code string) error {
	if err := s.responseIs(code); err != nil {
		return err
	}
	if s.lastHdr.Get("Retry-After") == "" {
		return fmt.Errorf("no Retry-After header")
	}
	return nil
}

func (s *s0State) noRetryAfter() error {
	if ra := s.lastHdr.Get("Retry-After"); ra != "" {
		return fmt.Errorf("Retry-After %q present, want none", ra)
	}
	return nil
}

func (s *s0State) noWarningAbout(name string) error {
	if bytes.Contains(s.lastBody, []byte("warning")) || bytes.Contains(s.lastBody, []byte(s.idOf(name))) {
		return fmt.Errorf("response mentions a warning or %q: %s", name, s.lastBody)
	}
	for k, v := range s.lastHdr {
		if strings.Contains(strings.ToLower(k), "warning") {
			return fmt.Errorf("response carries header %s: %v", k, v)
		}
	}
	return nil
}

func (s *s0State) headerIgnoredWithoutError() error { return s.responseIs("200") }

// fallbacksAllowed re-fires the SAME request with the ordered station now answering 429
// and expects the broker to fail over: the body's default (true) governs, not the header pin.
func (s *s0State) fallbacksAllowed() error {
	picked := s.nameOfID(s.provider())
	s.script429For(picked, "")
	if err := s.snapshotNow(); err != nil {
		return err
	}
	if err := s.fire(false); err != nil {
		return err
	}
	if s.lastCode != 200 {
		return fmt.Errorf("with %q 429ing the relay was %d, want a failover to 200 (%s)", picked, s.lastCode, s.lastBody)
	}
	if got := s.nameOfID(s.provider()); got == picked {
		return fmt.Errorf("served by %q again, want a failover", picked)
	}
	return nil
}

// holdsPlaced counts the hold rows written since the scenario's first relay.
func (s *s0State) holdsPlaced() (int, error) {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold}, 5000)
	if err != nil {
		return 0, err
	}
	return len(rows) - s.holdsBefore, nil
}

// holdAmountOfLast is the ONE hold the scenario's relay placed (every scenario that reads
// it fires exactly one holding relay after its Givens).
func (s *s0State) holdAmountOfLast() (float64, error) {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold}, 5000)
	if err != nil {
		return 0, err
	}
	if n := len(rows) - s.holdsBefore; n != 1 {
		return 0, fmt.Errorf("%d hold row(s) since the relay, want exactly one", n)
	}
	newest := rows[0]
	for _, r := range rows[1:] {
		if r.ID > newest.ID {
			newest = r
		}
	}
	return -newest.Amount, nil
}

func (s *s0State) noHold() error {
	n, err := s.holdsPlaced()
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%d hold(s) placed, want none", n)
	}
	return nil
}

func (s *s0State) exactlyOneHold() error {
	n, err := s.holdsPlaced()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%d hold(s) placed, want exactly one", n)
	}
	return nil
}

func (s *s0State) expectedHold(name string) float64 {
	return holdCostFor(pricingPlan{}, s.offerOf(name), s.requestBody(false), time.Now())
}

func (s *s0State) holdCovers(name, price string) error {
	p, _ := strconv.ParseFloat(price, 64)
	if o := s.offerOf(name); math.Abs(o.PriceOut-p) > 1e-9 {
		return fmt.Errorf("%q is priced %v, the scenario says %v", name, o.PriceOut, p)
	}
	got, err := s.holdAmountOfLast()
	if err != nil {
		return err
	}
	if want := s.expectedHold(name); math.Abs(got-want) > 1e-9 {
		return fmt.Errorf("hold %.9f, want %.9f (%s's max cost at %v)", got, want, name, p)
	}
	return nil
}

func (s *s0State) holdCoversNot(a, pa, b, pb string) error {
	if err := s.holdCovers(a, pa); err != nil {
		return err
	}
	got, _ := s.holdAmountOfLast()
	if other := s.expectedHold(b); math.Abs(got-other) < 1e-9 {
		return fmt.Errorf("hold %.9f equals %s's max cost at %s", got, b, pb)
	}
	return nil
}

func (s *s0State) holdCoveredPicked() error {
	picked := s.nameOfID(s.provider())
	got, err := s.holdAmountOfLast()
	if err != nil {
		return err
	}
	if want := s.expectedHold(picked); math.Abs(got-want) > 1e-9 {
		return fmt.Errorf("hold %.9f, want %.9f (the picked %s's max cost)", got, want, picked)
	}
	if pricey := s.expectedHold("s3"); picked != "s3" && math.Abs(got-pricey) < 1e-9 {
		return fmt.Errorf("hold %.9f is the priciest station's max cost", got)
	}
	return nil
}

func (s *s0State) chargedAtAndReleased(name, price string) error {
	if err := s.pickIs(name); err != nil {
		return err
	}
	p, _ := strconv.ParseFloat(price, 64)
	tin, _ := strconv.Atoi(s.lastHdr.Get("X-RogerAI-Tokens-In"))
	tout, _ := strconv.Atoi(s.lastHdr.Get("X-RogerAI-Tokens-Out"))
	cost, _ := strconv.ParseFloat(s.lastHdr.Get("X-RogerAI-Cost"), 64)
	want := (float64(tin)*p + float64(tout)*p) / 1e6
	if math.Abs(cost-want) > 1e-9 {
		return fmt.Errorf("X-RogerAI-Cost %v, want %v (tokens %d/%d at %v)", cost, want, tin, tout, p)
	}
	_, releases, spends, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if releases < 1 || spends < 1 {
		return fmt.Errorf("ledger: %d release(s), %d spend(s); want the remainder released and one spend", releases, spends)
	}
	hold, _ := s.holdAmountOfLast()
	if cost >= hold {
		return fmt.Errorf("cost %v is not below the hold %v", cost, hold)
	}
	return nil
}

// request_shape observations
func (s *s0State) servedNodeIs(name string) error { return s.pickIs(name) }

func (s *s0State) stationReceivedKey(key, value string) error {
	body := s.lastUpstreamBody()
	if body == nil {
		return fmt.Errorf("no station received a body")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("station body is not a JSON object: %v", err)
	}
	raw, ok := m[key]
	if !ok {
		return fmt.Errorf("station body has no key %q: %s", key, body)
	}
	var got, want any
	_ = json.Unmarshal(raw, &got)
	if err := json.Unmarshal([]byte(value), &want); err != nil {
		return fmt.Errorf("bad expected value %q: %v", value, err)
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		return fmt.Errorf("station received %s=%s, want %s", key, gb, wb)
	}
	return nil
}

func (s *s0State) lastUpstreamBody() []byte {
	name := s.nameOfID(s.provider())
	s.bodiesMu.Lock()
	defer s.bodiesMu.Unlock()
	return s.bodies[name]
}

// sawNoValue: a null routing key is "not stated", so the relay routes exactly as with no
// routing object (the default pick among the two cheapest, as the no-carrier scenario pins)
// and the station sees no carrier at all.
func (s *s0State) sawNoValue(_ string) error {
	if err := s.pickIsOneOf("n-a", "n-b"); err != nil {
		return err
	}
	body := s.lastUpstreamBody()
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	for _, k := range routingCarriers {
		if _, has := m[k]; has {
			return fmt.Errorf("station received carrier %q: %s", k, body)
		}
	}
	return nil
}

func (s *s0State) bodyIsErrorObject(obj, field string) error {
	var m map[string]any
	if err := json.Unmarshal(s.lastBody, &m); err != nil {
		return fmt.Errorf("body is not a JSON object: %s", s.lastBody)
	}
	e, _ := m[obj].(map[string]any)
	if e == nil {
		return fmt.Errorf("body has no %q object: %s", obj, s.lastBody)
	}
	if _, ok := e[field].(string); !ok {
		return fmt.Errorf("%s.%s is not a string: %s", obj, field, s.lastBody)
	}
	return nil
}

func TestRoutingSlice0BDD(t *testing.T) {
	st := &s0State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.resetSlice0()
			})
			sc.After(func(ctx context.Context, scn *godog.Scenario, err error) (context.Context, error) {
				if err != nil {
					// The broker's own log lines are the fastest way to see WHY a real relay
					// went where it went; surface the scenario's tail on failure.
					all := st.logs.String()
					if len(all) > 4000 {
						all = all[len(all)-4000:]
					}
					t.Logf("broker log tail for %q:\n%s\nlast response %d %s", scn.Name, all, st.lastCode, st.lastBody)
				}
				st.teardownPins()
				return ctx, nil
			})
			// Background (both files)
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)
			sc.Step(`^the fee rate is 10%$`, st.feeRate10)
			sc.Step(`^stations "s1", "s2", "s3" serve "m", all on air, healthy \(Tier A\), seen just now$`, st.threeStations)
			sc.Step(`^"s1" prices out 1\.00, "s2" prices out 2\.00, "s3" prices out 3\.00 per 1M$`, st.threePrices)
			sc.Step(`^a funded consumer$`, st.fundedConsumer)
			sc.Step(`^the consumer default out-price cap is \$10/1M$`, st.defaultOutCap10)
			sc.Step(`^the operator ceilings are \$100/1M out and \$50/1M in$`, st.ceilings100and50)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at in \$([0-9.]+) out \$([0-9.]+) per 1M, seen just now$`, st.nodeOnAirPriced)
			sc.Step(`^a logged-in consumer "([^"]*)" with a \$([0-9.]+) balance$`, st.loggedInConsumer)
			// Given: station states
			sc.Step(`^"([^"]*)" 429s and "([^"]*)" serves$`, st.is429sAndServes)
			sc.Step(`^"([^"]*)" 429s with Retry-After (\d+) and "([^"]*)" serves$`, st.is429sRetryAfterAndServes)
			sc.Step(`^"([^"]*)" 429s, "([^"]*)" 429s, and "([^"]*)" serves$`, st.threeWay429)
			sc.Step(`^"([^"]*)" 429s and "([^"]*)" and "([^"]*)" serve$`, st.a429sBandCServe)
			sc.Step(`^"([^"]*)" 429s, "([^"]*)" 429s with Retry-After (\d+), and "([^"]*)" serves$`, st.a429sB429sRAcServes)
			sc.Step(`^"([^"]*)" 429s twice in a row and "([^"]*)" serves$`, st.twiceThenServes)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" serves$`, st.coolingAndServes)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" is cooling for (\d+) more seconds$`, st.bothCooling)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" is banned$`, st.coolingAndBanned)
			sc.Step(`^"([^"]*)" is banned and "([^"]*)" is cooling$`, st.bannedAndCooling)
			sc.Step(`^"([^"]*)" is stale$`, st.stale)
			sc.Step(`^"([^"]*)" was last seen 2\*nodeTTL ago$`, st.stale)
			sc.Step(`^"([^"]*)" is (banned|owned by a banned operator|stale \(last seen 2\*nodeTTL ago\)|past the dead-probe streak|cooling|private \(band-only\) and no code is presented|not offering "m"|priced out 12\.00 \(over the \$10 default cap\)|declaring ctx 4096 for an 8000-token prompt|below the request's min_tps|lacking a required capability|not TEE-attested on a confidential request|priced over the default out-cap)$`, st.stationState)
			sc.Step(`^"([^"]*)" prices out ([0-9.]+) per 1M$`, st.pricesOut)
			sc.Step(`^only "([^"]*)" and "([^"]*)" are TEE-attested$`, st.onlyAttested)
			sc.Step(`^"([^"]*)" and "([^"]*)" serve "m" free \(0/0\)$`, st.freeStations)
			sc.Step(`^the consumer's balance covers "([^"]*)" but not "([^"]*)"$`, st.balanceCovers)
			sc.Step(`^stations "s1"\.\."s5" serve "m" and all 429$`, st.fiveAll429)
			sc.Step(`^"([^"]*)" and "([^"]*)" are idle and "([^"]*)" is at capacity$`, st.idleAndAtCapacity)
			sc.Step(`^a private band "([^"]*)" with stations? (.+) for "m"$`, st.privateBand)
			sc.Step(`^"([^"]*)" is a private \(band-only\) station for "([^"]*)"$`, st.privateStationNoCode)
			// When: relays
			sc.Step(`^(\d+) funded consumers relay with provider\.ignore (\[.*\])$`, st.relaysIgnore)
			sc.Step(`^a funded consumer relays with provider\.ignore (\[.*\]) and the pick lands on "([^"]*)"$`, st.relayIgnoreLandsOn)
			sc.Step(`^a funded consumer relays with provider\.ignore (\[[^\]]*\]|"[^"]*"|\d+|33 distinct station ids)$`, st.relayIgnore)
			sc.Step(`^a funded consumer relays with X-Roger-Exclude-Nodes "([^"]*)" and provider\.ignore (\[.*\])$`, st.relayExcludeAndIgnore)
			sc.Step(`^a funded consumer relays with roger\.freq for band "([^"]*)" and provider\.ignore (\[.*\])$`, st.relayFreqIgnore)
			sc.Step(`^a funded consumer relays with roger\.freq for band "([^"]*)" and provider\.order (\[.*\])$`, st.relayFreqOrder)
			sc.Step(`^(\d+) funded consumers relay with provider\.order (\[.*\])$`, st.relaysOrder)
			sc.Step(`^a funded consumer relays with provider\.order (\[.*\]) and provider\.ignore (\[.*\])$`, st.relayOrderIgnore)
			sc.Step(`^a funded consumer relays with provider\.order (\[.*\]) and no max_price$`, st.relayOrderNoMaxPrice)
			sc.Step(`^a funded consumer relays with provider\.order (\[.*\]) and provider\.allow_fallbacks (true|false)$`, st.relayOrderFallbacks)
			sc.Step(`^a funded consumer relays with provider\.order (\[.*\]), provider\.allow_fallbacks false, and ROGERAI_RELAY_ATTEMPTS (\d+)$`, st.relayOrderNoFallbackAttempts)
			sc.Step(`^a funded consumer relays with provider\.order (\[[^\]]*\]|"[^"]*"|\d+|33 distinct station ids)$`, st.relayOrder)
			sc.Step(`^a funded consumer relays with X-Roger-Node "([^"]*)" and provider\.order (\[.*\])$`, st.relayPinAndOrder)
			sc.Step(`^a funded consumer relays with X-Roger-Node "([^"]*)", provider\.order (\[.*\]), and provider\.allow_fallbacks false$`, st.relayPinOrderNoFallbacks)
			sc.Step(`^a funded consumer relays with X-Roger-Node "([^"]*)" and provider\.allow_fallbacks true$`, st.relayPinFallbacksTrue)
			sc.Step(`^a funded consumer relays with X-Roger-Node "([^"]*)"$`, st.relayPin)
			sc.Step(`^a funded consumer relays with roger\.confidential true and provider\.order (\[.*\])$`, st.relayConfidentialOrder)
			sc.Step(`^an anonymous consumer relays with provider\.order (\[.*\])$`, st.anonRelayOrder)
			sc.Step(`^a funded consumer relays with provider\.allow_fallbacks false and the pick lands on "([^"]*)"$`, st.relayNoFallbackLandsOn)
			sc.Step(`^a funded consumer relays with provider\.allow_fallbacks false$`, st.relayNoFallback)
			sc.Step(`^a funded consumer relays with provider\.allow_fallbacks ("[^"]*"|\d+|null|true)$`, st.relayFallbacksRaw)
			sc.Step("^\"([^\"]*)\" posts a chat completion for \"([^\"]*)\" with body `(.*)`$", st.postsBody)
			sc.Step("^\"([^\"]*)\" posts a chat completion for \"([^\"]*)\" with header X-Roger-Exclude-Nodes \"([^\"]*)\" and body `(.*)`$", st.postsHeaderExcludeBody)
			sc.Step("^\"([^\"]*)\" posts a STREAMING chat completion for \"([^\"]*)\" with header X-Roger-Node \"([^\"]*)\" and body `(.*)`$", st.postsStreamingPinBody)
			sc.Step(`^"([^"]*)" posts a chat completion with a body that is not a JSON object$`, st.postsNonObject)
			// Then
			sc.Step(`^the pick is "([^"]*)"$`, st.pickIs)
			sc.Step(`^the pick is "([^"]*)" or "([^"]*)" by score$`, st.pickIsOneOf2)
			sc.Step(`^the pick is "([^"]*)", "([^"]*)" or "([^"]*)" by score$`, st.pickIsOneOf3)
			sc.Step(`^every pick is "([^"]*)"$`, st.everyPickIs)
			sc.Step(`^every pick is "([^"]*)" or "([^"]*)"$`, st.everyPickIsOr)
			sc.Step(`^"([^"]*)" is picked at least once$`, st.pickedAtLeastOnce)
			sc.Step(`^the pick distribution matches (\d+) relays with no routing object$`, st.distributionMatches)
			sc.Step(`^"([^"]*)" received nothing$`, st.receivedNothing)
			sc.Step(`^"([^"]*)" and "([^"]*)" received nothing$`, st.bothReceivedNothing)
			sc.Step(`^no station received anything$`, st.noStationReceivedAnything)
			sc.Step(`^"([^"]*)" received exactly one attempt$`, st.receivedExactlyOne)
			sc.Step(`^the response is (\d+)$`, st.responseIs)
			sc.Step(`^the response is (\d+) \{"error":\{"code":"([^"]*)"\}\}$`, st.responseIsWithCode)
			sc.Step(`^the response is (\d+) \{"error":\{"code":"([^"]*)"\}\} with Retry-After: (\d+)$`, st.responseIsWithCodeRetryAfter)
			sc.Step(`^the response is 400 with error\.code "([^"]*)" naming "([^"]*)"$`, st.response400Naming)
			sc.Step(`^the response is 503 "([^"]*)"$`, st.response503Message)
			sc.Step(`^the body carries no error code \(§2\)$`, st.noErrorCode)
			sc.Step(`^the error code is "([^"]*)"$`, st.errorCodeIs)
			sc.Step(`^the error message names "([^"]*)"$`, st.errorMessageNames)
			sc.Step(`^the failover goes to "([^"]*)", never "([^"]*)"$`, st.failoverGoesToNever)
			sc.Step(`^the failover goes to "([^"]*)"$`, st.failoverGoesTo)
			sc.Step(`^attempt 1 hit "([^"]*)", attempt 2 hit "([^"]*)", attempt 3 hit "([^"]*)"$`, st.attemptsHit)
			sc.Step(`^attempt 1 hit "([^"]*)" and attempt 2 hit "([^"]*)" or "([^"]*)"$`, st.attempt1And2Either)
			sc.Step(`^attempt 1 hit "([^"]*)" and attempt 2 hit "([^"]*)"$`, st.attempt1And2)
			sc.Step(`^the consumer sees 200 with X-RogerAI-Provider "([^"]*)"$`, st.consumerSees200Provider)
			sc.Step(`^the consumer sees (\d+)$`, st.consumerSees)
			sc.Step(`^exactly one attempt was made, to "([^"]*)"$`, st.exactlyOneAttemptTo)
			sc.Step(`^exactly one attempt was made$`, st.exactlyOneAttempt)
			sc.Step(`^exactly (\d+) attempts were made, to "([^"]*)", "([^"]*)", "([^"]*)"$`, st.exactlyNAttemptsTo)
			sc.Step(`^the response is (\d+) with Retry-After: (\d+)$`, st.responseWithRetryAfter)
			sc.Step(`^the response is (\d+) with Retry-After$`, st.responseHasRetryAfter)
			sc.Step(`^no Retry-After header is present$`, st.noRetryAfter)
			sc.Step(`^the response carries no warning about "([^"]*)"$`, st.noWarningAbout)
			sc.Step(`^the header is ignored without error$`, st.headerIgnoredWithoutError)
			sc.Step(`^fallbacks are allowed \(the body's default\), not disabled by the header$`, st.fallbacksAllowed)
			sc.Step(`^no hold was placed$`, st.noHold)
			sc.Step(`^exactly one hold was placed$`, st.exactlyOneHold)
			sc.Step(`^the hold covers "([^"]*)" at ([0-9.]+)$`, st.holdCovers)
			sc.Step(`^the hold covers "([^"]*)" at ([0-9.]+), not "([^"]*)" at ([0-9.]+)$`, st.holdCoversNot)
			sc.Step(`^the hold covered exactly the picked station, not the priciest of three$`, st.holdCoveredPicked)
			sc.Step(`^on success at "([^"]*)" the consumer is charged at ([0-9.]+) and the remainder released$`, st.chargedAtAndReleased)
			sc.Step(`^the served node is "([^"]*)"$`, st.servedNodeIs)
			sc.Step(`^the station received top-level key "([^"]*)" with value (.+)$`, st.stationReceivedKey)
			sc.Step(`^the routing pass saw no value for "([^"]*)"$`, st.sawNoValue)
			sc.Step(`^the response body is a JSON object with an "([^"]*)" object holding string "([^"]*)"$`, st.bodyIsErrorObject)
		},
		Options: &godog.Options{
			Format: "pretty",
			Tags:   "@slice0",
			Paths: []string{
				"../../features/routing/node_preference.feature",
				"../../features/routing/request_shape.feature",
			},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("slice-0 node preference / request shape scenarios failed")
	}
}
