package main

import (
	"net/http"
	"sync"
	"time"
)

// storeoutage.go - serving through a RUNTIME shared-store outage (founder ruling 2026-10-04,
// features/multinode/runtime_store_outage.feature). A multi-instance broker that became ready
// keeps serving when Valkey stops answering: cross-instance dispatch needs the store, but a
// station long-polling THIS instance is handed the job in memory and its result and stream
// chunks are read back in memory. A boot-time outage is unchanged (NOT READY, bootShared).

// localPollFresh is how recently a station must have long-polled this instance to be served
// here while the shared store is down: one 25 s poll hold plus margin.
var localPollFresh = 30 * time.Second

// localJobs marks the jobs this instance handed to a local poller because the shared store was
// down, so their result and stream chunks are read back in memory rather than routed through
// the store. Per-request and per-instance by nature (the job never left this process).
var localJobs sync.Map // job id -> struct{}

// dispatchStoreDown reports whether this multi-instance broker currently sees its shared store
// as unreachable (the same signal /ready reports as shared "degraded").
func (b *broker) dispatchStoreDown() bool {
	if !b.multiInstance {
		return false
	}
	vs, ok := b.shared.(*valkeyStore)
	return ok && vs != nil && !vs.healthy()
}

// polledHereLocked reports whether node long-polled this instance within localPollFresh.
// Caller holds b.mu (localPollAt is guarded by it).
func (b *broker) polledHereLocked(node string, now time.Time) bool {
	at := b.localPollAt[node]
	return !at.IsZero() && now.Sub(at) < localPollFresh
}

// polledHere is polledHereLocked taking b.mu: a job for node can be handed over in memory when
// the shared store is down (or the dispatch store just failed) and the station polls here.
func (b *broker) polledHere(node string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.polledHereLocked(node, time.Now())
}

// outageSkipLocked is the pick-time rule: while the store is down, a station that does not
// long-poll this instance cannot be dispatched to and is skipped. Caller holds b.mu.
func (b *broker) outageSkipLocked(node string, now time.Time) bool {
	return b.dispatchStoreDown() && !b.polledHereLocked(node, now)
}

// markLocalJob / isLocalJob / unmarkLocalJob track a job handed over in memory.
func markLocalJob(id string)   { localJobs.Store(id, struct{}{}) }
func unmarkLocalJob(id string) { localJobs.Delete(id) }
func isLocalJob(id string) bool {
	_, ok := localJobs.Load(id)
	return ok
}

// dispatchBusUnavailable answers a request none of whose stations can be reached while the
// shared store is down: 503, a code, a Retry-After, nothing held or charged.
func dispatchBusUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	w.Header().Set("X-RogerAI-Cost", "0")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{
		"message": "dispatch bus unavailable - no station reachable from this instance right now",
		"code":    "dispatch_bus_unavailable",
	}})
}
