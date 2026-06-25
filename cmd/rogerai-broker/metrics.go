package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// This file is the per-model METRICS views: what the caller's account SERVES as a
// provider (/metrics/provider) and what it CONSUMES (/metrics/usage), each broken
// down per model with a free-vs-paid split over a trailing `days` window. Both are
// login-required and return ONLY the caller's own data, accepting EITHER a logged-in
// web session cookie OR a signed Ed25519 request (the same dual-auth as the payout /
// account endpoints). Aggregation is receipt-derived in the store (a GROUP BY in
// Postgres; an iterate in Mem), so the numbers never drift from the earnings/spend
// they roll up.

const (
	metricsDefaultDays = 30  // the default trailing window
	metricsMaxDays     = 366 // sane cap on the window so the scan stays bounded
)

// metricsDays reads + clamps the `days` query param to [1, metricsMaxDays], defaulting
// to metricsDefaultDays when absent or unparseable.
func metricsDays(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || n <= 0 {
		return metricsDefaultDays
	}
	if n > metricsMaxDays {
		return metricsMaxDays
	}
	return n
}

// metricsProvider handles GET /metrics/provider?days=30: the caller's PROVIDER
// per-model breakdown - for each (model, node) the caller's node(s) served, the
// request + token counts, a free-vs-paid split, and the owner's earnings (the 70%
// net share), plus summed totals and the period. Account-scoped (the owner pubkey),
// so it accepts a web session OR a signed CLI request (see payoutOwner). An owner
// with no operator account / no served traffic gets empty rows + zero totals, plus an
// explicit "is_provider" flag so the UI can distinguish "you have no nodes yet" (a
// not-yet-provider) from "your nodes had no traffic this period" instead of spinning on
// an ambiguous empty body.
func (b *broker) metricsProvider(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	// Account identity (owner pubkey), the SAME dual-auth as payouts: a logged-in web
	// session OR a signed Ed25519 request bound to a non-anonymized GitHub owner.
	_, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	days := metricsDays(r)
	var rows []store.ProviderModelMetric
	// is_provider is an EXPLICIT honesty signal for the UI: true once the account has
	// ever bound a node (it is an operator, even if this window is empty), false for a
	// logged-in consumer who has never registered a node. Without it the web client
	// can't tell "no nodes yet" from "nodes but no traffic", so it spins on the empty
	// body forever. Default false; flipped true below when a node binding exists.
	isProvider := false
	// A logged-in identity that is not (yet) a bound operator account has no served
	// traffic - return an empty, well-formed body rather than a 403.
	if o.Pubkey != "" {
		if nodes, err := b.db.NodesOfAccount(o.Pubkey); err == nil && len(nodes) > 0 {
			isProvider = true
		}
		since, until := metricsWindowUTC(time.Now(), days)
		rows, _ = b.db.ProviderMetrics(o.Pubkey, since, until)
		// Served traffic this window also proves provider-hood (covers a legacy node
		// whose binding row is gone but whose receipts remain).
		if len(rows) > 0 {
			isProvider = true
		}
	}
	if rows == nil {
		rows = []store.ProviderModelMetric{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":      rows,
		"totals":      providerTotals(rows),
		"period_days": days,
		"is_provider": isProvider,
	})
}

// metricsUsage handles GET /metrics/usage?days=30: the caller's CONSUMER per-model
// breakdown - for each model the caller used, the request + token counts, a
// free-vs-paid split, and total spend, plus summed totals and the period. Wallet-
// scoped, so it accepts a web session OR a signed request (see dashIdentity). Login is
// REQUIRED (own data only): an anonymous / unbound keypair has no wallet (free models
// + grant keys only) and is rejected 401, like the payout endpoints.
func (b *broker) metricsUsage(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	user, ok := b.dashIdentity(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	// Own-data-only: a plain unsigned request resolves to a legacy/anon id, and an
	// unbound signed keypair to its own pubkey-derived id - neither owns a wallet, so
	// reject rather than report a bogus empty body (mirrors the payout 401).
	if !walletLoggedIn(user) {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to read your usage")
		return
	}
	days := metricsDays(r)
	since, until := metricsWindowUTC(time.Now(), days)
	rows, _ := b.db.UsageMetrics(user, since, until)
	if rows == nil {
		rows = []store.UsageModelMetric{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_in":   true,
		"models":      rows,
		"totals":      usageTotals(rows),
		"period_days": days,
	})
}

// metricsWindowUTC returns the trailing [since,until) unix window for `days` ending at
// now (UTC). The store treats the window as half-open [since,until), so until is set to
// now+1s: a receipt written in the SAME second as the query still lands inside the
// window (it would be dropped by an exclusive `ts < now`), while `since` (now-days*24h,
// inclusive) is the lower edge a just-older row falls below.
func metricsWindowUTC(now time.Time, days int) (since, until int64) {
	until = now.UTC().Unix() + 1
	since = now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	return since, until
}

// providerTotals sums the per-model provider rows into one totals object.
func providerTotals(rows []store.ProviderModelMetric) map[string]any {
	var requests, tokensIn, tokensOut, freeReq, paidReq, freeTok, paidTok int64
	var earnings float64
	for _, r := range rows {
		requests += r.Requests
		tokensIn += r.TokensIn
		tokensOut += r.TokensOut
		freeReq += r.FreeRequests
		paidReq += r.PaidRequests
		freeTok += r.FreeTokens
		paidTok += r.PaidTokens
		earnings += r.EarningsUSD
	}
	return map[string]any{
		"requests":      requests,
		"tokens_in":     tokensIn,
		"tokens_out":    tokensOut,
		"free_requests": freeReq,
		"paid_requests": paidReq,
		"free_tokens":   freeTok,
		"paid_tokens":   paidTok,
		"earnings_usd":  round6(earnings),
	}
}

// usageTotals sums the per-model usage rows into one totals object.
func usageTotals(rows []store.UsageModelMetric) map[string]any {
	var requests, tokensIn, tokensOut, freeReq, paidReq int64
	var spend float64
	for _, r := range rows {
		requests += r.Requests
		tokensIn += r.TokensIn
		tokensOut += r.TokensOut
		freeReq += r.FreeRequests
		paidReq += r.PaidRequests
		spend += r.SpendUSD
	}
	return map[string]any{
		"requests":      requests,
		"tokens_in":     tokensIn,
		"tokens_out":    tokensOut,
		"free_requests": freeReq,
		"paid_requests": paidReq,
		"spend_usd":     round6(spend),
	}
}
