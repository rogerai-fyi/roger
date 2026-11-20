package main

// nodeserved.go - GET /nodes/served: a node's own record of what it served and earned
// (features/serving/probe_accounting.feature R1). The share screen in the CLI and the phone app
// shows these numbers, never its own count: Core's checks look like customers to a node by
// design, so only Core knows which jobs were paid. Aggregates only, for one node, read with
// the node's own token, at most once every 30 seconds.

import (
	"net/http"
	"strconv"
	"time"
)

// servedReadEvery is how often a node may read its record.
const servedReadEvery = 30 * time.Second

func (b *broker) nodeServed(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	q := r.URL.Query()
	node := q.Get("node")
	t, tok := b.tunnelFor(node)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !b.servedReadAllowed(node) {
		w.Header().Set("Retry-After", strconv.Itoa(int(servedReadEvery/time.Second)))
		jsonErr(w, http.StatusTooManyRequests, "a node reads its record at most every 30 seconds")
		return
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	served, tokens, earned, err := b.db.ServedSince(node, since)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "the record cannot be read right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": node, "since": since, "served": served,
		"earned": round6(earned), "tokens": tokens, "as_of": time.Now().Unix()})
}

// servedReadAllowed lets a node read its record once per servedReadEvery, across instances when
// the shared store answers, on this instance alone when it does not.
func (b *broker) servedReadAllowed(node string) bool {
	if b.shared != nil && b.shared.healthy() {
		if ok, err := b.shared.setIfAbsent("servedread:"+node, "1", servedReadEvery); err == nil {
			return ok
		}
	}
	b.checkMu.Lock()
	defer b.checkMu.Unlock()
	if b.servedReadAt == nil {
		b.servedReadAt = map[string]time.Time{}
	}
	if at, ok := b.servedReadAt[node]; ok && time.Since(at) < servedReadEvery {
		return false
	}
	b.servedReadAt[node] = time.Now()
	return true
}
