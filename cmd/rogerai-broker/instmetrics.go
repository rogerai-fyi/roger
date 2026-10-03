package main

import (
	"sync"
	"sync/atomic"
)

// instmetrics.go is the MULTI-INSTANCE OBSERVABILITY surface: a handful of low-overhead,
// lock-free counters that make cross-instance (Valkey-bus) dispatch vs. local dispatch
// VISIBLE on the admin overview, instead of inferring it from grep over logs. Every bump
// is a single atomic add on the relay path (no mutex, no allocation), so it is invisible
// to request latency. The counters are monotonic since process start and surfaced
// READ-ONLY on the admin-gated /admin/live.
//
// They are PURE TELEMETRY: they change no request behavior and are byte-for-byte invisible
// to clients. localDispatch is bumped on the single-instance fast-path too, but that only
// touches an atomic int - the HTTP response is identical to today.
type instStats struct {
	// localDispatch counts jobs handed to a poller on THIS instance via the in-memory job
	// channel: the single-instance fast-path AND the multi-instance case where the picked
	// node happens to long-poll this same instance.
	localDispatch atomic.Int64
	// busDispatch counts jobs dispatched to a poller over the Valkey bus (delivered to a
	// subscriber on some instance) - the cross-instance relay handoff working as intended.
	busDispatch atomic.Int64
	// busNoPoller counts bus dispatches that reached NO poller on any instance (the node
	// was busy / had no free poller) - the cross-instance equivalent of a full local queue.
	// A high ratio vs. busDispatch means the registry mirror sees nodes whose pollers are
	// saturated or have drifted off-air.
	busNoPoller atomic.Int64
	// busDispatchErr counts bus dispatches that failed on a backend error (publish/subscribe
	// against Valkey). The request failed cleanly and the pre-auth hold was refunded; a
	// non-zero, growing value is the signal that the bus itself is unhealthy.
	busDispatchErr atomic.Int64

	// The dispatch queue (dispatchq.go). dqNudge: wake-ups sent to a peer's idle poller;
	// dqBounce: wake-ups a peer answered with "no idle poller" (a stale route); dqHandoff: jobs
	// written to a node from the queue; dqBusy: jobs refused or withdrawn as station busy;
	// dqLost: popped jobs whose handoff never reported taken; dqOffAir: dispatches refused
	// because the node is not live.
	dqNudge   atomic.Int64
	dqBounce  atomic.Int64
	dqHandoff atomic.Int64
	dqBusy    atomic.Int64
	dqLost    atomic.Int64
	dqOffAir  atomic.Int64
	// dqRedeliver counts jobs put back because an ack-capable node never acked them.
	dqRedeliver atomic.Int64
	// rcFrames counts remote-control frames this instance received for its viewers.
	rcFrames atomic.Int64

	// Upstream failover / cooldown (features/routing/upstream_failover.feature): relays
	// re-dispatched to a sibling after a no-output failure, stations cooled by an upstream
	// 429, and consumer requests refused fast with the band-cooling 503.
	relayFailovers atomic.Int64
	routingPref    [4]atomic.Int64 // per-profile routing passes, indexed by pref
	// The routing expression (features/routing/ROUTING-EXPRESSION-CONTRACT.md): requests
	// that carried a routing body / were refused with a routing 400, requests that used a
	// strict order / a strict sort, no-fallback requests refused no_match, the variant sugar
	// per suffix, unknown X-Roger-Pref header values (lenient: balanced), and refusals that
	// named a capability. Counts only - never a node id, a price or a band code.
	routingBodyRequests      atomic.Int64
	routingBodyRejects       atomic.Int64
	routingStrictOrder       atomic.Int64
	routingStrictSort        atomic.Int64
	modelFallbacks           atomic.Int64 // a failover that moved to a LATER model of the list
	routingNoFallbackRefused atomic.Int64
	variantFree              atomic.Int64
	variantFloor             atomic.Int64
	variantNitro             atomic.Int64
	prefHeaderUnknown        atomic.Int64
	relayNoMatchCapability   atomic.Int64
	stationCooldowns         atomic.Int64
	bandCooling503           atomic.Int64
	// The Tower bridge inside the plan (contract §6): fan-out coin flips (counted only when
	// the coin is consulted), Tower rows declined per consumer constraint (once per request
	// and constraint), and ids that name both a direct node and a Tower.
	edgeCoinFlips atomic.Int64
	// generationLookups counts GET /generation reads (every outcome).
	generationLookups atomic.Int64
	edgeMu            sync.Mutex
	edgeDeclined      map[string]int64
	// noMatchFilter counts no_match refusals by each filter that emptied the pool
	// (relay_no_match_<filter>, contract §5). Guarded by edgeMu.
	noMatchFilter map[string]int64
	collisions    map[string]bool
	// paramsMismatch: node ids whose declared params_b the model id contradicts (§5).
	paramsMismatch map[string]bool
}

// edgeDeclineAdd counts one request in which a Tower row was declined for reason.
func (s *instStats) edgeDeclineAdd(reason string) {
	s.edgeMu.Lock()
	if s.edgeDeclined == nil {
		s.edgeDeclined = map[string]int64{}
	}
	s.edgeDeclined[reason]++
	s.edgeMu.Unlock()
}

// edgeDeclinedSnapshot is the per-constraint decline counters (edge_bridge_declined{<c>}).
func (s *instStats) edgeDeclinedSnapshot() map[string]int64 {
	s.edgeMu.Lock()
	defer s.edgeMu.Unlock()
	out := make(map[string]int64, len(s.edgeDeclined))
	for k, v := range s.edgeDeclined {
		out[k] = v
	}
	return out
}

// noteCollision records an id that names both a direct node and a Tower (warned once).
func (s *instStats) noteCollision(id string) { s.noteID(&s.collisions, id) }

func (s *instStats) collisionsSnapshot() []string { return s.idsOf(s.collisions) }

// noteParamsMismatch records a node whose declared params_b its model id contradicts.
func (s *instStats) noteParamsMismatch(id string) { s.noteID(&s.paramsMismatch, id) }

func (s *instStats) paramsMismatchSnapshot() []string { return s.idsOf(s.paramsMismatch) }

func (s *instStats) noteID(set *map[string]bool, id string) {
	s.edgeMu.Lock()
	if *set == nil {
		*set = map[string]bool{}
	}
	(*set)[id] = true
	s.edgeMu.Unlock()
}

func (s *instStats) idsOf(set map[string]bool) []string {
	s.edgeMu.Lock()
	defer s.edgeMu.Unlock()
	return sortedKeys(set)
}

// snapshot returns the counters as a plain map for the admin overview JSON. Read-only.
func (s *instStats) snapshot() map[string]any {
	return map[string]any{
		"local_dispatch":   s.localDispatch.Load(),
		"bus_dispatch":     s.busDispatch.Load(),
		"bus_no_poller":    s.busNoPoller.Load(),
		"bus_dispatch_err": s.busDispatchErr.Load(),
		"dq_nudge":         s.dqNudge.Load(),
		"dq_bounce":        s.dqBounce.Load(),
		"dq_handoff":       s.dqHandoff.Load(),
		"dq_busy":          s.dqBusy.Load(),
		"dq_lost":          s.dqLost.Load(),
		"dq_off_air":       s.dqOffAir.Load(),
	}
}

// countVariants bumps the per-suffix sugar counters for one request (each suffix once).
func (s *instStats) countVariants(suffixes []string) {
	for sfx, ctr := range map[string]*atomic.Int64{"free": &s.variantFree, "floor": &s.variantFloor, "nitro": &s.variantNitro} {
		if containsString(suffixes, sfx) {
			ctr.Add(1)
		}
	}
}

// routingCounters is the flat /admin/live view of the routing-expression counters.
func (s *instStats) routingCounters() map[string]int64 {
	m := map[string]int64{
		"routing_body_requests":      s.routingBodyRequests.Load(),
		"routing_body_rejects":       s.routingBodyRejects.Load(),
		"routing_strict_order":       s.routingStrictOrder.Load(),
		"routing_strict_sort":        s.routingStrictSort.Load(),
		"routing_nofallback_refused": s.routingNoFallbackRefused.Load(),
		"variant_free":               s.variantFree.Load(),
		"variant_floor":              s.variantFloor.Load(),
		"variant_nitro":              s.variantNitro.Load(),
		"pref_header_unknown":        s.prefHeaderUnknown.Load(),
		"relay_no_match_capability":  s.relayNoMatchCapability.Load(),
		"edge_coin_flips":            s.edgeCoinFlips.Load(),
		"generation_lookups":         s.generationLookups.Load(),
	}
	s.edgeMu.Lock()
	for f, n := range s.noMatchFilter {
		m["relay_no_match_"+f] = n
	}
	s.edgeMu.Unlock()
	return m
}

func (s *instStats) noteNoMatchFilter(f string) {
	s.edgeMu.Lock()
	if s.noMatchFilter == nil {
		s.noMatchFilter = map[string]int64{}
	}
	s.noMatchFilter[f]++
	s.edgeMu.Unlock()
}
