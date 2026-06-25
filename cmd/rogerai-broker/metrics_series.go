package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// This file adds the TIME-DIMENSION account feeds the redesigned web pages need:
//
//   - GET /metrics/series?days=N - a per-day (and short hourly) time-series of the
//     authed identity's tokens / requests / spend (consumer) AND earned (provider),
//     broken down per model, plus a savings-vs-frontier rollup. The existing
//     /metrics/{provider,usage} are point-in-time per-model breakdowns with no time
//     axis; this is what the Dashboard + Metrics CHARTS render over time.
//
//   - GET /console - the recent lineage activity a "console" page shows: the last N
//     requests with their receipt (model, node callsign, tokens, cost, request id,
//     timestamp), plus live counters (requests today, active nodes/bands, spend or
//     earned today). Owner sees node-serving activity; consumer sees consumption.
//
// Both are read-only, derived from existing receipts + the ledger (no new tracking),
// and authed to the CALLING owner/consumer with the SAME dual-auth (web session OR
// signed Ed25519) as /earnings + /metrics - never another account's data.

// frontierRef is one reference frontier-lab model's PUBLIC list price, used ONLY to
// estimate what the caller's consumed tokens WOULD have cost at a name-brand lab. It
// is a hard-coded reference estimate (NOT a live quote) so the savings number is
// honest + stable. Prices are USD per 1,000,000 tokens (input/output), the standard
// published unit. Update deliberately; clearly labeled as an estimate in the response.
type frontierRef struct {
	Model    string  `json:"model"`
	InPer1M  float64 `json:"in_per_1m"`
	OutPer1M float64 `json:"out_per_1m"`
}

// frontierTable is the small static reference set the savings estimate compares
// against. These are public list prices (USD / 1M tokens) for a handful of widely
// known frontier models, captured as a REFERENCE ESTIMATE - not a live or contractual
// quote. The "default" baseline used for the headline savings number is the median-ish
// mid-tier model (gpt-4o); the per-model table is returned so the web can show the
// spread. Keep this list short and in ONE place.
var frontierTable = []frontierRef{
	{Model: "gpt-4o", InPer1M: 2.50, OutPer1M: 10.00},
	{Model: "claude-sonnet", InPer1M: 3.00, OutPer1M: 15.00},
	{Model: "gpt-4o-mini", InPer1M: 0.15, OutPer1M: 0.60},
	{Model: "claude-haiku", InPer1M: 0.80, OutPer1M: 4.00},
}

// frontierBaseline is the model in frontierTable whose price drives the HEADLINE
// savings figure (the others are returned for context/spread).
const frontierBaseline = "gpt-4o"

// frontierCost returns what `in`/`out` tokens would cost at the named reference
// model's list price (USD), or at the baseline when model is "".
func frontierCost(in, out int64, model string) float64 {
	if model == "" {
		model = frontierBaseline
	}
	for _, f := range frontierTable {
		if f.Model == model {
			return (float64(in)*f.InPer1M + float64(out)*f.OutPer1M) / 1e6
		}
	}
	return 0
}

// seriesPoint is one time bucket (a UTC day, or an hour for the recent-hours series).
// It carries BOTH consumer ($ spend) and provider ($ earned) figures so one series
// serves a user who both consumes and operates nodes; a pure consumer sees earned=0
// and a pure operator sees spend=0.
type seriesPoint struct {
	Bucket    string       `json:"bucket"` // "2006-01-02" (day) or "2006-01-02T15" (hour, UTC)
	Requests  int64        `json:"requests"`
	TokensIn  int64        `json:"tokens_in"`
	TokensOut int64        `json:"tokens_out"`
	Spend     float64      `json:"spend"`            // consumer $ paid in the bucket
	Earned    float64      `json:"earned"`           // provider $ owner-share in the bucket
	Frontier  float64      `json:"frontier_est"`     // est. cost at the baseline frontier model
	Savings   float64      `json:"savings_est"`      // frontier_est - spend (>=0 floor)
	Models    []modelPoint `json:"models,omitempty"` // per-model split within the bucket
}

// modelPoint is one model's slice of a time bucket.
type modelPoint struct {
	Model     string  `json:"model"`
	Requests  int64   `json:"requests"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	Spend     float64 `json:"spend"`
	Earned    float64 `json:"earned"`
}

// bucketAgg accumulates one bucket's totals + a nested per-model map.
type bucketAgg struct {
	requests  int64
	tokensIn  int64
	tokensOut int64
	spend     float64
	earned    float64
	frontier  float64
	byModel   map[string]*modelPoint
}

func newBucketAgg() *bucketAgg { return &bucketAgg{byModel: map[string]*modelPoint{}} }

// mergedEntry is one receipt with its consumer/provider sides reconciled. When the
// caller BOTH consumed and served a request (self-serve: their wallet AND their node),
// it is ONE request, not two - so requests/tokens are counted once while spend (the
// consumer side) and earned (the provider side) are both attributed. spendSide marks
// that the caller consumed this receipt (so frontier savings are estimated on it);
// earnSide marks that the caller served it.
type mergedEntry struct {
	store.Entry
	spendSide bool
	earnSide  bool
}

// fold adds one merged receipt to a bucket (and its per-model slice). Requests/tokens
// count once; spend + frontier come from the consumer side, earned from the provider
// side. A self-served receipt contributes to both spend and earned but counts as one
// request.
func (a *bucketAgg) fold(m mergedEntry) {
	e := m.Entry
	a.requests++
	a.tokensIn += int64(e.PromptTokens)
	a.tokensOut += int64(e.CompletionTokens)
	mk := e.Model
	if mk == "" {
		mk = "unknown"
	}
	mp := a.byModel[mk]
	if mp == nil {
		mp = &modelPoint{Model: mk}
		a.byModel[mk] = mp
	}
	mp.Requests++
	mp.TokensIn += int64(e.PromptTokens)
	mp.TokensOut += int64(e.CompletionTokens)
	if m.spendSide {
		a.spend += e.Cost
		a.frontier += frontierCost(int64(e.PromptTokens), int64(e.CompletionTokens), "")
		mp.Spend += e.Cost
	}
	if m.earnSide {
		a.earned += e.OwnerShare
		mp.Earned += e.OwnerShare
	}
}

// metricsSeries handles GET /metrics/series?days=N: the per-day (+ recent hourly)
// time-series for the authed identity, with a per-model split and a savings-vs-frontier
// rollup. Dual-auth (web session OR signed Ed25519). It serves whichever sides the
// caller has: a consumer wallet -> spend/savings; a bound operator account -> earned.
// A logged-in identity with neither side (no wallet, no operator account) is 401.
func (b *broker) metricsSeries(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)

	// Resolve BOTH possible identities the same way the existing feeds do: the
	// wallet (consumer side, dashIdentity) and the operator account (provider side,
	// payoutOwner). A pure consumer has a wallet but no operator account; a pure
	// operator the reverse; many users have both (one github-scoped identity).
	wallet, walletOK := b.dashIdentity(r)
	consumer := walletOK && walletLoggedIn(wallet)
	_, owner, ownerOK := b.payoutOwner(r, nil)
	provider := ownerOK && owner.Pubkey != "" && owner.GitHubID != 0

	if !consumer && !provider {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to view your metrics")
		return
	}

	now := time.Now()
	days := metricsDays(r)

	// Per-AUTHED-IDENTITY hot-path cache (flag-gated). This feed reads + aggregates the
	// caller's receipts/ledger per request; a 10-30s window collapses repeated loads.
	// SECURITY: the key is namespaced by BOTH resolved, AUTHENTICATED identities (the
	// wallet AND the operator pubkey) plus the window, so one account's cached series can
	// NEVER be served to another - the response depends on exactly these inputs. The
	// identities come from dashIdentity/payoutOwner (verified), not from spoofable input.
	// Flag OFF => direct compute (zero behavior change).
	key := identityCacheKey("series", wallet, consumer, owner.Pubkey, provider) + "|d=" + strconv.Itoa(days)
	b.serveCachedJSON(w, key, authedFeedTTL, func() any {
		return b.computeMetricsSeries(now, days, wallet, consumer, owner.Pubkey, provider)
	})
}

// computeMetricsSeries builds the /metrics/series payload for the resolved identities.
// READ-ONLY: it reads receipts/ledger rows and aggregates them; it never mutates money
// state, so its serialized result is safe to cache for the short authed window. The
// caller has already authenticated wallet/owner; this only consumes those ids.
func (b *broker) computeMetricsSeries(now time.Time, days int, wallet string, consumer bool, ownerPubkey string, provider bool) any {
	since, until := metricsWindowUTC(now, days)

	var consEntries, provEntries []store.Entry
	if consumer {
		consEntries, _ = b.db.EntriesByUser(wallet, since, until)
	}
	if provider {
		provEntries, _ = b.db.EntriesByAccount(ownerPubkey, since, until)
	}

	// Per-day series. Buckets are keyed by UTC day; both sides fold into the same map
	// so one timeline carries spend AND earned.
	dayKey := func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }
	days24Key := func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02T15") }

	daySeries := buildSeries(consEntries, provEntries, dayKey)

	// Short hourly series for the last 48h (cheap: a re-bucket of the same rows in a
	// tighter window). This is what a "last 24-48h" sparkline renders.
	h48Since := now.UTC().Add(-48 * time.Hour).Unix()
	hourCons := windowEntries(consEntries, h48Since)
	hourProv := windowEntries(provEntries, h48Since)
	hourSeries := buildSeries(hourCons, hourProv, days24Key)

	// Savings rollup (consumer side only - a provider does not "save", they earn).
	// Computed from the raw consumer entries (NOT by re-summing the rounded per-bucket
	// figures) so the headline totals carry no per-bucket rounding drift. The savings
	// total is floored at 0 (a heavy/cheap RogerAI use never shows "negative savings").
	var totSpend, totFrontier float64
	for _, e := range consEntries {
		totSpend += e.Cost
		totFrontier += frontierCost(int64(e.PromptTokens), int64(e.CompletionTokens), "")
	}
	totSavings := totFrontier - totSpend
	if totSavings < 0 {
		totSavings = 0
	}
	totSpend = round6(totSpend)
	totFrontier = round6(totFrontier)
	totSavings = round6(totSavings)

	return map[string]any{
		"period_days": days,
		"is_consumer": consumer,
		"is_provider": provider,
		"daily":       daySeries,
		"hourly":      hourSeries, // last 48h, UTC hour buckets
		"savings": map[string]any{
			"baseline_model": frontierBaseline,
			"spend_usd":      totSpend,
			"frontier_est":   totFrontier, // est. cost at the baseline frontier list price
			"savings_est":    totSavings,  // frontier_est - spend (floored at 0 per bucket)
			"reference":      frontierTable,
			"reference_note": "Estimate only: published list prices, not a live or contractual quote.",
		},
	}
}

// identityCacheKey builds the per-AUTHED-IDENTITY cache key for an own-data feed. It
// folds in BOTH the wallet (consumer side) and the operator pubkey (provider side) -
// each only when that side is actually present/authenticated - so the key uniquely
// identifies WHOSE data this is. A caller who is both consumer and provider gets a key
// distinct from a pure consumer or pure provider with the same wallet/pubkey, because
// the response itself differs. This is the cross-user isolation guarantee: two
// different identities can NEVER collide on one key, so one account's cached bytes are
// never returned to another.
func identityCacheKey(feed, wallet string, consumer bool, ownerPubkey string, provider bool) string {
	w, o := "", ""
	if consumer {
		w = wallet
	}
	if provider {
		o = ownerPubkey
	}
	return feed + ":w=" + w + "|o=" + o
}

// windowEntries filters newest-first entries to those at/after `since`.
func windowEntries(es []store.Entry, since int64) []store.Entry {
	var out []store.Entry
	for _, e := range es {
		if e.TS >= since {
			out = append(out, e)
		}
	}
	return out
}

// mergeEntries reconciles the consumer-side + provider-side entries into one set keyed
// by RequestID. A receipt the caller BOTH consumed and served (self-serve) is ONE
// merged entry with both spendSide + earnSide set, so it counts as a single request
// while still attributing its spend AND its earned. A receipt seen on only one side
// keeps that single side.
func mergeEntries(cons, prov []store.Entry) []mergedEntry {
	idx := map[string]int{}
	out := make([]mergedEntry, 0, len(cons)+len(prov))
	add := func(e store.Entry, spend, earn bool) {
		if e.RequestID != "" {
			if i, ok := idx[e.RequestID]; ok {
				out[i].spendSide = out[i].spendSide || spend
				out[i].earnSide = out[i].earnSide || earn
				return
			}
			idx[e.RequestID] = len(out)
		}
		out = append(out, mergedEntry{Entry: e, spendSide: spend, earnSide: earn})
	}
	for _, e := range cons {
		add(e, true, false)
	}
	for _, e := range prov {
		add(e, false, true)
	}
	return out
}

// buildSeries reconciles the consumer + provider entries (a self-served receipt
// appears in BOTH but is ONE request), folds them into time buckets keyed by keyFn(ts),
// then returns them sorted oldest -> newest (chart order) with a per-model split and the
// per-bucket savings estimate.
func buildSeries(cons, prov []store.Entry, keyFn func(int64) string) []seriesPoint {
	merged := mergeEntries(cons, prov)
	aggs := map[string]*bucketAgg{}
	get := func(k string) *bucketAgg {
		a := aggs[k]
		if a == nil {
			a = newBucketAgg()
			aggs[k] = a
		}
		return a
	}
	for _, m := range merged {
		get(keyFn(m.TS)).fold(m)
	}
	out := make([]seriesPoint, 0, len(aggs))
	for k, a := range aggs {
		sav := a.frontier - a.spend
		if sav < 0 {
			sav = 0
		}
		models := make([]modelPoint, 0, len(a.byModel))
		for _, mp := range a.byModel {
			mp.Spend = round6(mp.Spend)
			mp.Earned = round6(mp.Earned)
			models = append(models, *mp)
		}
		sort.SliceStable(models, func(i, j int) bool { return models[i].Model < models[j].Model })
		out = append(out, seriesPoint{
			Bucket: k, Requests: a.requests, TokensIn: a.tokensIn, TokensOut: a.tokensOut,
			Spend: round6(a.spend), Earned: round6(a.earned),
			Frontier: round6(a.frontier), Savings: round6(sav), Models: models,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket }) // oldest first (chart order)
	return out
}

// consoleEvent is one lineage row a console page renders: a receipt projected to the
// fields a "live feed" needs (the node callsign is the bare node id - already
// hostname-free in this system). success is always true here: only SETTLED receipts
// reach the store, so a present receipt is a successful request.
type consoleEvent struct {
	RequestID string  `json:"request_id"` // the receipt / chain id
	TS        int64   `json:"ts"`
	Model     string  `json:"model"`
	Node      string  `json:"node"` // node callsign (hostname-free node id)
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	Cost      float64 `json:"cost"`   // consumer $ paid
	Earned    float64 `json:"earned"` // provider owner-share $ (0 on the consumer view)
	Success   bool    `json:"success"`
}

// console handles GET /console (alias /activity): the recent lineage activity feed +
// live counters. Dual-auth, own-data only. An OWNER (bound operator account) sees the
// activity their NODES served (earned per row, active-nodes counter); a CONSUMER sees
// their CONSUMPTION (cost per row, spend-today counter). A caller who is both is shown
// the provider view (their node-serving console) since that is the operator-facing
// "console" page; the consumer feed is /me + /usage. Honest empty state: no fabricated
// rows, real receipts only.
func (b *broker) console(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)

	wallet, walletOK := b.dashIdentity(r)
	consumer := walletOK && walletLoggedIn(wallet)
	_, owner, ownerOK := b.payoutOwner(r, nil)
	provider := ownerOK && owner.Pubkey != "" && owner.GitHubID != 0

	if !consumer && !provider {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to view your console")
		return
	}

	now := time.Now()
	limit := recentLimit(r)

	// Per-AUTHED-IDENTITY hot-path cache (flag-gated). SECURITY: keyed by BOTH resolved,
	// AUTHENTICATED identities (wallet + operator pubkey) plus the row limit, so one
	// account's cached console is NEVER served to another. Flag OFF => direct compute.
	key := identityCacheKey("console", wallet, consumer, owner.Pubkey, provider) + "|n=" + strconv.Itoa(limit)
	b.serveCachedJSON(w, key, authedFeedTTL, func() any {
		return b.computeConsole(now, limit, wallet, consumer, owner, provider)
	})
}

// computeConsole builds the /console payload (recent lineage feed + live counters) for
// the resolved identities. READ-ONLY over receipts/ledger, so its serialized result is
// safe to cache for the short authed window.
func (b *broker) computeConsole(now time.Time, limit int, wallet string, consumer bool, owner store.Owner, provider bool) any {
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	dayUntil := now.UTC().Unix() + 1

	role := "consumer"
	var recent []store.Entry
	var today []store.Entry
	if provider {
		role = "owner"
		recent, _ = entriesForOwner(b, owner.Pubkey, limit)
		today, _ = b.db.EntriesByAccount(owner.Pubkey, dayStart, dayUntil)
	} else {
		recent, _ = b.db.RecentByUser(wallet, limit)
		today, _ = b.db.EntriesByUser(wallet, dayStart, dayUntil)
	}

	events := make([]consoleEvent, 0, len(recent))
	for _, e := range recent {
		events = append(events, consoleEvent{
			RequestID: e.RequestID, TS: e.TS, Model: e.Model, Node: e.Node,
			TokensIn: int64(e.PromptTokens), TokensOut: int64(e.CompletionTokens),
			Cost: round6(e.Cost), Earned: round6(e.OwnerShare), Success: true,
		})
	}

	// Live counters from today's receipts (and, for an owner, the active node set).
	var reqToday int64
	var spendToday, earnedToday float64
	activeNodes := map[string]bool{}
	for _, e := range today {
		reqToday++
		spendToday += e.Cost
		earnedToday += e.OwnerShare
		if e.Node != "" {
			activeNodes[e.Node] = true
		}
	}

	counters := map[string]any{
		"requests_today": reqToday,
	}
	if provider {
		counters["earned_today"] = round6(earnedToday)
		counters["active_nodes"] = len(activeNodes)
		// active bands: the owner's live (non-revoked, non-expired) private bands.
		if n, err := b.db.CountActiveBands(owner.Pubkey, now); err == nil {
			counters["active_bands"] = n
		}
	} else {
		counters["spend_today"] = round6(spendToday)
	}

	return map[string]any{
		"role":     role, // "owner" | "consumer"
		"events":   events,
		"counters": counters,
	}
}

// entriesForOwner returns the most-recent receipts served by ALL nodes bound to the
// operator account, newest first, capped at limit. It merges the per-node recents (the
// store keys recents by user|node) so the owner console spans every node they run.
func entriesForOwner(b *broker, accountID string, limit int) ([]store.Entry, error) {
	nodes, err := b.db.NodesOfAccount(accountID)
	if err != nil {
		return nil, err
	}
	var all []store.Entry
	for _, n := range nodes {
		rows, e := b.db.RecentByNode(n, limit)
		if e != nil {
			return nil, e
		}
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS > all[j].TS })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
