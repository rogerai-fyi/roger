package main

import (
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
)

// health.go is the liveness/readiness split. /health stays a cheap static "ok" (the
// process is up and serving), used as a liveness probe. /ready is a REAL readiness
// check the load balancer can gate on: it pings the durable store (b.db) and the
// optional shared state layer (b.shared), and only returns 200 when both are reachable,
// else 503 with a small JSON status so a broker whose Postgres just dropped is pulled
// out of rotation instead of black-holing requests.

// brokerCommit returns the exact source revision injected by the deployment
// platform. Refuse malformed or abbreviated values: /version is an audit surface,
// so an absent identity is better than asserting one that cannot name a commit.
func brokerCommit() string {
	commit := strings.ToLower(strings.TrimSpace(os.Getenv("ROGERAI_BUILD_COMMIT")))
	if len(commit) != 40 {
		return ""
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return ""
	}
	return commit
}

// logBrokerCommitStatus makes a deployment wiring error observable without
// echoing the supplied value. Keep brokerCommit strict: an abbreviated hash is
// useful for display, but it is not the exact auditable source identity promised
// by this endpoint.
func logBrokerCommitStatus() {
	if strings.TrimSpace(os.Getenv("ROGERAI_BUILD_COMMIT")) != "" && brokerCommit() == "" {
		log.Printf("build identity: ROGERAI_BUILD_COMMIT is not a full 40-character hexadecimal commit; omitting it from /version")
	}
}

// versionInfo reports the human release and exact deployed revision. no-store is
// intentional: during a rolling deployment two healthy instances may briefly run
// different commits, and an intermediary must not conceal that fact.
func (b *broker) versionInfo(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	info := map[string]any{"version": version}
	if commit := brokerCommit(); commit != "" {
		info["commit"] = commit
	}
	writeJSON(w, http.StatusOK, info)
}

// ready reports broker readiness as JSON. Healthy => 200 {"ready":true,...}; a failed
// dependency => 503 {"ready":false,...} naming which dependency is down. A broker not
// configured to share state never fails readiness on the shared store; one that is
// configured is not ready until the store has answered once ("connecting" /
// "not_configured", see bootShared), and after that a runtime outage is "degraded" only.
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
	switch b.sharedPhase.Load() {
	case sharedConnecting:
		status["ready"], status["shared"] = false, "connecting"
		code = http.StatusServiceUnavailable
	case sharedNotConfigured:
		status["ready"], status["shared"] = false, "not_configured"
		code = http.StatusServiceUnavailable
	default:
		b.sharedHealth(status)
	}

	writeJSON(w, code, status)
}

// sharedHealth reports a wired shared store (absent when none is configured).
func (b *broker) sharedHealth(status map[string]any) {
	if b.shared != nil {
		if b.shared.healthy() {
			status["shared"] = "ok"
		} else {
			status["shared"] = "degraded"
		}
	}
}
