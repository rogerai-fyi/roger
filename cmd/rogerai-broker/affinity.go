package main

// affinity.go: session affinity (contract §14.B1, features/routing/session_affinity.feature).
// A conversation's next turn prefers the station that served its previous turn, so the
// station's prompt cache is reused. A preference for the plan head only, never a limit: the
// affine station is tried first when it is eligible for THIS request, Tier-A, not cooling and
// below its load limit, and normal routing runs silently otherwise.
//
// The entry lives in the shared store under a keyed hash of (payer, session, served model), so
// every instance honours it and the raw session id is never stored; a broker without a
// reachable shared store keeps a bounded local map (best effort, same rules).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// affinityTTL is ROGERAI_AFFINITY_TTL (a Go duration, default 10m): the inactivity window,
// refreshed by every served turn.
func affinityTTL() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ROGERAI_AFFINITY_TTL")); err == nil && d > 0 {
		return d
	}
	return 10 * time.Minute
}

// affinityTowerPrefix marks an entry whose server is a Tower row (its Tower id), not a station.
const affinityTowerPrefix = "tower:"

// affinityLocalMax bounds the no-shared-store fallback map.
const affinityLocalMax = 10000

// affinityEntry is the server of a session's last served turn and when it served (unix ms).
type affinityEntry struct {
	Node string `json:"n"`
	At   int64  `json:"t"`
}

type affinityLocal struct {
	mu sync.Mutex
	m  map[string]affinityEntry
}

// affinityKey is the store key: an HMAC under the broker's own secret, so neither the session
// id nor the payer can be read back from it.
func (b *broker) affinityKey(payer, session, model string) string {
	mac := hmac.New(sha256.New, b.deriveSecret("rogerai affinity v1"))
	mac.Write([]byte(payer + "\x00" + session + "\x00" + model))
	return "aff:" + hex.EncodeToString(mac.Sum(nil))[:40]
}

// affinityGet returns the entry's server and whether it is still inside the window; found is
// false when no entry exists at all (a fresh session, not an expired one).
func (b *broker) affinityGet(key string) (node string, fresh, found bool) {
	var e affinityEntry
	if raw, ok := b.affinityRead(key); ok && json.Unmarshal(raw, &e) == nil && e.Node != "" {
		return e.Node, b.now().Sub(time.UnixMilli(e.At)) <= affinityTTL(), true
	}
	return "", false, false
}

func (b *broker) affinityRead(key string) ([]byte, bool) {
	if b.shared != nil {
		if raw, found, err := b.shared.cacheGet(key); err == nil {
			return raw, found
		}
	}
	b.affLocal.mu.Lock()
	defer b.affLocal.mu.Unlock()
	e, ok := b.affLocal.m[key]
	if !ok {
		return nil, false
	}
	raw, _ := json.Marshal(e)
	return raw, true
}

// affinitySet records a served turn. The stored key outlives the window by an hour so an
// expired entry still reads as expired (affinity_misses{expired}) rather than as a new session.
func (b *broker) affinitySet(key, node string) {
	e := affinityEntry{Node: node, At: b.now().UnixMilli()}
	raw, _ := json.Marshal(e)
	if b.shared != nil && b.shared.cacheSet(key, raw, affinityTTL()+time.Hour) == nil {
		return
	}
	b.affLocal.mu.Lock()
	defer b.affLocal.mu.Unlock()
	if b.affLocal.m == nil {
		b.affLocal.m = map[string]affinityEntry{}
	}
	if _, ok := b.affLocal.m[key]; !ok && len(b.affLocal.m) >= affinityLocalMax {
		for k := range b.affLocal.m { // bounded: drop an arbitrary entry (best effort)
			delete(b.affLocal.m, k)
			break
		}
	}
	b.affLocal.m[key] = e
}

// affineGateLocked is why the affine station cannot head THIS request ("" = it can): the pick
// for it (pinned) already applied every constraint and the station cooldown; this adds the
// pair cooldown, the Tier-A bar and the load limit. Caller holds b.mu.
func (b *broker) affineGateLocked(node, model string, picked bool, req pickReq) string {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	now := b.now()
	_, cooling := b.coolingUntilLocked(node, now)
	if pu, ok := pairUntil(req.pairCool, node, model); ok && now.Before(pu) {
		cooling = true
	}
	switch {
	case cooling:
		return "cooling"
	case !picked:
		return "ineligible"
	}
	tq := b.trust[node]
	sr, seen := b.success[node]
	if !tierAHealthy(tq.probeFails, sr, seen) {
		return "ineligible"
	}
	if b.inflight[node]+b.peerInflight[node] >= capacityOf(b.concurrentTPS[node], b.nodes[node].HW) {
		return "busy"
	}
	return ""
}
