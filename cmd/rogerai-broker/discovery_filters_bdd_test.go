package main

// discovery_filters_bdd_test.go makes features/discovery/filters.feature and the untagged
// (non-@slice0) scenarios of features/discovery/models_endpoint.feature EXECUTABLE against the
// REAL broker: real stations stood up through the upstream_failover harness (foState / rpState:
// real registry, real store incl. Postgres when ROGERAI_TEST_DATABASE_URL is set, the shared
// valkey store over miniredis, scripted upstreams behind real tunnels), the public reads served
// through the real mux (b.routes()), relays through the real relay(). No mocks.
//
// RED notes (slice 2): today /discover and /market ignore every query param (market.go:19),
// /v1/models is the slice-0 minimum (list shape, created per instance, rogerai{cooling}) and
// protocol.ModelOffer has NO params_b field. A Given that declares params_b therefore cannot
// put it on the wire: this runner RECORDS the declared value in a side table (df2State.params)
// and the Thens that read params_b / params_estimated off the view fail honestly naming the
// missing field. (A Given that failed outright would take every scenario of both Backgrounds
// down with it, including the green @slice0 pins, and hide the per-scenario failures.)
//
// Observation points this runner relies on:
//   - the shared cache is the miniredis behind foState.vs: "computeDiscover ran once" is read as
//     exactly one "discover:" key (every compute writes its key), "not re-run" as the key's TTL
//     not having been reset within publicMarketTTL;
//   - "the stale nodes are scheduled for a demand probe" is probeSched[node].nextDue <= now;
//   - "no log line contains" reads the harness's captured broker log (foState.logs).
//
// The @slice0 scenarios of models_endpoint.feature keep running in TestModelsEndpointSlice0BDD
// (its dmState harness has no relay); TestModelsEndpointBDD here runs the rest, so the two
// cover the whole file. Scenarios drivable only from the local proxy or the docs runner are
// tagged @proxy / @docs in the .feature files.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type df2State struct {
	*rpState

	// the last public read
	code   int
	hdr    http.Header
	body   []byte
	kind   string // discover | market | models | model | band | other
	offers []map[string]any
	market []map[string]any
	data   []map[string]any
	obj    map[string]any

	params    map[string]string // declared params_b by scenario station name (no wire field yet)
	bandCode  string
	codes     []int
	bodies    [][]byte
	keyTTL    map[string]time.Duration
	burstErr  string
	relayOut  []rpResult
	anon      bool
	bInst     map[string]*broker
	perInst   map[string][]map[string]any
	lastNamed string
}

func (s *df2State) reset() error {
	if err := s.resetPins(); err != nil {
		return err
	}
	s.code, s.hdr, s.body, s.kind = 0, nil, nil, ""
	s.offers, s.market, s.data, s.obj = nil, nil, nil, nil
	s.params = map[string]string{}
	s.bandCode, s.burstErr, s.anon = "", "", false
	s.codes, s.bodies, s.relayOut = nil, nil, nil
	s.keyTTL = map[string]time.Duration{}
	s.bInst = map[string]*broker{}
	s.perInst = map[string][]map[string]any{}
	return nil
}

// --- fixtures -------------------------------------------------------------------------

type df2Row map[string]string

func df2Table(table *godog.Table) []df2Row {
	head := table.Rows[0].Cells
	var rows []df2Row
	for _, r := range table.Rows[1:] {
		row := df2Row{}
		for i, h := range head {
			if i < len(r.Cells) {
				row[h.Value] = strings.TrimSpace(r.Cells[i].Value)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func df2Float(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// addStation stands up a REAL station for model with the row's attributes. Attributes the
// protocol carries (quant, ctx, ctx_estimated, modality, prices, capabilities) go onto the
// offer; node-level ones (region, curated, confidential) onto the registration / broker maps;
// measured ones (tps, ttft) into the broker's measurement maps; verified = a completed canary.
func (s *df2State) addStation(name, model string, row df2Row) {
	s.model = model
	st := s.station(name, df2Float(row["price_in"]), df2Float(row["price_out"]))
	ctx, _ := strconv.Atoi(row["ctx"])
	s.setOffer(st.id, func(o *protocol.ModelOffer) {
		o.Quant = row["quant"]
		if ctx > 0 {
			o.Ctx = ctx
		}
		o.CtxEstimated = row["ctx_estimated"] == "true"
		if m := row["modality"]; m != "" {
			o.Modality = m
		}
	})
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Region = row["region"]
	if row["curated"] == "true" {
		reg.Curated, reg.CuratedProvider = true, "openrouter"
	}
	s.b.nodes[st.id] = reg
	if row["confidential"] == "true" {
		s.b.confidential[st.id] = true
	}
	s.b.mu.Unlock()
	s.setTPS(st.id, df2Float(row["tps"]))
	s.b.metricsMu.Lock()
	tq := trustState{probed: true, probeOK: true, ttftMs: df2Float(row["ttft_ms"])}
	if row["verified"] == "true" {
		tq.probeCompleted = true
		if s.b.probeSched == nil {
			s.b.probeSched = map[string]*probeState{}
		}
		s.b.probeSched[st.id] = &probeState{lastMeasured: time.Now()}
	}
	s.b.trust[st.id] = tq
	s.b.metricsMu.Unlock()
	if p := row["params_b"]; p != "" {
		s.params[name] = p
		// the declared count rides the offer (protocol.ModelOffer.ParamsB, slice 2)
		v := df2Float(p)
		s.setOffer(st.id, func(o *protocol.ModelOffer) { o.ParamsB = v })
	}
}

func (s *df2State) nodesOnAirFor(model string, table *godog.Table) error {
	for _, row := range df2Table(table) {
		s.addStation(row["node"], model, row)
	}
	return nil
}

func (s *df2State) nodesOnAir(table *godog.Table) error {
	for _, row := range df2Table(table) {
		s.addStation(row["node"], row["model"], row)
	}
	return nil
}

func (s *df2State) nodeOnAirQuantParamsTPSRegion(name, model, quant, params, tps, region string) error {
	s.addStation(name, model, df2Row{"quant": quant, "params_b": params, "tps": tps, "region": region, "price_in": "0.10", "price_out": "0.30"})
	return nil
}

func (s *df2State) nodeOnAir(name, model string) error {
	s.addStation(name, model, df2Row{"price_in": "0.10", "price_out": "0.30", "tps": "20"})
	return nil
}

func (s *df2State) nodeOnAirNoParams(name, model string) error { return s.nodeOnAir(name, model) }

func (s *df2State) nodeOnlyStationNoParams(name, model string) error {
	// a station arriving mid-scenario: the next read is past the public cache window
	defer s.df2FlushCache()
	return s.nodeOnAir(name, model)
}

func (s *df2State) nodeAlsoOnAirParams(name, model, params string) error {
	s.addStation(name, model, df2Row{"price_in": "0.10", "price_out": "0.30", "tps": "20", "params_b": params})
	return nil
}

func (s *df2State) nodeOnAirModality(name, model, modality string) error {
	s.addStation(name, model, df2Row{"price_in": "0.10", "price_out": "0.30", "tps": "20", "modality": modality})
	return nil
}

func (s *df2State) freeWindowNow(name string) error {
	s.setOffer(s.idOf(name), func(o *protocol.ModelOffer) {
		o.Schedule = []protocol.PriceWindow{
			{Start: "00:00", End: "12:00", Free: true},
			{Start: "12:00", End: "00:00", Free: true}, // wraps past midnight: 12:00-23:59
		}
	})
	return nil
}

func (s *df2State) earnedTools(name, model string) error {
	st, ok := s.stations[name]
	if !ok {
		return fmt.Errorf("no station %q", name)
	}
	s.b.recordToolProbe(st.id, model, true, false, true)
	s.b.metricsMu.Lock()
	ok = s.b.toolsVerifiedForLocked(st.id, model)
	s.b.metricsMu.Unlock()
	if !ok {
		return fmt.Errorf("fixture: the tools verdict for (%s, %s) did not land", name, model)
	}
	return nil
}

func (s *df2State) declaredVision(name, model string) error {
	s.setOffer(s.idOf(name), func(o *protocol.ModelOffer) {
		if o.Model == model {
			o.Capabilities = append(o.Capabilities, protocol.CapVision)
		}
	})
	return nil
}

func (s *df2State) earnedToolsDeclaredVision(name, model string) error {
	if err := s.earnedTools(name, model); err != nil {
		return err
	}
	return s.declaredVision(name, model)
}

// declaredToolsNoCanary writes a wire-declared "tools" straight into the registry (bypassing
// the register door that strips it) so the view's verified-only rule is what keeps it out.
func (s *df2State) declaredToolsNoCanary(name, model string) error {
	s.setOffer(s.idOf(name), func(o *protocol.ModelOffer) {
		if o.Model == model {
			o.Capabilities = append(o.Capabilities, protocol.CapTools)
		}
	})
	return nil
}

func (s *df2State) nodeBanned(name string) error {
	id := s.idOf(name)
	s.b.metricsMu.Lock()
	s.b.banned[id] = true
	s.b.metricsMu.Unlock()
	return nil
}

func (s *df2State) nodeStale(name string) error {
	s.b.mu.Lock()
	s.b.lastSeen[s.idOf(name)] = time.Now().Add(-2 * nodeTTL)
	s.b.mu.Unlock()
	return nil
}

func (s *df2State) nodeCooling(name string, secs int) error {
	st, ok := s.stations[name]
	if !ok {
		return fmt.Errorf("no station %q", name)
	}
	s.b.coolStation(st.id, st.model, secs)
	return nil
}

func (s *df2State) onlyStationCooling(model string) error {
	n := 0
	for name, st := range s.stations {
		if st.model == model {
			n++
			if err := s.nodeCooling(name, 30); err != nil {
				return err
			}
		}
	}
	if n != 1 {
		return fmt.Errorf("fixture: %d stations serve %q, want exactly one", n, model)
	}
	return nil
}

func (s *df2State) probeDeadOnPollHost(name string) error {
	id := s.idOf(name)
	s.b.metricsMu.Lock()
	tq := s.b.trust[id]
	tq.probeFails = probeDeadStreak
	s.b.trust[id] = tq
	if s.b.probeSched == nil {
		s.b.probeSched = map[string]*probeState{}
	}
	s.b.probeSched[id] = &probeState{} // no recent serving evidence
	s.b.metricsMu.Unlock()
	s.b.mu.Lock()
	if s.b.localPollAt == nil {
		s.b.localPollAt = map[string]time.Time{}
	}
	s.b.localPollAt[id] = time.Now() // THIS instance hosts the poll: authoritative
	s.b.mu.Unlock()
	return nil
}

func (s *df2State) everyoneLeft() error {
	s.b.mu.Lock()
	for id := range s.b.nodes {
		delete(s.b.nodes, id)
		delete(s.b.lastSeen, id)
	}
	s.b.mu.Unlock()
	return nil
}

func (s *df2State) everyCtxEstimated(model string) error {
	for _, st := range s.stations {
		if st.model == model {
			s.setOffer(st.id, func(o *protocol.ModelOffer) { o.CtxEstimated = true })
		}
	}
	return nil
}

func (s *df2State) privateBandQuant(name, model, quant string) error {
	return s.privateBand(name, model, df2Row{"quant": quant})
}

func (s *df2State) privateBandParams(name, model, params string) error {
	return s.privateBand(name, model, df2Row{"params_b": params})
}

func (s *df2State) privateBand(name, model string, row df2Row) error {
	row["price_in"], row["price_out"], row["tps"] = "0.20", "0.20", "20"
	s.addStation(name, model, row)
	st := s.stations[name]
	s.b.mu.Lock()
	s.b.private[st.id] = true
	s.b.mu.Unlock()
	s.bandCode = "147.520 MHz · DF2-" + strings.ToUpper(s.nonce[:4])
	return s.db.CreateBand(store.Band{ID: "band_df2_" + s.nonce, CodeHash: protocol.BandCodeHash(s.bandCode),
		CodeDisplay: "147.520 MHz · ••••-••••", Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()})
}

func (s *df2State) cacheColdProbingOn() error {
	s.b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	s.df2FlushCache()
	s.b.localCacheMu.Lock()
	s.b.localCache = map[string]localCacheEntry{}
	s.b.localCacheMu.Unlock()
	// "stale nodes": every station was measured long ago, so the demand hook has something
	// to refresh (a never-measured node reads as neutral and is not pulled in).
	s.b.metricsMu.Lock()
	if s.b.probeSched == nil {
		s.b.probeSched = map[string]*probeState{}
	}
	for _, st := range s.stations {
		s.b.probeSched[st.id] = &probeState{lastMeasured: time.Now().Add(-time.Hour)}
	}
	s.b.metricsMu.Unlock()
	return nil
}

func (s *df2State) discoverCachedWithinTTL() error {
	if err := s.get("/discover", nil); err != nil {
		return err
	}
	s.snapshotCacheTTLs("discover:")
	return nil
}

func (s *df2State) modelsComputedWithinTTL() error {
	if err := s.get("/v1/models", nil); err != nil {
		return err
	}
	s.snapshotCacheTTLs("models")
	return nil
}

func (s *df2State) cacheKeys(prefix string) []string {
	var out []string
	for _, k := range s.mr.Keys() {
		if strings.HasPrefix(k, prefix) || strings.Contains(k, ":"+prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *df2State) snapshotCacheTTLs(prefix string) {
	s.keyTTL = map[string]time.Duration{}
	for _, k := range s.cacheKeys(prefix) {
		s.keyTTL[k] = s.mr.TTL(k)
	}
}

func (s *df2State) fundedConsumer(_ string) error { return s.ensureFunded() }

func (s *df2State) allPaid(model string) error {
	for name, st := range s.stations {
		if st.model == model && st.priceIn == 0 && st.priceOut == 0 {
			return fmt.Errorf("fixture: %q serves %q at 0/0", name, model)
		}
	}
	return nil
}

// df2FlushCache drops the public-read cache only (the shared ":cache:" entries and the
// in-process ones). Other shared-store records - the first-on-air times, cooldowns, holds -
// are state, not cache, and survive.
// df2AgedVerified ages a verified station's last measurement past the verified window (the
// probe measurement freshness ceiling routing uses; the harness runs with no probe config, so
// the production default ceiling is configured, as the routing runner does).
func (s *df2State) df2AgedVerified(name string) error {
	st, ok := s.stations[name]
	if !ok {
		return fmt.Errorf("no station %q", name)
	}
	s.b.metricsMu.Lock()
	if s.b.probe.ceiling <= 0 {
		s.b.probe.ceiling = defaultProbeCeiling
	}
	sched := s.b.probeSchedLocked()
	ps := sched[st.id]
	if ps == nil {
		ps = &probeState{}
		sched[st.id] = ps
	}
	aged := time.Now().Add(-s.b.probe.ceiling - time.Minute)
	ps.lastMeasured, ps.lastProbe = aged, aged
	s.b.metricsMu.Unlock()
	s.df2FlushCache()
	return nil
}

func (s *df2State) df2FlushCache() {
	for _, k := range s.mr.Keys() {
		if strings.Contains(k, ":cache:") {
			s.mr.Del(k)
		}
	}
	s.b.localCacheMu.Lock()
	s.b.localCache = map[string]localCacheEntry{}
	s.b.localCacheMu.Unlock()
}

func (s *df2State) firstOnAirAt(model string, unixStr string) error {
	// Contract §8: `created` is the unix time the model FIRST came on air, a SHARED-store
	// record. The broker's own writer records it (the first read that sees the model on air
	// writes it once; later writes never move it).
	unix, err := strconv.ParseInt(strings.ReplaceAll(unixStr, "_", ""), 10, 64)
	if err != nil {
		return err
	}
	if got := s.b.firstSeenModel(model, unix); got != unix {
		return fmt.Errorf("fixture: %q already has a first-on-air time %d", model, got)
	}
	return nil
}

func (s *df2State) twoInstances(a, b string) error {
	s.bInst[a] = s.b
	s.bInst[b] = s.instanceB()
	return nil
}

func (s *df2State) firstOnAirWhileDown(model, unixStr, inst string) error {
	delete(s.bInst, inst) // "B is down": its broker is discarded; it comes up fresh below
	return s.firstOnAirAt(model, unixStr)
}

func (s *df2State) instanceComesUpBothGet(inst, a, b string) error {
	if s.bInst[inst] == nil {
		s.bInst[inst] = s.instanceB()
	}
	for _, name := range []string{a, b} {
		br := s.bInst[name]
		if br == nil {
			return fmt.Errorf("no instance %q", name)
		}
		rec := httptest.NewRecorder()
		br.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		var out struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		s.perInst[name] = out.Data
	}
	return nil
}

func (s *df2State) bothInstancesCreated(unixStr, model string) error {
	want := df2Unix(unixStr)
	for name, data := range s.perInst {
		found := false
		for _, e := range data {
			if e["id"] == model {
				found = true
				if got, _ := e["created"].(float64); int64(got) != want {
					return fmt.Errorf("instance %s shows created %v for %q, want %d", name, e["created"], model, want)
				}
			}
		}
		if !found {
			return fmt.Errorf("instance %s does not list %q", name, model)
		}
	}
	if len(s.perInst) < 2 {
		return fmt.Errorf("only %d instance(s) were read", len(s.perInst))
	}
	return nil
}

func df2Unix(s string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64)
	return n
}

// --- reads -----------------------------------------------------------------------------

func (s *df2State) get(path string, hdr map[string]string) error {
	return s.do(http.MethodGet, path, hdr)
}

func (s *df2State) do(method, path string, hdr map[string]string) error {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.b.routes().ServeHTTP(rec, req)
	s.code, s.hdr, s.body = rec.Code, rec.Header(), rec.Body.Bytes()
	s.decode(path)
	return nil
}

func (s *df2State) decode(path string) {
	s.offers, s.market, s.data, s.obj = nil, nil, nil, nil
	u, _ := url.Parse(path)
	p := u.Path
	switch {
	case p == "/discover":
		s.kind = "discover"
	case p == "/market":
		s.kind = "market"
	case p == "/v1/models":
		s.kind = "models"
	case strings.HasPrefix(p, "/v1/models/"):
		s.kind = "model"
	case p == "/bands/resolve":
		s.kind = "band"
	default:
		s.kind = "other"
	}
	var obj map[string]any
	if json.Unmarshal(s.body, &obj) == nil {
		s.obj = obj
	}
	if s.obj == nil {
		return
	}
	s.offers = df2List(s.obj["offers"])
	s.market = df2List(s.obj["market"])
	s.data = df2List(s.obj["data"])
}

func df2List(v any) []map[string]any {
	arr, _ := v.([]any)
	var out []map[string]any
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *df2State) consumerGets(path string) error { return s.get(path, nil) }

func (s *df2State) consumerGetsFromWebsite(path string) error {
	return s.get(path, map[string]string{"Origin": "https://rogerai.fm"})
}

func (s *df2State) consumerGetsWithHeader(path, h string) error {
	k, v, _ := strings.Cut(h, ":")
	return s.get(path, map[string]string{strings.TrimSpace(k): strings.TrimSpace(v)})
}

func (s *df2State) anonNoAuthGets(path string) error { return s.get(path, nil) }

func (s *df2State) browserOptions(path string) error {
	return s.do(http.MethodOptions, path, map[string]string{"Origin": "https://rogerai.fm"})
}

func (s *df2State) consumerSends(method, path string) error { return s.do(method, path, nil) }

func (s *df2State) getsTwiceTenSecondsApart(path string) error {
	if err := s.get(path, nil); err != nil {
		return err
	}
	s.bodies = [][]byte{append([]byte(nil), s.body...)}
	s.advance(10 * time.Second)
	// the public cache would otherwise hand back the same bytes within publicMarketTTL
	s.df2FlushCache()
	if err := s.get(path, nil); err != nil {
		return err
	}
	s.bodies = append(s.bodies, append([]byte(nil), s.body...))
	return nil
}

func (s *df2State) bothReadsCreated(unixStr, model string) error {
	want := df2Unix(unixStr)
	for i, body := range s.bodies {
		var out struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.Unmarshal(body, &out)
		found := false
		for _, e := range out.Data {
			if e["id"] == model {
				found = true
				if got, _ := e["created"].(float64); int64(got) != want {
					return fmt.Errorf("read %d shows created %v for %q, want %d", i+1, e["created"], model, want)
				}
			}
		}
		if !found {
			return fmt.Errorf("read %d does not list %q", i+1, model)
		}
	}
	return nil
}

func (s *df2State) burst(path string, n int) error {
	s.codes, s.bodies = nil, nil
	for i := 0; i < n; i++ {
		if err := s.get(path, nil); err != nil {
			return err
		}
		s.codes = append(s.codes, s.code)
		s.bodies = append(s.bodies, append([]byte(nil), s.body...))
	}
	return nil
}

func (s *df2State) oneIPBurstDiscover(path string, n int) error { return s.burst(path, n) }

// hundredCombinations fires n distinct filter combinations within one TTL window.
func (s *df2State) hundredCombinations(base string, n int) error {
	parts := []string{"model=qwen3-32b", "quant=Q8_0", "region=eu", "region=us", "min_tps=10", "min_tps=30",
		"self_hosted=1", "free=1", "confidential=1", "max_price_out=0.5", "min_ctx=32768", "params_min=30", "trust_min=verified"}
	seen := map[string]bool{}
	s.codes = nil
	for i := 0; len(seen) < n; i++ {
		var q []string
		for j := 0; j < len(parts); j++ {
			if (i>>uint(j))&1 == 1 {
				q = append(q, parts[j])
			}
		}
		key := strings.Join(q, "&")
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := s.get(base+"?"+key, nil); err != nil {
			return err
		}
		s.codes = append(s.codes, s.code)
	}
	return nil
}

func (s *df2State) readWhileLocked(path string) error {
	// The filter pass must sit AFTER the cache: warm it, then hold b.mu while reading.
	if err := s.get("/discover", nil); err != nil {
		return err
	}
	s.b.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.get(path, nil) }()
	select {
	case err := <-done:
		s.b.mu.Unlock()
		return err
	case <-time.After(2 * time.Second):
		s.b.mu.Unlock()
		s.burstErr = "the read waited on b.mu"
		return nil
	}
}

func (s *df2State) returnedWithoutWaiting() error {
	if s.burstErr != "" {
		return fmt.Errorf("%s: a filtered public read must be served from the cached payload without the broker lock", s.burstErr)
	}
	return nil
}

func (s *df2State) ownerResolvesCode() error {
	body, _ := json.Marshal(map[string]string{"freq": s.bandCode})
	req := httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.b.routes().ServeHTTP(rec, req)
	s.code, s.hdr, s.body = rec.Code, rec.Header(), rec.Body.Bytes()
	s.decode("/bands/resolve")
	return nil
}

func (s *df2State) getsThreeViews() error {
	s.bodies = nil
	for _, p := range []string{"/v1/models", "/market", "/discover"} {
		if err := s.get(p, nil); err != nil {
			return err
		}
		s.bodies = append(s.bodies, append([]byte(nil), s.body...))
	}
	return nil
}

// --- relays ----------------------------------------------------------------------------

func (s *df2State) relayModel(model string, anon bool) error {
	s.model = model
	s.kind = "" // the next status/error assertions read the relay, not a public read
	if anon {
		s.callerPriv = s.anonPriv
	} else {
		s.callerPriv = nil
	}
	if err := s.relayOnce(false); err != nil {
		return err
	}
	s.relayOut = append(s.relayOut, s.batch[len(s.batch)-1])
	return nil
}

func (s *df2State) fundedListsThenRelaysEach(_ string) error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	if err := s.get("/v1/models", nil); err != nil {
		return err
	}
	s.relayOut = nil
	for _, e := range s.data {
		id, _ := e["id"].(string)
		if ra, _ := e["rogerai"].(map[string]any); ra != nil {
			if c, _ := ra["cooling"].(bool); c {
				continue
			}
		}
		if err := s.relayModel(id, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *df2State) noNoNodeOffers() error {
	for _, r := range s.relayOut {
		if bytes.Contains(r.body, []byte("no node offers")) {
			return fmt.Errorf("a listed id relayed to %d %s", r.code, r.body)
		}
	}
	if len(s.relayOut) == 0 {
		return fmt.Errorf("no listed id was relayed")
	}
	return nil
}

func (s *df2State) fundedGetsModels() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	return s.get("/v1/models", nil)
}

func (s *df2State) anonGetsModels() error { s.anon = true; return s.get("/v1/models", nil) }

func (s *df2State) consumerPosts(model string) error { return s.relayModel(model, false) }

func (s *df2State) anonPosts(model string) error { return s.relayModel(model, true) }

func (s *df2State) lastRelay() (rpResult, error) {
	if len(s.relayOut) == 0 {
		return rpResult{}, fmt.Errorf("no relay was made")
	}
	return s.relayOut[len(s.relayOut)-1], nil
}

// --- assertions: status, envelope, headers -----------------------------------------------

func (s *df2State) statusIs(code int) error {
	if s.relayPending() {
		r, _ := s.lastRelay()
		if r.code != code {
			return fmt.Errorf("relay = %d, want %d: %s", r.code, code, r.body)
		}
		return nil
	}
	if s.code != code {
		return fmt.Errorf("status = %d, want %d: %s", s.code, code, s.body)
	}
	return nil
}

// relayPending reports whether the most recent action was a relay rather than a public read.
func (s *df2State) relayPending() bool { return s.kind == "" && len(s.relayOut) > 0 }

func (s *df2State) statusAndID(code int, id string) error {
	if err := s.statusIs(code); err != nil {
		return err
	}
	if got, _ := s.obj["id"].(string); got != id {
		return fmt.Errorf("id = %q, want %q: %s", got, id, s.body)
	}
	return nil
}

func (s *df2State) errCode() (string, string) {
	var body []byte
	if s.relayPending() {
		r, _ := s.lastRelay()
		body = r.body
	} else {
		body = s.body
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return env.Error.Code, env.Error.Message
}

func (s *df2State) errorCodeIs(code string) error {
	got, msg := s.errCode()
	if got != code {
		return fmt.Errorf("error.code = %q (message %q), want %q", got, msg, code)
	}
	return nil
}

func (s *df2State) errorMessageNames(word string) error {
	_, msg := s.errCode()
	if !strings.Contains(msg, word) {
		return fmt.Errorf("error.message %q does not name %q", msg, word)
	}
	return nil
}

func (s *df2State) errorSaysLogIn() error {
	_, msg := s.errCode()
	if !strings.Contains(strings.ToLower(msg), "log in") {
		return fmt.Errorf("error.message %q does not say to log in to spend", msg)
	}
	return nil
}

func (s *df2State) carriesRetryAfter() error {
	r, err := s.lastRelay()
	if err != nil {
		return err
	}
	if r.hdr.Get("Retry-After") == "" {
		return fmt.Errorf("the response carries no Retry-After (status %d)", r.code)
	}
	return nil
}

// bodyIs compares the body with a JSON literal; a `...` value is a wildcard for any value.
func (s *df2State) bodyIs(lit string) error {
	want := strings.TrimSpace(lit)
	var w any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(want, "...", `"__any__"`)), &w); err != nil {
		return fmt.Errorf("bad literal in the step: %v", err)
	}
	var g any
	if err := json.Unmarshal(s.body, &g); err != nil {
		return fmt.Errorf("body is not JSON: %s", s.body)
	}
	if !df2Match(w, g) {
		return fmt.Errorf("body = %s, want %s", bytes.TrimSpace(s.body), want)
	}
	return nil
}

func df2Match(want, got any) bool {
	switch w := want.(type) {
	case string:
		if w == "__any__" {
			return got != nil
		}
		g, ok := got.(string)
		return ok && g == w
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, v := range w {
			if !df2Match(v, g[k]) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !df2Match(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprint(want) == fmt.Sprint(got)
	}
}

func (s *df2State) bodyIsOneModel(id, object, owner string) error {
	if s.obj == nil {
		return fmt.Errorf("body is not a JSON object: %s", s.body)
	}
	if s.obj["id"] != id || s.obj["object"] != object || s.obj["owned_by"] != owner {
		return fmt.Errorf("body = %s, want id %q object %q owned_by %q", s.body, id, object, owner)
	}
	if _, ok := s.obj["rogerai"].(map[string]any); !ok {
		return fmt.Errorf("body carries no rogerai block: %s", s.body)
	}
	return nil
}

func (s *df2State) sameCORSAsUnfiltered() error {
	got := map[string]string{}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
		got[h] = s.hdr.Get(h)
	}
	if err := s.get("/discover", map[string]string{"Origin": "https://rogerai.fm"}); err != nil {
		return err
	}
	for h, v := range got {
		if ref := s.hdr.Get(h); ref != v {
			return fmt.Errorf("%s = %q on the 400, %q on an unfiltered /discover", h, v, ref)
		}
		if v == "" {
			return fmt.Errorf("%s is absent on the 400", h)
		}
	}
	return nil
}

func (s *df2State) corsIdentical(origin, methods, headers string) error {
	for h, want := range map[string]string{"Access-Control-Allow-Origin": origin, "Access-Control-Allow-Methods": methods, "Access-Control-Allow-Headers": headers} {
		if got := s.hdr.Get(h); got != want {
			return fmt.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	return nil
}

func (s *df2State) corsPresent() error {
	return s.corsIdentical("*", "GET, OPTIONS", "Content-Type")
}

// --- assertions: offers and market -------------------------------------------------------

func (s *df2State) nOffersListed(n int) error {
	if s.code != 200 {
		return fmt.Errorf("status %d: %s", s.code, s.body)
	}
	if len(s.offers) != n {
		return fmt.Errorf("%d offers listed, want %d: %s", len(s.offers), n, s.listedNames())
	}
	return nil
}

func (s *df2State) nOffersAllFor(n int, model string) error {
	if err := s.nOffersListed(n); err != nil {
		return err
	}
	return s.everyOfferFor(model)
}

func (s *df2State) everyOfferFor(model string) error {
	if s.code != 200 {
		return fmt.Errorf("status %d: %s", s.code, s.body)
	}
	for _, o := range s.offers {
		if o["model"] != model {
			return fmt.Errorf("offer %v is for %v, want every offer for %q", o["node_id"], o["model"], model)
		}
	}
	return nil
}

func (s *df2State) orderedByPriceIn() error {
	last := -1.0
	for _, o := range s.offers {
		p, _ := o["price_in"].(float64)
		if p < last {
			return fmt.Errorf("offers are not ordered by price_in ascending: %v", s.listedNames())
		}
		last = p
	}
	return nil
}

func (s *df2State) nameOf(id string) string {
	for name, st := range s.stations {
		if st.id == id {
			return name
		}
	}
	return id
}

func (s *df2State) listedNames() []string {
	var out []string
	for _, o := range s.offers {
		id, _ := o["node_id"].(string)
		out = append(out, s.nameOf(id))
	}
	return out
}

func df2Split(list string) []string {
	list = strings.Trim(strings.TrimSpace(list), `"`)
	if list == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *df2State) listedExactly(list string) error {
	if s.code != 200 {
		return fmt.Errorf("status %d: %s", s.code, s.body)
	}
	want := df2Split(list)
	got := s.listedNames()
	sw, sg := append([]string(nil), want...), append([]string(nil), got...)
	sort.Strings(sw)
	sort.Strings(sg)
	if strings.Join(sw, ",") != strings.Join(sg, ",") {
		return fmt.Errorf("listed node ids = %v, want exactly %v", got, want)
	}
	return nil
}

func (s *df2State) listedInOrder(list string) error {
	if err := s.listedExactly(list); err != nil {
		return err
	}
	if got := strings.Join(s.listedNames(), ","); got != strings.Join(df2Split(list), ",") {
		return fmt.Errorf("listed node ids in order %q, want %q", got, list)
	}
	return nil
}

func (s *df2State) nodeNotListed(name string) error {
	if s.code != 200 {
		return fmt.Errorf("status %d: %s", s.code, s.body)
	}
	switch s.kind {
	case "market":
		for _, m := range s.market {
			if m["model"] == name {
				return fmt.Errorf("%q is listed on /market: %s", name, s.body)
			}
		}
	case "models":
		for _, e := range s.data {
			if e["id"] == name {
				return fmt.Errorf("%q is listed on /v1/models: %s", name, s.body)
			}
		}
	default:
		for _, n := range s.listedNames() {
			if n == name {
				return fmt.Errorf("%q is listed", name)
			}
		}
	}
	return nil
}

func (s *df2State) nodeListedOnline(name string, online string) error {
	for _, o := range s.offers {
		if s.nameOf(o["node_id"].(string)) == name {
			if fmt.Sprint(o["online"]) != online {
				return fmt.Errorf("%q online = %v, want %s", name, o["online"], online)
			}
			return nil
		}
	}
	return fmt.Errorf("%q is not listed: %v", name, s.listedNames())
}

func (s *df2State) marketRow(model string) (map[string]any, error) {
	for _, m := range s.market {
		if m["model"] == model {
			return m, nil
		}
	}
	return nil, fmt.Errorf("%q is not on /market (status %d): %s", model, s.code, s.body)
}

var df2PairRe = regexp.MustCompile(`([a-z_]+)\s+(\[[^\]]*\]|"[^"]*"|\S+)`)

// pairs parses "providers 4, curated_providers 1 and best_tps 120" style clauses.
func df2Pairs(clause string) [][2]string {
	clause = strings.ReplaceAll(clause, " and ", ", ")
	var out [][2]string
	for _, m := range df2PairRe.FindAllStringSubmatch(clause, -1) {
		out = append(out, [2]string{m[1], strings.TrimSuffix(m[2], ",")})
	}
	return out
}

func df2ValueEq(got any, want string) bool {
	want = strings.TrimSpace(want)
	switch g := got.(type) {
	case float64:
		w, err := strconv.ParseFloat(want, 64)
		return err == nil && g == w
	case bool:
		return strconv.FormatBool(g) == want
	case string:
		return strings.Trim(want, `"`) == g
	case nil:
		return false
	default:
		b, _ := json.Marshal(got)
		var w any
		if json.Unmarshal([]byte(want), &w) != nil {
			return false
		}
		wb, _ := json.Marshal(w)
		return string(b) == string(wb)
	}
}

func (s *df2State) marketShows(model, clause string) error {
	row, err := s.marketRow(model)
	if err != nil {
		return err
	}
	for _, kv := range df2Pairs(clause) {
		got := row[kv[0]]
		if _, present := row[kv[0]]; !present && kv[0] == "curated_providers" {
			// curated_providers is omitempty on the approved /market wire: absent reads 0
			got = 0.0
		}
		if !df2ValueEq(got, kv[1]) {
			return fmt.Errorf("/market %q %s = %v, want %s (row %v)", model, kv[0], row[kv[0]], kv[1], row)
		}
	}
	return nil
}

func (s *df2State) marketListsCapabilities(model, list string) error {
	row, err := s.marketRow(model)
	if err != nil {
		return err
	}
	if !df2ValueEq(row["capabilities"], list) {
		return fmt.Errorf("/market %q capabilities = %v, want %s", model, row["capabilities"], list)
	}
	return nil
}

func (s *df2State) exactlyOneRowFor(model string) error {
	if len(s.market) != 1 || s.market[0]["model"] != model {
		return fmt.Errorf("/market rows = %d, want exactly one for %q: %s", len(s.market), model, s.body)
	}
	return nil
}

// signalDiffersFromUnfiltered compares the filtered row with the broker's own aggregate over
// the SURVIVORS (computed directly, with the filtered-out curated station banned for the
// read), and with the unfiltered row served from the public read.
func (s *df2State) signalDiffersFromUnfiltered(model string) error {
	row, err := s.marketRow(model)
	if err != nil {
		return err
	}
	filtered := append([]map[string]any(nil), s.market...)
	s.df2FlushCache()
	if err := s.get("/market?model="+model, nil); err != nil {
		return err
	}
	unfiltered, err := s.marketRow(model)
	if err != nil {
		return err
	}
	s.market = filtered
	if fmt.Sprint(row["signal"]) == fmt.Sprint(unfiltered["signal"]) {
		return fmt.Errorf("the filtered %q row's signal %v equals the unfiltered row's: the row was copied, not recomputed over the survivors", model, row["signal"])
	}
	return nil
}

func (s *df2State) priceTierOverSurvivors() error {
	// Reference: the broker's own market with the curated station absent (banned for the
	// read), computed directly, is what "graded over the survivors" means.
	cur := s.idOf("n-cur")
	s.b.metricsMu.Lock()
	s.b.banned[cur] = true
	s.b.metricsMu.Unlock()
	var refFeed struct {
		Market []map[string]any `json:"market"`
	}
	refBytes, _ := json.Marshal(s.b.computeMarket())
	_ = json.Unmarshal(refBytes, &refFeed)
	ref := refFeed.Market
	s.b.metricsMu.Lock()
	delete(s.b.banned, cur)
	s.b.metricsMu.Unlock()
	row, err := s.marketRow("qwen3-32b")
	if err != nil {
		return err
	}
	for _, r := range ref {
		if r["model"] == "qwen3-32b" {
			if fmt.Sprint(r["price_tier"]) != fmt.Sprint(row["price_tier"]) || fmt.Sprint(r["signal"]) != fmt.Sprint(row["signal"]) {
				return fmt.Errorf("filtered row price_tier %v signal %v, want the survivors' aggregate price_tier %v signal %v", row["price_tier"], row["signal"], r["price_tier"], r["signal"])
			}
			return nil
		}
	}
	return fmt.Errorf("the reference aggregate has no qwen3-32b row")
}

// --- assertions: params_b on the views ------------------------------------------------------

func (s *df2State) offerOf(name string) (map[string]any, error) {
	for _, o := range s.offers {
		if s.nameOf(o["node_id"].(string)) == name {
			return o, nil
		}
	}
	return nil, fmt.Errorf("%q is not listed: %v", name, s.listedNames())
}

func (s *df2State) offerCarriesParams(name, params, estimated string) error {
	o, err := s.offerOf(name)
	if err != nil {
		return err
	}
	if _, ok := o["params_b"]; !ok {
		return fmt.Errorf("the %q offer carries no params_b (protocol.ModelOffer has no params_b field; offerView exposes none): %v", name, o)
	}
	if !df2ValueEq(o["params_b"], params) || !df2ValueEq(o["params_estimated"], estimated) {
		return fmt.Errorf("the %q offer carries params_b %v params_estimated %v, want %s / %s", name, o["params_b"], o["params_estimated"], params, estimated)
	}
	return nil
}

func (s *df2State) offerCarriesNoParams(name string) error {
	o, err := s.offerOf(name)
	if err != nil {
		return err
	}
	if _, ok := o["params_b"]; ok {
		return fmt.Errorf("the %q offer carries params_b %v, want none", name, o["params_b"])
	}
	if _, ok := o["params_estimated"]; ok {
		return fmt.Errorf("the %q offer carries params_estimated, want none", name)
	}
	return nil
}

func (s *df2State) bandOfferCarriesParams(params, estimated string) error {
	if s.code != 200 || len(s.offers) == 0 {
		return fmt.Errorf("the band resolved to %d with %d offers: %s", s.code, len(s.offers), s.body)
	}
	o := s.offers[0]
	if _, ok := o["params_b"]; !ok {
		return fmt.Errorf("the band's offer view carries no params_b (protocol.ModelOffer has no params_b field): %v", o)
	}
	if !df2ValueEq(o["params_b"], params) || !df2ValueEq(o["params_estimated"], estimated) {
		return fmt.Errorf("band offer params_b %v params_estimated %v, want %s / %s", o["params_b"], o["params_estimated"], params, estimated)
	}
	return nil
}

// --- assertions: cache, throttle, probes --------------------------------------------------

func (s *df2State) computeNotRerun() error {
	keys := s.cacheKeys("discover:")
	if len(keys) == 0 {
		return fmt.Errorf("no discover: cache entry exists after the read")
	}
	for _, k := range keys {
		before, had := s.keyTTL[k]
		if !had {
			return fmt.Errorf("the filtered read wrote a NEW cache entry %q (fragmenting the cache by filter) instead of reading the unfiltered one", k)
		}
		if after := s.mr.TTL(k); after > before {
			return fmt.Errorf("cache entry %q was re-set (TTL %s -> %s): computeDiscover re-ran", k, before, after)
		}
	}
	return nil
}

func (s *df2State) filterAppliedToCached() error {
	keys := s.cacheKeys("discover:")
	if len(keys) != 1 {
		return fmt.Errorf("expected the one unfiltered discover: entry, found %v", keys)
	}
	cached, _ := s.mr.Get(keys[0])
	var all struct {
		Offers []map[string]any `json:"offers"`
	}
	_ = json.Unmarshal([]byte(cached), &all)
	want := map[string]bool{}
	for _, o := range all.Offers {
		if o["model"] == "qwen3-32b" && o["region"] == "eu" {
			want[o["node_id"].(string)] = true
		}
	}
	got := map[string]bool{}
	for _, o := range s.offers {
		got[o["node_id"].(string)] = true
	}
	if len(want) == 0 || len(got) != len(want) {
		return fmt.Errorf("filtered offers %v, want the cached payload filtered to model+region %v", s.listedNames(), want)
	}
	for id := range want {
		if !got[id] {
			return fmt.Errorf("cached offer %s is missing from the filtered read", s.nameOf(id))
		}
	}
	return nil
}

func (s *df2State) computeRanAtMostOnce() error {
	if keys := s.cacheKeys("discover:"); len(keys) != 1 {
		return fmt.Errorf("computeDiscover ran %d times (one cache entry per compute): %v", len(keys), keys)
	}
	return nil
}

func (s *df2State) oneDiscoverEntry() error { return s.computeRanAtMostOnce() }

func (s *df2State) catalogNotRecomputed() error {
	keys := s.cacheKeys("models")
	if len(keys) == 0 {
		return fmt.Errorf("no models cache entry exists")
	}
	for _, k := range keys {
		before, had := s.keyTTL[k]
		if !had {
			return fmt.Errorf("the second read wrote a new cache entry %q", k)
		}
		if after := s.mr.TTL(k); after > before {
			return fmt.Errorf("cache entry %q was re-set (TTL %s -> %s): the catalog was recomputed", k, before, after)
		}
	}
	return nil
}

func (s *df2State) catalogComputedAtMostOnce() error {
	if keys := s.cacheKeys("models"); len(keys) != 1 {
		return fmt.Errorf("the catalog was computed %d times (one cache entry per compute): %v", len(keys), keys)
	}
	return nil
}

func (s *df2State) everyResponse200Filtered() error {
	for i, c := range s.codes {
		if c != 200 {
			return fmt.Errorf("response %d = %d: %s", i+1, c, s.bodies[i])
		}
		var out struct {
			Offers []map[string]any `json:"offers"`
		}
		if json.Unmarshal(s.bodies[i], &out) != nil || out.Offers == nil {
			return fmt.Errorf("response %d carries no offers array: %s", i+1, s.bodies[i])
		}
		for _, o := range out.Offers {
			if o["model"] != "qwen3-32b" {
				return fmt.Errorf("response %d is not filtered: offer for %v", i+1, o["model"])
			}
		}
	}
	return nil
}

func (s *df2State) everyResponse200() error {
	for i, c := range s.codes {
		if c != 200 {
			return fmt.Errorf("response %d = %d: %s", i+1, c, s.bodies[i])
		}
	}
	return nil
}

func (s *df2State) everyResponse200Data() error {
	for i, c := range s.codes {
		if c != 200 {
			return fmt.Errorf("response %d = %d: %s", i+1, c, s.bodies[i])
		}
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(s.bodies[i], &out) != nil || out.Data == nil {
			return fmt.Errorf("response %d carries no data array: %s", i+1, s.bodies[i])
		}
	}
	return nil
}

func (s *df2State) noneIs429() error {
	for i, c := range s.codes {
		if c == 429 {
			return fmt.Errorf("response %d is a 429", i+1)
		}
	}
	return nil
}

func (s *df2State) staleNodesScheduled() error {
	now := time.Now()
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	for name, st := range s.stations {
		ps := s.b.probeSched[st.id]
		if ps == nil || ps.nextDue.IsZero() || ps.nextDue.After(now) {
			due := "none"
			if ps != nil {
				due = ps.nextDue.String()
			}
			return fmt.Errorf("stale station %q (model %s) was not scheduled for a demand probe on the cache miss (nextDue %s); the hook must fire for every public offer, filtered out or not", name, st.model, due)
		}
	}
	return nil
}

// --- assertions: /v1/models ----------------------------------------------------------------

// ids are the MODEL entries' ids: class alias entries (@class/..., §14.B4) are not models.
func (s *df2State) ids() []string {
	var out []string
	for _, e := range s.data {
		id, _ := e["id"].(string)
		if strings.HasPrefix(id, classPrefix) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (s *df2State) dataIDsExactly(list string) error {
	if s.code != 200 {
		return fmt.Errorf("status %d: %s", s.code, s.body)
	}
	want := df2Split(list)
	got := append([]string(nil), s.ids()...)
	sw := append([]string(nil), want...)
	sort.Strings(sw)
	sort.Strings(got)
	if strings.Join(sw, ",") != strings.Join(got, ",") {
		return fmt.Errorf("data ids = %v, want exactly %v", s.ids(), want)
	}
	return nil
}

func (s *df2State) entry(id string) (map[string]any, error) {
	for _, e := range s.data {
		if e["id"] == id {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%q is not listed (status %d): %s", id, s.code, s.body)
}

func (s *df2State) rogerBlock(id string) (map[string]any, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	ra, _ := e["rogerai"].(map[string]any)
	if ra == nil {
		return nil, fmt.Errorf("%q carries no rogerai block: %v", id, e)
	}
	return ra, nil
}

func (s *df2State) blockReads(id string, table *godog.Table) error {
	ra, err := s.rogerBlock(id)
	if err != nil {
		return err
	}
	for _, row := range table.Rows[1:] {
		field, want := strings.TrimSpace(row.Cells[0].Value), strings.TrimSpace(row.Cells[1].Value)
		got, present := ra[field]
		if strings.HasPrefix(want, "(omitted") {
			if present {
				return fmt.Errorf("rogerai.%s = %v, want omitted", field, got)
			}
			continue
		}
		if !present {
			return fmt.Errorf("rogerai.%s is absent, want %s (block %v)", field, want, ra)
		}
		if !df2ValueEq(got, want) {
			return fmt.Errorf("rogerai.%s = %v, want %s", field, got, want)
		}
	}
	return nil
}

func (s *df2State) blockHas(id, clause string) error {
	ra, err := s.rogerBlock(id)
	if err != nil {
		return err
	}
	for _, kv := range df2Pairs(clause) {
		got, present := ra[kv[0]]
		if !present {
			return fmt.Errorf("rogerai.%s is absent on %q, want %s (block %v)", kv[0], id, kv[1], ra)
		}
		if !df2ValueEq(got, kv[1]) {
			return fmt.Errorf("rogerai.%s = %v on %q, want %s", kv[0], got, id, kv[1])
		}
	}
	return nil
}

func (s *df2State) blockHasNoKey(id, key string) error {
	ra, err := s.rogerBlock(id)
	if err != nil {
		return err
	}
	if v, ok := ra[key]; ok {
		return fmt.Errorf("rogerai.%s = %v on %q, want the key omitted", key, v, id)
	}
	return nil
}

func (s *df2State) modelListed(id string) error {
	_, err := s.entry(id)
	return err
}

func (s *df2State) listedWithBlock(id, clause string) error {
	if err := s.modelListed(id); err != nil {
		return err
	}
	return s.blockHas(id, strings.ReplaceAll(clause, "rogerai.", ""))
}

func (s *df2State) noLogContains(word string) error {
	if strings.Contains(s.logs.String(), word) {
		return fmt.Errorf("a broker log line contains %q", word)
	}
	return nil
}

func (s *df2State) idsEqualMarketSet() error {
	if len(s.bodies) != 3 {
		return fmt.Errorf("the three views were not read")
	}
	var models struct {
		Data []map[string]any `json:"data"`
	}
	var market struct {
		Market []map[string]any `json:"market"`
	}
	_ = json.Unmarshal(s.bodies[0], &models)
	_ = json.Unmarshal(s.bodies[1], &market)
	a, b := map[string]bool{}, map[string]bool{}
	for _, e := range models.Data {
		if id := e["id"].(string); !strings.HasPrefix(id, classPrefix) {
			a[id] = true
		}
	}
	for _, m := range market.Market {
		b[m["model"].(string)] = true
	}
	if fmt.Sprint(df2Keys(a)) != fmt.Sprint(df2Keys(b)) {
		return fmt.Errorf("/v1/models ids %v, /market models %v", df2Keys(a), df2Keys(b))
	}
	return nil
}

func (s *df2State) idsEqualOnlineDiscover() error {
	var models struct {
		Data []map[string]any `json:"data"`
	}
	var disc struct {
		Offers []map[string]any `json:"offers"`
	}
	_ = json.Unmarshal(s.bodies[0], &models)
	_ = json.Unmarshal(s.bodies[2], &disc)
	a, b := map[string]bool{}, map[string]bool{}
	for _, e := range models.Data {
		if id := e["id"].(string); !strings.HasPrefix(id, classPrefix) {
			a[id] = true
		}
	}
	for _, o := range disc.Offers {
		if on, _ := o["online"].(bool); on {
			b[o["model"].(string)] = true
		}
	}
	if fmt.Sprint(df2Keys(a)) != fmt.Sprint(df2Keys(b)) {
		return fmt.Errorf("/v1/models ids %v, ONLINE /discover models %v", df2Keys(a), df2Keys(b))
	}
	return nil
}

func df2Keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- suites -----------------------------------------------------------------------------

const df2Tags = "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice3"

func df2Register(sc *godog.ScenarioContext, st *df2State) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return ctx, st.reset()
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		st.teardownPins()
		return ctx, nil
	})
	// Background / fixtures
	sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
	sc.Step(`^nodes are on air for "([^"]*)":$`, st.nodesOnAirFor)
	sc.Step(`^nodes are on air:$`, st.nodesOnAir)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with quant "([^"]*)", params_b ([0-9.]+), tps ([0-9.]+), region "([^"]*)"$`, st.nodeOnAirQuantParamsTPSRegion)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no params_b$`, st.nodeOnAirNoParams)
	sc.Step(`^node "([^"]*)" is the only station for "([^"]*)" and declares no params_b$`, st.nodeOnlyStationNoParams)
	sc.Step(`^node "([^"]*)" is also on air for "([^"]*)" with params_b ([0-9.]+)$`, st.nodeAlsoOnAirParams)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with modality "([^"]*)"$`, st.nodeOnAirModality)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)"$`, st.nodeOnAir)
	sc.Step(`^"([^"]*)" has a schedule window that is FREE right now$`, st.freeWindowNow)
	sc.Step(`^\("([^"]*)", "([^"]*)"\) earned verified "tools"$`, st.earnedTools)
	sc.Step(`^\("([^"]*)", "([^"]*)"\) earned verified "tools" and declared "vision"$`, st.earnedToolsDeclaredVision)
	sc.Step(`^"([^"]*)" declared "vision" for "([^"]*)"$`, st.declaredVision)
	sc.Step(`^"([^"]*)" declared "tools" for "([^"]*)" and never passed a tool-call canary$`, st.declaredToolsNoCanary)
	sc.Step(`^node "([^"]*)" is banned$`, st.nodeBanned)
	sc.Step(`^"([^"]*)" is banned$`, st.nodeBanned)
	sc.Step(`^"([^"]*)" has not heartbeat within nodeTTL$`, st.nodeStale)
	sc.Step(`^"([^"]*)" is in a 429 cooldown for (\d+) s$`, st.nodeCooling)
	sc.Step(`^the only station serving "([^"]*)" is in an upstream-429 cooldown$`, st.onlyStationCooling)
	sc.Step(`^"([^"]*)" has failed a sustained probe streak on its poll host with no recent serving evidence$`, st.probeDeadOnPollHost)
	sc.Step(`^every node has left the air$`, st.everyoneLeft)
	sc.Step(`^every "([^"]*)" offer's ctx is estimated$`, st.everyCtxEstimated)
	sc.Step(`^node "([^"]*)" shares "([^"]*)" on a PRIVATE band with quant "([^"]*)"$`, st.privateBandQuant)
	sc.Step(`^node "([^"]*)" shares "([^"]*)" on a PRIVATE band with params_b ([0-9.]+)$`, st.privateBandParams)
	sc.Step(`^the cache is cold and probing is enabled$`, st.cacheColdProbingOn)
	sc.Step(`^/discover was computed and cached within publicMarketTTL$`, st.discoverCachedWithinTTL)
	sc.Step(`^/v1/models was computed within publicMarketTTL$`, st.modelsComputedWithinTTL)
	sc.Step(`^"([^"]*)" is a funded consumer$`, st.fundedConsumer)
	sc.Step(`^every station serving "([^"]*)" is priced above 0/0$`, st.allPaid)
	sc.Step(`^"([^"]*)" first came on air at unix ([0-9_]+)$`, st.firstOnAirAt)
	sc.Step(`^two broker instances "([^"]*)" and "([^"]*)" share the store$`, st.twoInstances)
	sc.Step(`^"([^"]*)" first came on air at unix ([0-9_]+) while "([^"]*)" was down$`, st.firstOnAirWhileDown)
	// Reads
	sc.Step(`^a consumer GETs (/[^ ]*) from the website origin$`, st.consumerGetsFromWebsite)
	sc.Step(`^a consumer GETs (/[^ ]*) with header "([^"]*)"$`, st.consumerGetsWithHeader)
	sc.Step(`^a consumer GETs (/[^ ]*) twice, (\d+) s apart$`, func(path string, _ int) error { return st.getsTwiceTenSecondsApart(path) })
	sc.Step(`^a consumer GETs (/[^ ]*) while a relay holds b\.mu$`, st.readWhileLocked)
	sc.Step(`^a consumer GETs /v1/models, /market and /discover in the same cache window$`, st.getsThreeViews)
	sc.Step(`^a consumer GETs (/[^ ]*)$`, st.consumerGets)
	sc.Step(`^another consumer GETs (/[^ ]*)$`, st.consumerGets)
	sc.Step(`^an anonymous consumer with no Authorization header GETs (/[^ ]*)$`, st.anonNoAuthGets)
	sc.Step(`^a browser sends OPTIONS (/[^ ]*)$`, st.browserOptions)
	sc.Step(`^a consumer sends ([A-Z]+) (/[^ ]*)$`, st.consumerSends)
	sc.Step(`^one IP GETs (/[^ ]*) (\d+) times in a second$`, st.oneIPBurstDiscover)
	sc.Step(`^(\d+) consumers GET (/[^ ]*) with (\d+) different filter combinations within publicMarketTTL$`, func(_ int, base string, n int) error { return st.hundredCombinations(base, n) })
	sc.Step(`^the band owner resolves the code$`, st.ownerResolvesCode)
	sc.Step(`^"([^"]*)" comes up and a consumer GETs /v1/models on "([^"]*)" and on "([^"]*)"$`, st.instanceComesUpBothGet)
	sc.Step(`^a funded consumer GETs /v1/models$`, st.fundedGetsModels)
	sc.Step(`^an anonymous consumer GETs /v1/models$`, st.anonGetsModels)
	sc.Step(`^"([^"]*)" GETs /v1/models and then POSTs /v1/chat/completions with each listed id whose stations are not all cooling$`, st.fundedListsThenRelaysEach)
	sc.Step(`^the consumer POSTs /v1/chat/completions for "([^"]*)"$`, st.consumerPosts)
	sc.Step(`^the anonymous consumer POSTs /v1/chat/completions for "([^"]*)"$`, st.anonPosts)
	// Status / envelope / headers
	sc.Step(`^the status is (\d+)$`, st.statusIs)
	sc.Step(`^the status is (\d+) and the id is "([^"]*)"$`, st.statusAndID)
	sc.Step(`^the error code is "([^"]*)"$`, st.errorCodeIs)
	sc.Step(`^the error message names "([^"]*)"$`, st.errorMessageNames)
	sc.Step(`^the error message says to log in to spend$`, st.errorSaysLogIn)
	sc.Step(`^the response carries a Retry-After$`, st.carriesRetryAfter)
	sc.Step(`^the body is (\{.*\})$`, st.bodyIs)
	sc.Step(`^the body is one model object with id "([^"]*)", object "([^"]*)", owned_by "([^"]*)" and a rogerai block$`, st.bodyIsOneModel)
	sc.Step(`^the response carries the same CORS headers an unfiltered /discover carries$`, st.sameCORSAsUnfiltered)
	sc.Step(`^the response carries Access-Control-Allow-Origin "([^"]*)", Allow-Methods "([^"]*)" and Allow-Headers "([^"]*)"$`, st.corsIdentical)
	sc.Step(`^the CORS headers are present$`, st.corsPresent)
	// Offers / market
	sc.Step(`^(\d+) offers are listed$`, st.nOffersListed)
	sc.Step(`^(\d+) offers are listed and every one is for "([^"]*)"$`, st.nOffersAllFor)
	sc.Step(`^every listed offer is for "([^"]*)"$`, st.everyOfferFor)
	sc.Step(`^they are ordered by price_in ascending$`, st.orderedByPriceIn)
	sc.Step(`^the listed node ids are exactly ?(.*)$`, st.listedExactly)
	sc.Step(`^"([^"]+)"'s last passed canary is older than the verified window$`, st.df2AgedVerified)
	sc.Step(`^the listed node ids are in order "([^"]*)"$`, st.listedInOrder)
	sc.Step(`^"([^"]*)" is not listed$`, st.nodeNotListed)
	sc.Step(`^"([^"]*)" is listed with online (true|false)$`, st.nodeListedOnline)
	sc.Step(`^"([^"]*)" shows (.+)$`, st.marketShows)
	sc.Step(`^"([^"]*)" lists capabilities (\[.*\])$`, st.marketListsCapabilities)
	sc.Step(`^exactly one row is listed, for "([^"]*)"$`, st.exactlyOneRowFor)
	sc.Step(`^the "([^"]*)" signal differs from the unfiltered row's signal$`, st.signalDiffersFromUnfiltered)
	sc.Step(`^its price_tier is graded over the surviving out-prices$`, st.priceTierOverSurvivors)
	sc.Step(`^the "([^"]*)" offer carries params_b ([0-9.]+) and params_estimated (true|false)$`, st.offerCarriesParams)
	sc.Step(`^the "([^"]*)" offer carries no params_b key and no params_estimated key$`, st.offerCarriesNoParams)
	sc.Step(`^the band's offer view carries params_b ([0-9.]+) and params_estimated (true|false)$`, st.bandOfferCarriesParams)
	// Cache / throttle / probes
	sc.Step(`^computeDiscover is not re-run$`, st.computeNotRerun)
	sc.Step(`^the filter is applied to the cached payload$`, st.filterAppliedToCached)
	sc.Step(`^computeDiscover ran at most once$`, st.computeRanAtMostOnce)
	sc.Step(`^the cache holds exactly one "discover:" entry$`, st.oneDiscoverEntry)
	sc.Step(`^the catalog is not recomputed$`, st.catalogNotRecomputed)
	sc.Step(`^the catalog was computed at most once$`, st.catalogComputedAtMostOnce)
	sc.Step(`^every response is 200 with a filtered offers array$`, st.everyResponse200Filtered)
	sc.Step(`^every response is 200 with a data array$`, st.everyResponse200Data)
	sc.Step(`^every response is 200$`, st.everyResponse200)
	sc.Step(`^none is a 429$`, st.noneIs429)
	sc.Step(`^the read returns without waiting on b\.mu$`, st.returnedWithoutWaiting)
	sc.Step(`^the stale nodes for every public offer are scheduled for a demand probe, filtered or not$`, st.staleNodesScheduled)
	// /v1/models
	sc.Step(`^the data ids are exactly ?(.*)$`, st.dataIDsExactly)
	sc.Step(`^the "([^"]*)" entry's rogerai block reads:$`, st.blockReads)
	sc.Step(`^the "([^"]*)" entry's rogerai block has no ([a-z_]+) key$`, st.blockHasNoKey)
	sc.Step(`^the "([^"]*)" entry's rogerai block has ([a-z_]+ (?:\[|"|[0-9tf]).*)$`, st.blockHas)
	sc.Step(`^its rogerai block has (.+)$`, func(clause string) error {
		if st.lastNamed == "" {
			return fmt.Errorf("\"its\" has no model: no previous step named one")
		}
		return st.blockHas(st.lastNamed, clause)
	})
	sc.Step(`^"([^"]*)" is listed$`, func(id string) error { st.lastNamed = id; return st.modelListed(id) })
	sc.Step(`^"([^"]*)" is listed with (rogerai\..+)$`, st.listedWithBlock)
	sc.Step(`^no log line contains "([^"]*)"$`, st.noLogContains)
	sc.Step(`^the /v1/models ids equal the /market model set$`, st.idsEqualMarketSet)
	sc.Step(`^equal the set of distinct model ids of ONLINE /discover offers$`, st.idsEqualOnlineDiscover)
	sc.Step(`^no request fails with "no node offers"$`, st.noNoNodeOffers)
	sc.Step(`^both reads show created ([0-9_]+) for "([^"]*)"$`, st.bothReadsCreated)
	sc.Step(`^both instances show created ([0-9_]+) for "([^"]*)"$`, st.bothInstancesCreated)
}

func TestDiscoveryFiltersBDD(t *testing.T) {
	st := &df2State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) { df2Register(sc, st) },
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/discovery/filters.feature"},
			Tags:     df2Tags,
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery/filters.feature failed (see godog output above)")
	}
}

// TestModelsEndpointBDD runs every models_endpoint.feature scenario the @slice0 runner does not
// (that runner's harness has no relay): together they execute the whole file.
func TestModelsEndpointBDD(t *testing.T) {
	st := &df2State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) { df2Register(sc, st) },
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/discovery/models_endpoint.feature"},
			Tags:     "~@slice0 && " + df2Tags,
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery/models_endpoint.feature (non-@slice0) failed (see godog output above)")
	}
}
