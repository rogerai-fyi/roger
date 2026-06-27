package main

import (
	"log"
	"time"
)

// staleNodeTTL is how long a node may be OFFLINE (no heartbeat) before its persisted
// registration is pruned from the registry + store. It is FAR longer than nodeTTL (the
// 45s on-air liveness gate): a node merely off for the night still shows as ○ off-air
// and is never pruned - only a genuinely dead registration ages out (e.g. a machine
// that upgraded to a privacy-callsign id and abandoned its old hostname-based id, which
// will never heartbeat again). Tunable via ROGERAI_NODE_PRUNE_DAYS; <=0 disables it.
var staleNodeTTL = func() time.Duration {
	days := envFloat("ROGERAI_NODE_PRUNE_DAYS", 7)
	if days <= 0 {
		return 0
	}
	return time.Duration(days * float64(24*time.Hour))
}()

// pruneStaleNodes removes every node offline longer than staleNodeTTL from the
// in-memory registry, its per-node metric maps, and the persistent store, returning the
// count pruned. Liveness is read from b.lastSeen, which rehydrateNodes seeds from the
// PERSISTED last_seen and every heartbeat refreshes (TouchNode) - so a live provider is
// never mistaken for stale across a restart. Pruning a still-running node would be
// harmless anyway: it re-registers on its next heartbeat, and earnings + the owner
// binding (separate tables) are left intact, so nothing about money is lost.
func (b *broker) pruneStaleNodes(now time.Time) int {
	if staleNodeTTL <= 0 || b.db == nil {
		return 0
	}
	cutoff := now.Add(-staleNodeTTL)

	b.mu.Lock()
	var stale []string
	for id := range b.nodes {
		if b.lastSeen[id].Before(cutoff) {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		delete(b.nodes, id)
		delete(b.lastSeen, id)
		delete(b.tunnels, id)
		delete(b.confidential, id)
		delete(b.private, id)
		delete(b.bandOf, id)
		delete(b.attestedAt, id)
		delete(b.localRegAt, id)
	}
	b.mu.Unlock()

	if len(stale) == 0 {
		return 0
	}

	// Per-node market metrics live behind metricsMu; drop them so a pruned node leaves
	// no dangling signal/trust/tps state behind.
	b.metricsMu.Lock()
	for _, id := range stale {
		delete(b.tps, id)
		delete(b.inflight, id)
		delete(b.success, id)
		delete(b.trust, id)
		delete(b.successCount, id)
		delete(b.concurrentTPS, id)
		delete(b.lastPersist, id)
		delete(b.lastSharedSeen, id)
		delete(b.probeSched, id)
	}
	b.metricsMu.Unlock()

	// Persistent registration (outside the locks - DB I/O). Earnings/bindings untouched.
	for _, id := range stale {
		if err := b.db.DeleteNode(id); err != nil {
			log.Printf("node-prune: DeleteNode %q failed: %v", id, err)
		}
	}
	log.Printf("node-prune: removed %d dead registration(s) offline > %s (earnings + owner bindings untouched)", len(stale), staleNodeTTL)
	return len(stale)
}

// pruneStaleNodesSweep runs pruneStaleNodes shortly after startup - after a short delay
// so a redeploy's still-running providers have re-confirmed liveness via their heartbeat
// first - and then on a steady cadence. Disabled when staleNodeTTL<=0.
func (b *broker) pruneStaleNodesSweep() {
	if staleNodeTTL <= 0 {
		log.Printf("node-prune: DISABLED (ROGERAI_NODE_PRUNE_DAYS<=0)")
		return
	}
	log.Printf("node-prune: ON - registrations offline > %s are removed (first pass in 2m, then every 6h)", staleNodeTTL)
	time.Sleep(2 * time.Minute) // grace for live providers to re-heartbeat after a restart
	b.pruneStaleNodes(time.Now())
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		b.pruneStaleNodes(time.Now())
	}
}
