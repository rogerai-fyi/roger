package main

import "sync/atomic"

// instmetrics.go is the MULTI-INSTANCE OBSERVABILITY surface: a handful of low-overhead,
// lock-free counters that make cross-instance (Valkey-bus) dispatch vs. local dispatch
// VISIBLE on the admin overview, instead of inferring it from grep over logs. Every bump
// is a single atomic add on the relay path (no mutex, no allocation), so it is invisible
// to request latency. The counters are monotonic since process start and surfaced
// READ-ONLY on the admin-gated /admin/overview.
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
}

// snapshot returns the counters as a plain map for the admin overview JSON. Read-only.
func (s *instStats) snapshot() map[string]any {
	return map[string]any{
		"local_dispatch":   s.localDispatch.Load(),
		"bus_dispatch":     s.busDispatch.Load(),
		"bus_no_poller":    s.busNoPoller.Load(),
		"bus_dispatch_err": s.busDispatchErr.Load(),
	}
}
