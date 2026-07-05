package main

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// admin.go is the broker's SLIM super-admin surface. The founder dashboard itself (and the
// financial / payout / abuse / activity QUERY LOGIC) lives in the PRIVATE rogerai-fyi/roger-admin
// repo, which reads Postgres directly. The broker keeps only what CAN'T leave its process: the
// LIVE in-memory operational state (health, the node registry, dispatch counters, seed/fee/stripe),
// exposed via GET /admin/live, plus the POST /admin/unhold write (recourse.go).
//
// Both are gated by requireAdmin (recourse.go): EITHER the BROKER_PRIVATE_KEY hex in X-Roger-Admin
// (how roger-admin authenticates) OR a web session whose github_id == ADMIN_GITHUB_ID. A
// non-matching request is 403'd before any state is read.

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

// stripeMode reports the billing/payout money-rail mode: "live" (an sk_live key), "test" (a test
// sk_ key present), or "disabled" (no key). Reads the loaded billing key, never exposing it.
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

// instancesLive reports the number of DISTINCT live broker instances. In single-instance mode
// it is always 1 (this process). In multi-instance mode it counts the live presence heartbeats
// in the shared store, falling back to 1 (this instance is always live) when the store is
// unreachable or reports none - so the ops panel never renders a bogus 0-live-instances fleet.
func (b *broker) instancesLive() int {
	if !b.multiInstance || b.shared == nil {
		return 1
	}
	n, err := b.shared.liveInstances()
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// infra is the topology/redundancy block of /admin/live: the cross-instance posture the ops
// panel renders (fleet size + redundancy, bus mode, shared-store reachability). A pure,
// non-blocking read of broker state — every backend touch is bounded and degrades to a safe
// fallback (never panics, never blocks). db/version/uptime_seconds stay in the health block
// (reused, not duplicated). instances_live is read FIRST so its shared-store read refreshes
// reachability before shared_store.reachable is snapshotted.
func (b *broker) infra() map[string]any {
	live := b.instancesLive()
	role := "none"
	reachable := false
	if b.shared != nil {
		reachable = b.shared.healthy()
		kind := "memory"
		if _, ok := b.shared.(*valkeyStore); ok {
			kind = "valkey"
		}
		mode := "accelerator"
		if b.multiInstance {
			mode = "accelerator+bus"
		}
		role = kind + " " + mode
	}
	return map[string]any{
		"multi_instance": b.multiInstance,
		"instances_live": live,
		"shared_store": map[string]any{
			"reachable": reachable,
			"role":      role,
		},
	}
}

// adminLive handles GET /admin/live: the broker's LIVE operational snapshot — the in-memory
// state that exists ONLY in this process and so can't be read from Postgres by roger-admin:
// readiness/health, the live marketplace counts, the cross-instance dispatch counters, and the
// seed/fee/stripe config. roger-admin fetches this and merges it with its own Postgres-derived
// financial/market rollups to render the dashboard. Admin-gated.
func (b *broker) adminLive(w http.ResponseWriter, r *http.Request) {
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
		if vs, ok := b.shared.(*valkeyStore); ok {
			health["valkey_op_errors"] = vs.opErrors.Load()
		}
	}
	if b.multiInstance {
		health["instance_id"] = b.instanceID
		health["dispatch"] = b.stats.snapshot()
	}

	var seeded, seedLimit, seedRemaining int
	if b.db != nil {
		seeded, seedLimit, seedRemaining, _ = b.db.SeedStatus()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"now":              now.Unix(),
		"health":           health,
		"infra":            b.infra(),
		"marketplace_live": b.liveMarket(now),
		"seed_funded":      seeded,
		"seed_limit":       seedLimit,
		"seed_remaining":   seedRemaining,
		"fee_rate":         b.feeRate,
		"stripe_mode":      b.stripeMode(),
	})
}
