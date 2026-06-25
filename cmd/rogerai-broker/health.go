package main

import "net/http"

// health.go is the liveness/readiness split. /health stays a cheap static "ok" (the
// process is up and serving), used as a liveness probe. /ready is a REAL readiness
// check the load balancer can gate on: it pings the durable store (b.db) and the
// optional shared state layer (b.shared), and only returns 200 when both are reachable,
// else 503 with a small JSON status so a broker whose Postgres just dropped is pulled
// out of rotation instead of black-holing requests.

// ready reports broker readiness as JSON. Healthy => 200 {"ready":true,...}; a failed
// dependency => 503 {"ready":false,...} naming which dependency is down. The shared
// store is OPTIONAL (nil when ROGERAI_REDIS_URL is unset), so it is only checked when
// wired - an unconfigured shared layer never fails readiness.
func (b *broker) ready(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	status := map[string]any{"ready": true}
	code := http.StatusOK

	// Durable store: a nil store should never happen in a running broker, but treat it
	// as not-ready rather than panicking.
	if b.db == nil {
		status["ready"] = false
		status["db"] = "nil"
		code = http.StatusServiceUnavailable
	} else if err := b.db.Healthy(); err != nil {
		status["ready"] = false
		status["db"] = "down"
		code = http.StatusServiceUnavailable
	} else {
		status["db"] = "ok"
	}

	// Optional shared state layer (Valkey). nil = not configured = not a readiness
	// dependency. When wired but unreachable, surface it but DON'T fail readiness: the
	// in-memory path is authoritative and the broker still serves correctly without it
	// (it only degrades cross-instance rate-limit/liveness sharing). Report it so an
	// operator can see the degradation.
	if b.shared != nil {
		if b.shared.healthy() {
			status["shared"] = "ok"
		} else {
			status["shared"] = "degraded"
		}
	}

	writeJSON(w, code, status)
}
