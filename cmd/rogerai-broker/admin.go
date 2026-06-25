package main

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// admin.go is the SUPER-ADMIN (founder) ops surface: platform-wide health, marketplace,
// revenue, payouts, abuse, and activity rollups, plus the existing admin-unhold control.
// EVERY endpoint here is gated by requireAdmin (see recourse.go), which accepts EITHER
// the BROKER_PRIVATE_KEY hex in X-Roger-Admin (CLI/curl) OR a web session whose
// github_id == ADMIN_GITHUB_ID (the browser portal). No data leaks to a non-admin: a
// non-matching request is 403'd BEFORE any aggregate query runs.
//
// The aggregates read across all accounts (store/admin.go); the live marketplace numbers
// come from the in-memory node registry. The web portal (web/src/admin.html +
// js/admin.js) is a thin reader over these endpoints with an auto-refresh.
//
// DEFERRED (noted in the prompt, not v1): real-time log STREAMING (this is a snapshot
// poll), proactive ALERTING, and per-account DRILL-DOWN. A recent-error-RATE tile is
// also deferred - the relay error path is not yet instrumented with a counter, so HEALTH
// reports uptime/version/readiness/total-requests honestly rather than a fabricated rate.

// adminGitHubID reads the single super-admin GitHub numeric id from ADMIN_GITHUB_ID.
// Unset / unparseable => 0 (the browser admin path is OFF; only the broker key works).
func adminGitHubID() int64 {
	if v := strings.TrimSpace(os.Getenv("ADMIN_GITHUB_ID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// adminWindowDays reads + clamps the `days` query param to [1,366], default 1 (today).
func adminWindowDays(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || n <= 0 {
		return 1
	}
	if n > 366 {
		return 366
	}
	return n
}

// stripeMode reports the billing/payout money-rail mode for the HEALTH/REVENUE tiles:
// "live" (an sk_live key), "test" (a test sk_ key present), or "disabled" (no key). It
// reads the loaded billing key, never exposing the key itself.
func (b *broker) stripeMode() string {
	k := b.bill.secretKey
	switch {
	case strings.HasPrefix(k, "sk_live"):
		return "live"
	case k != "":
		return "test"
	default:
		return "disabled"
	}
}

// liveMarket walks the in-memory node registry under b.mu to count the live marketplace:
// on-air (within nodeTTL) nodes, distinct live models, total registered nodes, private
// (band-only) node count, and banned nodes. A pure read of broker state.
func (b *broker) liveMarket(now time.Time) map[string]any {
	b.mu.Lock()
	b.metricsMu.Lock()
	var onAir, private int
	models := map[string]bool{}
	total := len(b.nodes)
	for id, n := range b.nodes {
		live := now.Sub(b.lastSeen[id]) < nodeTTL
		if b.private[id] {
			private++
		}
		if live && !b.banned[id] {
			onAir++
			for _, o := range n.Offers {
				models[o.Model] = true
			}
		}
	}
	bannedNodes := len(b.banned)
	b.metricsMu.Unlock()
	b.mu.Unlock()
	return map[string]any{
		"nodes_total":  total,
		"on_air":       onAir,
		"models_live":  len(models),
		"private":      private,
		"banned_nodes": bannedNodes,
	}
}

// adminOverview handles GET /admin/overview: the dashboard's HEALTH + MARKETPLACE +
// REVENUE/FINANCIAL rollup in one payload (the page's primary, auto-refreshed read).
// Admin-gated. ?days=N sets the marketplace window (default 1 = today).
func (b *broker) adminOverview(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	now := time.Now()
	days := adminWindowDays(r)
	since, until := metricsWindowUTC(now, days)

	// HEALTH: readiness (db + optional shared), uptime, version, total relayed requests.
	health := map[string]any{
		"version":        version,
		"uptime_seconds": int64(now.Sub(b.startTime).Seconds()),
		"started_at":     b.startTime.Unix(),
		"total_requests": b.totalReqs.Load(),
		"db":             "ok",
	}
	if b.db == nil {
		health["db"] = "nil"
		health["ready"] = false
	} else if err := b.db.Healthy(); err != nil {
		health["db"] = "down"
		health["ready"] = false
	} else {
		health["ready"] = true
	}
	if b.shared != nil {
		if b.shared.healthy() {
			health["shared"] = "ok"
		} else {
			health["shared"] = "degraded"
		}
	}

	fin, _ := b.db.AdminFinancials(now)
	totals, _ := b.db.AdminMarketTotals(since, until)
	seeded, seedLimit, seedRemaining, _ := b.db.SeedStatus()

	writeJSON(w, http.StatusOK, map[string]any{
		"now":         now.Unix(),
		"period_days": days,
		"health":      health,
		"marketplace": mergeMaps(b.liveMarket(now), map[string]any{
			"requests_total":    totals.Requests,
			"tokens_in_total":   totals.TokensIn,
			"tokens_out_total":  totals.TokensOut,
			"requests_window":   totals.WindowRequests,
			"tokens_in_window":  totals.WindowTokensIn,
			"tokens_out_window": totals.WindowTokensOut,
		}),
		"financial": map[string]any{
			"platform_fee":    fin.PlatformFee,
			"consumer_spend":  fin.ConsumerSpend,
			"operator_earned": fin.OperatorEarned,
			"topup_volume":    fin.TopupVolume,
			"held":            fin.Held,
			"reserved":        fin.Reserved,
			"payable":         fin.Payable,
			"paid":            fin.Paid,
			"clawed":          fin.Clawed,
			"platform_loss":   fin.PlatformLoss,
			"wallet_count":    fin.WalletCount,
			"wallet_balance":  fin.WalletBalance,
			"owner_count":     fin.OwnerCount,
			"node_bindings":   fin.NodeBindings,
			"fee_rate":        b.feeRate,
			"stripe_mode":     b.stripeMode(),
			"seed_funded":     seeded,
			"seed_limit":      seedLimit,
			"seed_remaining":  seedRemaining,
		},
	})
}

// mergeMaps returns a new map with b's entries overlaid on a's (b wins). Small helper so
// the overview can fold the live-market read into the windowed totals.
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// adminPayouts handles GET /admin/payouts: the PAYOUTS MANAGEMENT view - every operator's
// payable/held/paid/pending posture (the queue), the platform payout history, the
// open Stripe transfer-reversals (+ dead-letter status), and the payout policy. The
// admin-unhold control is the existing POST /admin/unhold. Admin-gated.
func (b *broker) adminPayouts(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	now := time.Now()
	queue, _ := b.db.AdminPayoutQueue(now, 200)
	if queue == nil {
		queue = []store.AdminPayoutQueueRow{}
	}
	history, _ := b.db.AdminAllPayouts(200)
	if history == nil {
		history = []store.Payout{}
	}
	// Open reversals: Stripe transfer-reversal intents still OWED (not yet done, not
	// dead-lettered). A non-empty list flags a money rail that hasn't fully recovered a
	// disputed payout yet. NOTE: dead-lettered reversals (exhausted retries, parked for
	// manual handling) are not returned by OpenPendingReversals; surfacing that
	// dead-letter set is a deferred v1.1 item (it needs a store read that includes them).
	open, _ := b.db.OpenPendingReversals(200)
	if open == nil {
		open = []store.PendingReversal{}
	}
	pol := store.LoadPayoutPolicy()
	writeJSON(w, http.StatusOK, map[string]any{
		"now":            now.Unix(),
		"queue":          queue,
		"history":        history,
		"open_reversals": open,
		"policy": map[string]any{
			"hold_days":  pol.HoldDays,
			"reserve":    pol.Reserve,
			"min_payout": pol.MinPayout,
			"schedule":   pol.Schedule,
		},
		"stripe_mode": b.stripeMode(),
	})
}

// adminAbuse handles GET /admin/abuse: the ABUSE/SAFETY view - banned operators (+ strike
// counts), struck-account + total-strike counts, the CSAM report queue depth, the
// abuse-report count, dispute/chargeback count, banned-node count, and accounts under a
// recount hold. Admin-gated. No CSAM CONTENT is ever returned (counts only).
func (b *broker) adminAbuse(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	abuse, _ := b.db.AdminAbuse()
	if abuse.BannedOwners == nil {
		abuse.BannedOwners = []store.AdminBannedOwner{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"abuse": abuse})
}

// adminActivity handles GET /admin/activity: the LOGS/ACTIVITY view - the recent
// cross-account ledger event stream (lineage), newest first. Admin-gated. ?limit=N
// (default 100, max 500). This is a snapshot poll; real-time streaming is deferred.
func (b *broker) adminActivity(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > 500 {
		limit = 500
	}
	rows, _ := b.db.AdminActivity(limit)
	if rows == nil {
		rows = []store.LedgerRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows, "count": len(rows)})
}

// adminWhoami handles GET /admin/whoami: the cheap is-this-caller-an-admin probe the web
// portal hits FIRST, so a non-admin is bounced to /login without ever loading data. It
// is admin-gated like everything else (a non-admin gets 403), then echoes the minimal
// admin identity so the page can show "signed in as @founder".
func (b *broker) adminWhoami(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	login, gid, _, _ := b.sessionOwner(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":        true,
		"github_login": login,
		"github_id":    gid,
		"via":          adminAuthVia(r),
	})
}

// adminAuthVia reports which credential authenticated the admin request (for the whoami
// echo): "key" when the broker-key header is present, else "session".
func adminAuthVia(r *http.Request) string {
	if r.Header.Get("X-Roger-Admin") != "" {
		return "key"
	}
	return "session"
}
