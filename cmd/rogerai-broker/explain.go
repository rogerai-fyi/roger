package main

// explain.go: route explain (contract §14.B3, features/routing/route_explain.feature). A dry run
// (`roger.dry_run: true`, or POST /v1/route/explain with the same body) goes through the real
// relay - the same validation, visibility and planning - and stops where the hold would be
// placed: no moderation, hold, key reserve, price lock, receipt, /generation record, dispatch or
// capacity use. The answer is a 200 explain document; a money or supply refusal the request
// would earn is reported inside it as `would`, never as an error.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// dryWouldCodes are the refusals a dry run reports inside its document (`would`); every other
// refusal (validation, authentication, a band code that does not resolve) answers as it would
// for a real request.
var dryWouldCodes = map[string]bool{"no_match": true, "band_cooling": true, "insufficient_balance": true,
	"monthly_cap_reached": true, "key_limit_reached": true, "request_exceeds_station_tpm": true, "grant_unavailable": true,
	"login_required": true}

type explainPlanEntry struct {
	Model    string  `json:"model"`
	Station  string  `json:"station,omitempty"`
	Tower    string  `json:"tower,omitempty"`
	PriceIn  float64 `json:"price_in"`
	PriceOut float64 `json:"price_out"`
	Tier     string  `json:"tier"`
	Reason   string  `json:"reason"`
}

type explainExcluded struct {
	Station     string   `json:"station"`
	Model       string   `json:"model"`
	Reasons     []string `json:"reasons"`
	RetryAfterS int      `json:"retry_after_s,omitempty"`
}

type explainWould struct {
	Status      int    `json:"status"`
	Code        string `json:"code"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
}

type explainDoc struct {
	RequestID    string             `json:"request_id"`
	Models       []string           `json:"models"`
	Plan         []explainPlanEntry `json:"plan"`
	Excluded     []explainExcluded  `json:"excluded"`
	Hold         float64            `json:"hold"`
	CostEstimate map[string]float64 `json:"cost_estimate"`
	Would        *explainWould      `json:"would,omitempty"`
}

// dryWriter holds a dry run's response until the relay returns: the explain document when the
// relay reached the plan, else the refusal - reported as `would` when it is one dryWouldCodes
// names, passed through unchanged otherwise.
type dryWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	doc    *explainDoc        // set when the relay reached the plan
	base   func() *explainDoc // the document so far (models, exclusions), for a `would`
	inPlan map[string]bool    // node\x00model pairs the plan holds (never listed as excluded)
	header http.Header        // the held response's headers
}

func (w *dryWriter) Header() http.Header { return w.header }

func (w *dryWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *dryWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.buf.Write(p)
}

func (w *dryWriter) Flush() {}

// finish writes what the dry run answers.
func (w *dryWriter) finish() {
	out := w.ResponseWriter
	for k, v := range w.header {
		if w.doc == nil || strings.HasPrefix(k, "X-Rogerai-") || k == "Access-Control-Allow-Origin" || k == "Access-Control-Allow-Credentials" || k == "Vary" {
			out.Header()[k] = v
		}
	}
	out.Header().Set("X-RogerAI-Cost", "0")
	out.Header().Set("X-RogerAI-Dry-Run", "true")
	doc := w.doc
	if doc == nil && w.status >= 400 {
		var env struct {
			Error struct {
				Code     string         `json:"code"`
				Metadata map[string]any `json:"metadata"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.buf.Bytes(), &env)
		if dryWouldCodes[env.Error.Code] {
			doc = &explainDoc{}
			if w.base != nil {
				doc = w.base()
			}
			doc.Plan = []explainPlanEntry{}
			ra, _ := env.Error.Metadata["retry_after_s"].(float64)
			doc.Would = &explainWould{Status: w.status, Code: env.Error.Code, RetryAfterS: int(ra)}
			out.Header().Del("Retry-After")
		}
	}
	if doc == nil {
		out.WriteHeader(w.status)
		_, _ = out.Write(w.buf.Bytes())
		return
	}
	out.Header().Set("Content-Type", "application/json")
	out.Header().Del("Content-Length")
	raw, _ := json.Marshal(doc)
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// dryLimiter is the dry runs' own bucket (§14.B3: not the relay bucket), at public-read limits
// (ROGERAI_DRYRUN_RATE_RPM / _BURST, default 60 / 30).
func (b *broker) dryLimiter() *rateLimiter {
	b.dryOnce.Do(func() {
		b.dryRL = &rateLimiter{buckets: map[string]*tokenBucket{},
			rpm: envFloat("ROGERAI_DRYRUN_RATE_RPM", 60), burst: envFloat("ROGERAI_DRYRUN_RATE_BURST", 30)}
	})
	return b.dryRL
}

// dryRunOf reports roger.dry_run: true on a decoded body (validation of the value is the
// routing parser's; this only decides the mode early, before any rate token).
func dryRunOf(d reqDoc) bool {
	var roger map[string]json.RawMessage
	if d.field("roger", &roger) != nil {
		return false
	}
	v, ok := routingBool(roger["dry_run"])
	return ok && v
}

// explainFilter is what a dry run's exclusions are judged against.
type explainFilter struct {
	ignore, only     []string
	onlySet          bool
	maxIn, maxOut    float64
	minTPS           float64
	visible          func(id string) bool
	req              pickReq
	confidentialOnly bool
}

// explainExclusions lists every on-air station the caller can see that offers one of models and
// that this request turns away, with every reason that applies (the noMatchFilters vocabulary
// plus cooling, ignore, only, context_window, max_price_in/out and capability <name>).
func (b *broker) explainExclusions(models []string, f explainFilter, inPlan map[string]bool) []explainExcluded {
	want := map[string]bool{}
	for _, m := range models {
		want[m] = true
	}
	now := b.now()
	out := []explainExcluded{}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL || b.banned[n.NodeID] || !f.visible(n.NodeID) {
			continue
		}
		for _, o := range n.Offers {
			if !want[o.Model] || offerModality(o.Modality) != "chat" || inPlan[n.NodeID+"\x00"+o.Model] {
				continue
			}
			reasons, retry := b.offerReasonsLocked(n, o, f, now)
			if len(reasons) > 0 {
				out = append(out, explainExcluded{Station: n.NodeID, Model: o.Model, Reasons: reasons, RetryAfterS: retry})
			}
		}
	}
	slices.SortFunc(out, func(a, c explainExcluded) int { return strings.Compare(a.Station+a.Model, c.Station+c.Model) })
	return out
}

func (b *broker) offerReasonsLocked(n protocol.NodeRegistration, o protocol.ModelOffer, f explainFilter, now time.Time) ([]string, int) {
	var r []string
	retry := 0
	add := func(s string) { r = append(r, s) }
	if slices.Contains(f.ignore, n.NodeID) {
		add("ignore")
	}
	if f.onlySet && !slices.Contains(f.only, n.NodeID) {
		add("only")
	}
	until, cooling := b.coolingUntilLocked(n.NodeID, now)
	if pu, ok := pairUntil(f.req.pairCool, n.NodeID, o.Model); ok && now.Before(pu) && (!cooling || pu.After(until)) {
		until, cooling = pu, true
	}
	if cooling {
		add("cooling")
		retry = max(1, int(until.Sub(now).Seconds()+0.5))
	}
	req := f.req
	if req.quants != nil && !req.quants[strings.ToLower(o.Quant)] && !(o.Quant == "" && req.quants[protocol.QuantUnknown]) {
		add("quantizations")
	}
	for _, nf := range []netFilters{{paramsLo: req.paramsLo, paramsHi: req.paramsHi}, {minCtx: req.minCtx}} {
		if nf.any() {
			if why := b.netRejectLocked(nf, n, o, true, now); why != "" {
				add(why)
			}
		}
	}
	if tps := b.tps[n.NodeID]; f.minTPS > 0 && tps > 0 && tps < f.minTPS {
		add("min_tps")
	}
	for _, nf := range []netFilters{{maxTTFT: req.maxTTFT}, {trustVerified: req.trustVerified}} {
		if nf.any() {
			if why := b.netRejectLocked(nf, n, o, true, now); why != "" {
				add(why)
			}
		}
	}
	if req.selfHostedOnly && n.Curated {
		add("self_hosted_only")
	}
	if req.regions != nil {
		if why := b.netRejectLocked(netFilters{regions: req.regions}, n, o, true, now); why != "" {
			add(why)
		}
	}
	in, out, _, _ := o.ActivePrice(now)
	if f.maxIn > 0 && in > f.maxIn {
		add("max_price_in")
	}
	if f.maxOut > 0 && out > f.maxOut {
		add("max_price_out")
	}
	if req.promptTokens > 0 && o.Ctx > 0 && !o.CtxEstimated && req.promptTokens > o.Ctx {
		add("context_window")
	}
	if req.needTools && !b.toolsVerifiedForLocked(n.NodeID, o.Model) {
		add("capability tools")
	}
	if req.needVision && !slices.Contains(o.Capabilities, protocol.CapVision) {
		add("capability vision")
	}
	if f.confidentialOnly && !b.confidential[n.NodeID] {
		add("confidential")
	}
	return r, retry
}

// explainTier is a plan entry's health tier ("A" or "B"; a Tower row carries its own).
func (b *broker) explainTier(c attemptCand) string {
	if c.edge != nil {
		return c.edge.tier
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	tq := b.trust[c.node.NodeID]
	sr, seen := b.success[c.node.NodeID]
	if tierAHealthy(tq.probeFails, sr, seen) {
		return "A"
	}
	return "B"
}

// dryHold is the hold the real request would place (the hold path's own rule, read-only): the
// priciest pair the balance and the monthly cap cover, else the head's own cost; would names the
// 402 the request would earn instead.
func (b *broker) dryHold(payer string, plan []attemptCand, now time.Time) (float64, *explainWould) {
	head := plan[0].maxCost
	if head <= 0 {
		return 0, nil
	}
	if !b.monthlyCapFits(payer, head, now) {
		return head, &explainWould{Status: http.StatusPaymentRequired, Code: "monthly_cap_reached"}
	}
	bal, err := b.db.PeekBalance(payer)
	if err != nil {
		return head, nil
	}
	for _, c := range planCeilings(plan) {
		if b.monthlyCapFits(payer, c, now) && bal+1e-12 >= c {
			return c, nil
		}
	}
	if bal+1e-12 < head {
		return head, &explainWould{Status: http.StatusPaymentRequired, Code: "insufficient_balance"}
	}
	return head, nil
}
