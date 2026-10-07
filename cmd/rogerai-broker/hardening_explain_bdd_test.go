package main

// hardening_explain_bdd_test.go makes features/routing/route_explain.feature EXECUTABLE (slice
// 6, contract §14.B3) against the REAL broker (sa6H, hardening_affinity_bdd_test.go): a dry run
// is posted through relay() (roger.dry_run) or through the broker's own mux
// (POST /v1/route/explain), and every "spent nothing" claim is read from the real store, the
// stations' upstream counters, the classifier's call count and the broker's price-lock table.
//
// The explain document is read as the contract states it: {request_id, models, plan[{model,
// station|tower, price_in, price_out, tier, reason}], excluded[{station, model, reasons}], hold,
// cost_estimate{min, max}, would{status, code, retry_after_s}}. Station fields carry broker ids;
// the runner maps them back to scenario names.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type rx6Doc struct {
	RequestID string   `json:"request_id"`
	Models    []string `json:"models"`
	Plan      []struct {
		Model    string   `json:"model"`
		Station  string   `json:"station"`
		Tower    string   `json:"tower"`
		PriceIn  *float64 `json:"price_in"`
		PriceOut *float64 `json:"price_out"`
		Tier     string   `json:"tier"`
		Reason   string   `json:"reason"`
	} `json:"plan"`
	Excluded []struct {
		Station     string   `json:"station"`
		Model       string   `json:"model"`
		Reasons     []string `json:"reasons"`
		RetryAfterS float64  `json:"retry_after_s"`
	} `json:"excluded"`
	Hold         *float64 `json:"hold"`
	CostEstimate *struct {
		Min float64 `json:"min"`
		Max float64 `json:"max"`
	} `json:"cost_estimate"`
	Would *struct {
		Status      int     `json:"status"`
		Code        string  `json:"code"`
		RetryAfterS float64 `json:"retry_after_s"`
	} `json:"would"`
}

type rx6State struct {
	*sa6H
	prevDoc  []byte
	dryHold  float64
	maxIn    int
	codes429 int
	// countersBefore are the traffic counters noted before a dry run (noteCounters).
	countersBefore map[string]int64
}

func (s *rx6State) doc() (*rx6Doc, error) {
	if s.lastCode != 200 {
		return nil, fmt.Errorf("status %d, want 200 with an explain document (%.300s)", s.lastCode, s.lastBody)
	}
	var d rx6Doc
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(s.lastBody, &raw); err != nil {
		return nil, fmt.Errorf("the body is not a JSON explain document: %.300s", s.lastBody)
	}
	for _, k := range []string{"plan", "excluded", "request_id"} {
		if _, ok := raw[k]; !ok {
			return nil, fmt.Errorf("the body has no %q: not an explain document (%.300s)", k, s.lastBody)
		}
	}
	if err := json.Unmarshal(s.lastBody, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *rx6State) name(d *rx6Doc, i int) string {
	p := d.Plan[i]
	if p.Tower != "" {
		for n, tw := range s.rsTowers {
			if tw.id == p.Tower {
				return n
			}
		}
		return p.Tower
	}
	return s.nameOfID(p.Station)
}

func (s *rx6State) planNames(d *rx6Doc) []string {
	var out []string
	for i := range d.Plan {
		out = append(out, s.name(d, i))
	}
	return out
}

func rx6List(list string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	return out
}

// --- When ---------------------------------------------------------------------------------------

func (s *rx6State) posts(user, model, with string) error {
	if user == "u-poor" || strings.HasPrefix(user, "u-") {
		s.as(user)
	}
	q, err := s.spec(user, model, " with "+with)
	if err != nil {
		return err
	}
	if err := s.send(q); err != nil {
		return err
	}
	if s.lastCode == 200 {
		s.prevDoc = s.lastBody
	}
	return nil
}

func (s *rx6State) postsAs(caller, model, with string) error {
	q, err := s.spec("", model, " with "+with)
	if err != nil {
		return err
	}
	q.caller = caller
	return s.send(q)
}

func (s *rx6State) grantPosts(model, with string) error { return s.postsAs("grant", model, with) }
func (s *rx6State) anonPosts(model, with string) error  { return s.postsAs("anon", model, with) }

func (s *rx6State) badSigPosts(model, with string) error {
	q, err := s.spec("", model, " with "+with)
	if err != nil {
		return err
	}
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	r := s.httpRequest(req, sent)
	r.Header.Set(protocol.HeaderSig, strings.Repeat("0", 128)) // a signature that does not verify
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	return nil
}

func (s *rx6State) explainEndpoint(user string) error {
	s.as(user)
	if s.prevDoc == nil {
		// "the same body": the scenario's dry run of "m", posted first so the explain endpoint's
		// document has the dry_run one to be compared with.
		if err := s.send(sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{}, roger: []sa6KV{{"dry_run", "true"}}}); err != nil {
			return err
		}
		s.prevDoc = append([]byte(nil), s.lastBody...)
	}
	q := s.lastSpec
	var r []sa6KV
	for _, kv := range q.roger {
		if kv.k != "dry_run" {
			r = append(r, kv)
		}
	}
	q.roger = r
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	hr := httptest.NewRequest(http.MethodPost, "/v1/route/explain", bytes.NewReader(sent))
	hr.Header.Set("Content-Type", "application/json")
	signReq(hr, s.consumerPriv, sent)
	rr := httptest.NewRecorder()
	s.b.routes().ServeHTTP(rr, hr)
	s.lastCode, s.lastBody, s.lastHdr = rr.Code, rr.Body.Bytes(), rr.Header()
	return nil
}

func (s *rx6State) sameAsDryRun() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	var prev rx6Doc
	if err := json.Unmarshal(s.prevDoc, &prev); err != nil || s.prevDoc == nil {
		return fmt.Errorf("no earlier dry-run document to compare with")
	}
	a, _ := json.Marshal([]any{d.Plan, d.Excluded})
	b, _ := json.Marshal([]any{prev.Plan, prev.Excluded})
	if !bytes.Equal(a, b) {
		return fmt.Errorf("plan/exclusions differ:\n explain %s\n dry_run %s", a, b)
	}
	return nil
}

func (s *rx6State) thenReal(user string) error {
	if r := s.shot.holdAmt; s.lastCode == 200 {
		_ = r
	}
	if d, err := s.doc(); err == nil && d.Hold != nil {
		s.dryHold = *d.Hold
	} else if err != nil {
		return err
	}
	q := s.lastSpec
	var r []sa6KV
	for _, kv := range q.roger {
		if kv.k != "dry_run" {
			r = append(r, kv)
		}
	}
	q.roger = r
	return s.send(q)
}

func (s *rx6State) concurrentDryRuns(user, n, model string) error {
	s.as(user)
	q := sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{}, roger: []sa6KV{{"dry_run", "true"}}}
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	stop := make(chan struct{})
	var maxIn int
	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.b.metricsMu.Lock()
			v := s.b.inflight[s.st("s1").id]
			s.b.metricsMu.Unlock()
			mu.Lock()
			if v > maxIn {
				maxIn = v
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	codes := make([]int, rs1i(n))
	for i := 0; i < rs1i(n); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
			s.b.relay(w, s.httpRequest(req, sent))
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	close(stop)
	mu.Lock()
	s.maxIn = maxIn
	mu.Unlock()
	for i, c := range codes {
		if c != 200 {
			return fmt.Errorf("dry run %d answered %d", i+1, c)
		}
	}
	return nil
}

func (s *rx6State) fastDryRuns(user string) error {
	s.as(user)
	q := sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{}, roger: []sa6KV{{"dry_run", "true"}}}
	s.codes429 = 0
	for i := 0; i < 200; i++ {
		if err := s.send(q); err != nil {
			return err
		}
		if s.lastCode == 429 {
			s.codes429++
			if code, _ := s.errBody(); code != "rate_limited" || s.lastHdr.Get("Retry-After") == "" {
				return fmt.Errorf("a 429 without code rate_limited and a Retry-After: %.200s", s.lastBody)
			}
		}
	}
	return nil
}

// --- Given ---------------------------------------------------------------------------------------

func (s *rx6State) stationFull(name, model, in, out, tps, quant, region string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.setTPS(st.id, rs1f(tps))
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Quant = quant })
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Region = region
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *rx6State) curatedStationFull(name, model, in, out, tps, quant, region string) error {
	if err := s.stationFull(name, model, in, out, tps, quant, region); err != nil {
		return err
	}
	s.b.mu.Lock()
	reg := s.b.nodes[s.st(name).id]
	reg.Curated = true
	s.b.nodes[reg.NodeID] = reg
	s.b.mu.Unlock()
	return nil
}

// rx6TrafficCounters are the counters only real traffic may move (a dry run is not traffic):
// every routing counter except the dry-run counts themselves.
func (s *rx6State) rx6TrafficCounters() map[string]int64 {
	out := map[string]int64{}
	for k, v := range s.b.stats.routingCounters() {
		if !strings.HasPrefix(k, "route_explain") && !strings.HasPrefix(k, "dry_run") {
			out[k] = v
		}
	}
	return out
}

func (s *rx6State) noteCounters() error { s.countersBefore = s.rx6TrafficCounters(); return nil }

func (s *rx6State) noCounterMoved() error {
	after := s.rx6TrafficCounters()
	for k, v := range after {
		if v != s.countersBefore[k] {
			return fmt.Errorf("the dry run moved %s from %d to %d", k, s.countersBefore[k], v)
		}
	}
	return nil
}

func (s *rx6State) syncMod() error { s.b.mod.mode = modeSync; return nil }

func (s *rx6State) capacityOne(name string) error {
	st := s.st(name)
	s.b.metricsMu.Lock()
	s.b.concurrentTPS[st.id] = tpsPerSlot
	s.b.inflight[st.id] = 0
	s.b.metricsMu.Unlock()
	return nil
}

func (s *rx6State) towerAt(name, model, out string) error {
	_, err := s.tower(name, model, rs1f(out), 0)
	return err
}

func (s *rx6State) towerPlain(name, model string) error {
	_, err := s.tower(name, model, 1, 0)
	return err
}

func (s *rx6State) threeCooling(a, b, c, ta, tb, tc string) error {
	for i, n := range []string{a, b, c} {
		if err := s.coolNode(n, rs1i([]string{ta, tb, tc}[i])); err != nil {
			return err
		}
	}
	return nil
}

func (s *rx6State) declaresCtx(name, ctx string) error {
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = rs1i(ctx), false })
	return nil
}

func (s *rx6State) poorBalance(user, bal string) error { return s.newUser(user, rs1f(bal)) }

func (s *rx6State) monthlyCap(user string) error {
	s.as(user)
	return s.db.SetMonthlyCap(s.wallet, 1e-9)
}

func (s *rx6State) grantOnlyModel(_, model string) error {
	return s.mint(s.st("s1"), []string{model}, 0)
}

func (s *rx6State) bandHidden(band, name, model string) error {
	_, err := s.band(band, band+"-code", name, model, nil)
	return err
}

func (s *rx6State) bandCode(band, code, name, model string) error {
	_, err := s.band(band, code, name, model, nil)
	return err
}

func (s *rx6State) freeGrantOf(_, name string) error {
	return s.mint(s.st(name), []string{s.st(name).model}, 0)
}

func (s *rx6State) pairCooling(name, user string) error {
	if _, ok := s.users[user]; !ok {
		if err := s.newUser(user, 10); err != nil {
			return err
		}
	}
	return s.pairCool(name, user)
}

// --- Then ---------------------------------------------------------------------------------------

func (s *rx6State) isExplain() error { _, err := s.doc(); return err }

func (s *rx6State) noHold() error {
	if s.shot.holdN != 0 {
		return fmt.Errorf("%d hold(s) placed", s.shot.holdN)
	}
	return nil
}

func (s *rx6State) classifierNotCalled() error {
	if s.shot.modN != 0 {
		return fmt.Errorf("the classifier was called %d time(s)", s.shot.modN)
	}
	return nil
}

func (s *rx6State) noLock(_, model string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for k := range s.b.quotes {
		if strings.HasSuffix(k, "|"+model) {
			return fmt.Errorf("a price lock %q exists", k)
		}
	}
	return nil
}

func (s *rx6State) noReceipt() error {
	id := s.lastHdr.Get("X-RogerAI-Request-Id")
	if id == "" {
		return fmt.Errorf("the dry run carries no X-RogerAI-Request-Id")
	}
	for _, k := range []string{id, id + "-1"} {
		if _, _, err := s.storedReceipt(k); err == nil {
			return fmt.Errorf("a receipt exists for %s", k)
		}
	}
	return nil
}

func (s *rx6State) generation404() error {
	id := s.lastHdr.Get("X-RogerAI-Request-Id")
	if id == "" {
		return fmt.Errorf("the dry run carries no X-RogerAI-Request-Id")
	}
	if code, body, _ := s.get(s.b, "/generation?id="+id); code != 404 {
		return fmt.Errorf("GET /generation = %d %.200s, want 404", code, body)
	}
	return s.isExplain()
}

func (s *rx6State) inflightStayed0(string) error {
	if s.maxIn != 0 {
		return fmt.Errorf("in_flight reached %d during the dry runs", s.maxIn)
	}
	return nil
}

func (s *rx6State) planIs(list, model string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	want := rx6List(list)
	got := s.planNames(d)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("plan %v, want %v", got, want)
	}
	for _, p := range d.Plan {
		if model != "" && p.Model != model {
			return fmt.Errorf("plan entry model %q, want %q", p.Model, model)
		}
	}
	return nil
}

func (s *rx6State) planIsAny(list string) error { return s.planIs(list, "") }

func (s *rx6State) planEmpty() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) != 0 {
		return fmt.Errorf("plan %v, want empty", s.planNames(d))
	}
	return nil
}

func (s *rx6State) eachEntry(reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) == 0 {
		return fmt.Errorf("empty plan")
	}
	for i, p := range d.Plan {
		if p.PriceIn == nil || p.PriceOut == nil || p.Tier == "" || p.Reason != reason {
			return fmt.Errorf("plan entry %d (%s) = %+v, want prices, tier and reason %q", i, s.name(d, i), p, reason)
		}
	}
	return nil
}

func (s *rx6State) headWithReason(name, reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) == 0 || s.name(d, 0) != name || d.Plan[0].Reason != reason {
		return fmt.Errorf("plan %v, want head %q with reason %q", d.Plan, name, reason)
	}
	return nil
}

func (s *rx6State) followsWith(name, reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) < 2 || s.name(d, 1) != name || d.Plan[1].Reason != reason {
		return fmt.Errorf("plan %v, want %q second with reason %q", d.Plan, name, reason)
	}
	return nil
}

func (s *rx6State) headReason(reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) == 0 || d.Plan[0].Reason != reason {
		return fmt.Errorf("plan %v, want the head's reason %q", d.Plan, reason)
	}
	return nil
}

func (s *rx6State) headTower(name string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	tw, ok := s.rsTowers[name]
	if !ok {
		return fmt.Errorf("no Tower %q", name)
	}
	if len(d.Plan) == 0 || d.Plan[0].Tower != tw.id {
		return fmt.Errorf("plan %v, want Tower %q (%s) at the head", d.Plan, name, tw.id)
	}
	return nil
}

func (s *rx6State) effectiveModels(list string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if want := rx6List(list); strings.Join(d.Models, ",") != strings.Join(want, ",") {
		return fmt.Errorf("models %v, want %v", d.Models, want)
	}
	return nil
}

func (s *rx6State) precedes(a, b string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	lastA, firstB := -1, len(d.Plan)
	for i, p := range d.Plan {
		if p.Model == a {
			lastA = i
		}
		if p.Model == b && i < firstB {
			firstB = i
		}
	}
	if lastA < 0 || firstB == len(d.Plan) || lastA > firstB {
		return fmt.Errorf("plan %v: every %q entry must precede every %q entry", d.Plan, a, b)
	}
	return nil
}

func (s *rx6State) exactlyEntries(n string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if len(d.Plan) != rs1i(n) {
		return fmt.Errorf("plan has %d entries, want %s", len(d.Plan), n)
	}
	return nil
}

func (s *rx6State) noDupes() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range d.Plan {
		k := d.Plan[i].Model + "|" + s.name(d, i)
		if seen[k] {
			return fmt.Errorf("%s appears twice in the plan", k)
		}
		seen[k] = true
	}
	return nil
}

func (s *rx6State) reasonsOf(d *rx6Doc, name string) ([]string, float64, bool) {
	for _, e := range d.Excluded {
		if s.nameOfID(e.Station) == name {
			return e.Reasons, e.RetryAfterS, true
		}
	}
	return nil, 0, false
}

func (s *rx6State) excludedWith(name, reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	r, _, ok := s.reasonsOf(d, name)
	if !ok {
		return fmt.Errorf("%q is not excluded (excluded %+v)", name, d.Excluded)
	}
	for _, x := range r {
		if x == reason {
			return nil
		}
	}
	return fmt.Errorf("%q excluded for %v, want %q", name, r, reason)
}

func (s *rx6State) excludedNotInPlan() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	in := map[string]bool{}
	for i := range d.Plan {
		in[s.name(d, i)] = true
	}
	for _, e := range d.Excluded {
		if in[s.nameOfID(e.Station)] {
			return fmt.Errorf("%q is both excluded and in the plan", s.nameOfID(e.Station))
		}
	}
	if len(d.Excluded) == 0 {
		return fmt.Errorf("nothing excluded")
	}
	return nil
}

func (s *rx6State) excludedTwo(name, a, b string) error {
	if err := s.excludedWith(name, a); err != nil {
		return err
	}
	return s.excludedWith(name, b)
}

func (s *rx6State) bothExcluded(a, b, reason string) error {
	if err := s.excludedWith(a, reason); err != nil {
		return err
	}
	return s.excludedWith(b, reason)
}

func (s *rx6State) excludedCooling(name, secs string) error {
	if err := s.excludedWith(name, "cooling"); err != nil {
		return err
	}
	d, _ := s.doc()
	_, ra, _ := s.reasonsOf(d, name)
	if math.Abs(ra-rs1f(secs)) > 3 {
		return fmt.Errorf("retry_after_s %v, want about %s", ra, secs)
	}
	return nil
}

func (s *rx6State) vocabulary() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	allowed := map[string]bool{"cooling": true, "ignore": true, "only": true, "context_window": true}
	for _, f := range []string{"quantizations", "params_b", "min_ctx", "min_tps", "max_ttft_ms", "trust_min", "region", "self_hosted_only", "max_price_in", "max_price_out", "confidential"} {
		allowed[f] = true
	}
	if len(d.Excluded) == 0 {
		return fmt.Errorf("nothing excluded under roger.region [\"ap\"]")
	}
	for _, e := range d.Excluded {
		for _, r := range e.Reasons {
			if !allowed[r] && !strings.HasPrefix(r, "capability ") {
				return fmt.Errorf("exclusion reason %q is outside the filter vocabulary", r)
			}
		}
	}
	return nil
}

func (s *rx6State) holdEqualsReal() error {
	if s.lastCode != 200 {
		return fmt.Errorf("the real request answered %d", s.lastCode)
	}
	if math.Abs(s.dryHold-s.shot.holdAmt) > 1e-9 {
		return fmt.Errorf("dry-run hold %v, real hold %v", s.dryHold, s.shot.holdAmt)
	}
	return nil
}

func (s *rx6State) costMin(n string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if d.CostEstimate == nil || len(d.Plan) == 0 || d.Plan[0].PriceIn == nil {
		return fmt.Errorf("no cost_estimate or plan head price")
	}
	want := rs1f(n) * *d.Plan[0].PriceIn / 1e6
	if math.Abs(d.CostEstimate.Min-want) > want*0.05+1e-9 {
		return fmt.Errorf("cost_estimate.min %v, want about %v", d.CostEstimate.Min, want)
	}
	return nil
}

func (s *rx6State) costMaxHold() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if d.CostEstimate == nil || d.Hold == nil || math.Abs(d.CostEstimate.Max-*d.Hold) > 1e-12 {
		return fmt.Errorf("cost_estimate %+v, hold %v", d.CostEstimate, d.Hold)
	}
	return nil
}

func (s *rx6State) would(status, code string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	if d.Would == nil || fmt.Sprint(d.Would.Status) != status || d.Would.Code != code {
		return fmt.Errorf("would %+v, want %s %s", d.Would, status, code)
	}
	return nil
}

func (s *rx6State) wouldCooling(secs string) error {
	if err := s.would("503", "band_cooling"); err != nil {
		return err
	}
	d, _ := s.doc()
	if math.Abs(d.Would.RetryAfterS-rs1f(secs)) > 2 {
		return fmt.Errorf("would.retry_after_s %v, want %s", d.Would.RetryAfterS, secs)
	}
	return nil
}

func (s *rx6State) servedReal() error {
	if err := s.statusIs("200"); err != nil {
		return err
	}
	if s.lastHdr.Get("X-RogerAI-Provider") == "" {
		return fmt.Errorf("no station served (%.200s)", s.lastBody)
	}
	return nil
}

func (s *rx6State) jsonCT() error {
	if ct := s.lastHdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("Content-Type %q", ct)
	}
	return nil
}

func (s *rx6State) appearsNeither(names ...string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	for _, n := range names {
		id := s.id(n)
		raw := string(s.lastBody)
		if strings.Contains(raw, id) {
			return fmt.Errorf("%q (%s) appears in the explain document", n, id)
		}
	}
	_ = d
	return nil
}

func (s *rx6State) uniform(msg string) error {
	if err := s.statusIs("503"); err != nil {
		return err
	}
	return s.errorMessageIs(msg)
}

func (s *rx6State) onDiscover() error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	code, body, _ := s.get(s.b, "/discover")
	if code != 200 {
		return fmt.Errorf("/discover = %d", code)
	}
	for i := range d.Plan {
		if id := d.Plan[i].Station; id != "" && !bytes.Contains(body, []byte(id)) {
			return fmt.Errorf("plan station %s is not on /discover", id)
		}
	}
	for _, e := range d.Excluded {
		if !bytes.Contains(body, []byte(e.Station)) {
			return fmt.Errorf("excluded station %s is not on /discover", e.Station)
		}
	}
	return nil
}

func (s *rx6State) noTowerID() error {
	if _, err := s.doc(); err != nil {
		return err
	}
	for n, tw := range s.rsTowers {
		if strings.Contains(string(s.lastBody), tw.id) {
			return fmt.Errorf("Tower %q's id appears in the response", n)
		}
	}
	return nil
}

func (s *rx6State) onlyContains(name string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	for _, n := range s.planNames(d) {
		if n != name {
			return fmt.Errorf("plan %v, want only %q", s.planNames(d), name)
		}
	}
	if len(d.Plan) == 0 {
		return fmt.Errorf("empty plan")
	}
	return nil
}

func (s *rx6State) notExcludedFor(name, reason string) error {
	d, err := s.doc()
	if err != nil {
		return err
	}
	r, _, _ := s.reasonsOf(d, name)
	for _, x := range r {
		if x == reason {
			return fmt.Errorf("%q excluded for %q", name, reason)
		}
	}
	return nil
}

func (s *rx6State) some429() error {
	if s.codes429 == 0 {
		return fmt.Errorf("200 dry runs in a row, none rate-limited")
	}
	return nil
}

func (s *rx6State) idPrefix(p string) error {
	if id := s.lastHdr.Get("X-RogerAI-Request-Id"); !strings.HasPrefix(id, p) {
		return fmt.Errorf("X-RogerAI-Request-Id %q, want prefix %q", id, p)
	}
	return nil
}

func (s *rx6State) bodyLacks(str string) error {
	if _, err := s.doc(); err != nil {
		return err
	}
	if strings.Contains(string(s.lastBody), str) {
		return fmt.Errorf("the response body contains %q", str)
	}
	return nil
}

func (s *rx6State) noPersonal() error {
	if _, err := s.doc(); err != nil {
		return err
	}
	raw := string(s.lastBody)
	if regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`).MatchString(raw) {
		return fmt.Errorf("the document carries an IP address")
	}
	for _, st := range s.stations {
		if strings.Contains(raw, st.acct) {
			return fmt.Errorf("the document carries an owner id")
		}
	}
	if strings.Contains(raw, s.wallet) || strings.Contains(raw, "u_gh_") {
		return fmt.Errorf("the document carries a wallet id")
	}
	return nil
}

func TestRouteExplainBDD(t *testing.T) {
	sa6Run(t, "../../features/routing/route_explain.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &rx6State{sa6H: h}
		sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M, Tier-A, tps (\d+), quant "([^"]+)", region "([^"]+)"$`, s.stationFull)
		sc.Step(`^station "([^"]+)" is a curated station on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M, Tier-A, tps (\d+), quant "([^"]+)", region "([^"]+)"$`, s.curatedStationFull)
		sc.Step(`^"([^"]+)" posts a chat completion for "([^"]+)" with ((?s).+)$`, s.posts)
		sc.Step(`^"([^"]+)" posts the same body to /v1/route/explain$`, s.explainEndpoint)
		sc.Step(`^"([^"]+)" then sends the same request without dry_run$`, s.thenReal)
		sc.Step(`^the routing counters are noted$`, s.noteCounters)
		sc.Step(`^no routing or affinity counter moved$`, s.noCounterMoved)
		sc.Step(`^"([^"]+)" posts (\d+) dry runs for "([^"]+)" concurrently$`, s.concurrentDryRuns)
		sc.Step(`^"([^"]+)" posts dry runs faster than the /market rate limit$`, s.fastDryRuns)
		sc.Step(`^the grant holder posts a chat completion for "([^"]+)" with (.+)$`, s.grantPosts)
		sc.Step(`^an anonymous caller posts a chat completion for "([^"]+)" with (.+)$`, s.anonPosts)
		sc.Step(`^an invalidly signed caller posts a chat completion for "([^"]+)" with (.+)$`, s.badSigPosts)
		sc.Step(`^sync moderation is enabled$`, s.syncMod)
		sc.Step(`^"([^"]+)" has capacity 1 and in_flight 0$`, s.capacityOne)
		sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)" at out \$([0-9.]+) per 1M$`, s.towerAt)
		sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)"$`, s.towerPlain)
		sc.Step(`^"([^"]+)" earned verified "tools" for "([^"]+)"$`, s.rs1EarnedTools)
		sc.Step(`^"([^"]+)", "([^"]+)" and "([^"]+)" are cooling for (\d+), (\d+) and (\d+) more seconds$`, s.threeCooling)
		sc.Step(`^"([^"]+)" declares a context window of (\d+)$`, s.declaresCtx)
		sc.Step(`^"([^"]+)" has a balance of \$([0-9.]+)$`, s.poorBalance)
		sc.Step(`^"([^"]+)" has a monthly cap that this request's hold would exceed$`, s.monthlyCap)
		sc.Step(`^a grant "([^"]+)" that allows only model "([^"]+)"$`, s.grantOnlyModel)
		sc.Step(`^a private band "([^"]+)" whose only station is "([^"]+)" on air for "([^"]+)"$`, s.bandHidden)
		sc.Step(`^a private band "([^"]+)" with code "([^"]+)" whose only station is "([^"]+)" on air for "([^"]+)"$`, s.bandCode)
		sc.Step(`^a free grant "([^"]+)" owned by the operator of "([^"]+)"$`, s.freeGrantOf)
		sc.Step(`^the pair \("([^"]+)", "([^"]+)"\) is cooling$`, s.pairCooling)
		sc.Step(`^"([^"]+)" has exhausted its relay rate bucket$`, func(u string) error { s.as(u); return s.rateLimitExhausted(u) })
		sc.Step(`^the response is 200 with an explain document$`, s.isExplain)
		sc.Step(`^the body is an explain document$`, s.isExplain)
		sc.Step(`^no hold was placed$`, s.noHold)
		sc.Step(`^X-RogerAI-Dry-Run is "([^"]+)"$`, func(v string) error { return s.headerIs("X-RogerAI-Dry-Run", v) })
		sc.Step(`^the response is 200 with an explain document equal in plan and exclusions to the dry_run one$`, s.sameAsDryRun)
		sc.Step(`^the moderation classifier was not called$`, s.classifierNotCalled)
		sc.Step(`^no price lock exists for \("([^"]+)", any station, "([^"]+)"\)$`, s.noLock)
		sc.Step(`^no receipt exists for the dry-run request id$`, s.noReceipt)
		sc.Step(`^GET /generation for the dry-run request id is 404$`, s.generation404)
		sc.Step(`^"([^"]+)" in_flight stayed 0 throughout$`, s.inflightStayed0)
		sc.Step(`^the plan is \[(.*)\] for model "([^"]+)"$`, s.planIs)
		sc.Step(`^the plan is \[(.*)\]$`, s.planIsAny)
		sc.Step(`^the plan is empty$`, s.planEmpty)
		sc.Step(`^each plan entry names its in and out price, its tier and reason "([^"]+)"$`, s.eachEntry)
		sc.Step(`^the plan head is "([^"]+)" with reason "([^"]+)"$`, s.headWithReason)
		sc.Step(`^"([^"]+)" follows with reason "([^"]+)"$`, s.followsWith)
		sc.Step(`^the plan head has reason "([^"]+)"$`, s.headReason)
		sc.Step(`^the plan head is tower "([^"]+)"$`, s.headTower)
		sc.Step(`^the effective models are \[(.*)\]$`, s.effectiveModels)
		sc.Step(`^every "([^"]+)" entry precedes every "([^"]+)" entry in the plan$`, s.precedes)
		sc.Step(`^the plan has exactly (\d+) entry$`, s.exactlyEntries)
		sc.Step(`^no station appears twice in the plan$`, s.noDupes)
		sc.Step(`^"([^"]+)" is excluded with reason "([^"]+)"$`, s.excludedWith)
		sc.Step(`^the excluded station does not appear in the plan$`, s.excludedNotInPlan)
		sc.Step(`^"([^"]+)" is excluded with reasons "([^"]+)" and "([^"]+)"$`, s.excludedTwo)
		sc.Step(`^"([^"]+)" and "([^"]+)" are excluded with reason "([^"]+)"$`, s.bothExcluded)
		sc.Step(`^"([^"]+)" is excluded with reason "cooling" and retry_after_s about (\d+)$`, s.excludedCooling)
		sc.Step(`^every exclusion reason is one of the names noMatchFilters can produce, plus cooling, ignore, only, context_window and capability <name>$`, s.vocabulary)
		sc.Step(`^the dry run's hold equals the hold the real request placed$`, s.holdEqualsReal)
		sc.Step(`^cost_estimate\.min is the (\d+) prompt tokens at the plan head's in price$`, s.costMin)
		sc.Step(`^cost_estimate\.max equals the hold$`, s.costMaxHold)
		sc.Step(`^would\.status is (\d+) with would\.code "([^"]+)"$`, s.would)
		sc.Step(`^would\.status is 503 with would\.code "band_cooling" and would\.retry_after_s (\d+)$`, s.wouldCooling)
		sc.Step(`^the response is 200 served by a station \(a real request\)$`, s.servedReal)
		sc.Step(`^the response Content-Type is application/json$`, s.jsonCT)
		sc.Step(`^"([^"]+)" appears neither in the plan nor in excluded$`, func(n string) error { return s.appearsNeither(n) })
		sc.Step(`^"([^"]+)" and "([^"]+)" appear neither in the plan nor in excluded$`, func(a, b string) error { return s.appearsNeither(a, b) })
		sc.Step(`^the response is the uniform "([^"]+)" refusal$`, s.uniform)
		sc.Step(`^every station named in the plan or excluded is listed on /discover$`, s.onDiscover)
		sc.Step(`^no Tower relay id appears in the response$`, s.noTowerID)
		sc.Step(`^the plan contains only "([^"]+)"$`, s.onlyContains)
		sc.Step(`^"([^"]+)" is not excluded for "([^"]+)"$`, s.notExcludedFor)
		sc.Step(`^some responses are 429 with error code "rate_limited" and a Retry-After$`, s.some429)
		sc.Step(`^X-RogerAI-Request-Id starts with "([^"]+)"$`, s.idPrefix)
		sc.Step(`^the response body does not contain "([^"]+)"$`, s.bodyLacks)
		sc.Step(`^no entry carries an IP address, owner id or wallet id$`, s.noPersonal)
	})
}

var _ = sort.Strings
