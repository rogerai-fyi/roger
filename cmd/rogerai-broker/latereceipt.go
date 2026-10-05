package main

// latereceipt.go - a non-stream result that arrives after the consumer was answered 504
// (contract §14.10, #14, default decision Option A): within ROGERAI_LATE_RECEIPT_GRACE
// (default 30 s) it is recorded as a $0 lineage receipt (void_reason late-after-timeout) - the
// consumer billed nothing, the operator unpaid, no strike; after the grace it is discarded as
// before.
//
// STATE: the expectation ("this job timed out; its result may still come") lives in the
// shared store with the grace as its TTL, because the result can land on any instance; the
// claim that turns it into a receipt is a set-if-absent, so a result delivered twice records
// once. The local map is ONLY the fallback for a broker with no shared store configured, or
// while the store is unreachable (a lineage record, not money: recording it on this instance
// is the best available).

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func lateReceiptGrace() time.Duration {
	return envDuration("ROGERAI_LATE_RECEIPT_GRACE", 30*time.Second)
}

// lateTicket is what a late result needs to be recorded against.
type lateTicket struct {
	Node  string `json:"node"`
	Payer string `json:"payer"`
	Model string `json:"model"`
	Until int64  `json:"until"` // unix ms: the grace's end
}

type lateLocal struct {
	mu sync.Mutex
	m  map[string]lateTicket
}

func lateKey(jobID string) string { return "late:" + jobID }

// expectLate records that job timed out and its result may still arrive.
func (b *broker) expectLate(jobID string, t lateTicket) {
	grace := lateReceiptGrace()
	if grace <= 0 {
		return
	}
	t.Until = time.Now().Add(grace).UnixMilli()
	raw, _ := json.Marshal(t)
	if b.shared != nil && b.shared.cacheSet(lateKey(jobID), raw, grace) == nil {
		return
	}
	b.lateLocal.mu.Lock()
	if b.lateLocal.m == nil {
		b.lateLocal.m = map[string]lateTicket{}
	}
	now := time.Now().UnixMilli()
	for id, lt := range b.lateLocal.m { // bounded by the grace: drop the lapsed
		if lt.Until < now {
			delete(b.lateLocal.m, id)
		}
	}
	b.lateLocal.m[jobID] = t
	b.lateLocal.mu.Unlock()
}

// takeLate claims a job's late ticket exactly once (false when none, lapsed, or claimed).
func (b *broker) takeLate(jobID string) (lateTicket, bool) {
	var t lateTicket
	if b.shared != nil {
		if raw, found, err := b.shared.cacheGet(lateKey(jobID)); err == nil {
			if !found || json.Unmarshal(raw, &t) != nil {
				return t, false
			}
			if won, err := b.shared.setIfAbsent("latedone:"+jobID, "1", lateReceiptGrace()+time.Minute); err != nil || !won {
				return t, false
			}
			_ = b.shared.cacheDel(lateKey(jobID))
			return t, time.Now().UnixMilli() <= t.Until
		}
	}
	b.lateLocal.mu.Lock()
	defer b.lateLocal.mu.Unlock()
	t, ok := b.lateLocal.m[jobID]
	delete(b.lateLocal.m, jobID)
	return t, ok && time.Now().UnixMilli() <= t.Until
}

// lateResult records a result no relay was waiting for, when it is a late one.
func (b *broker) lateResult(node string, res protocol.JobResult) {
	t, ok := b.takeLate(res.ID)
	if !ok || t.Node != node || b.db == nil {
		return // not a timed-out job, or past the grace: discarded as before
	}
	b.mu.Lock()
	reg := b.nodes[node]
	b.mu.Unlock()
	rec := res.Receipt
	if !rec.VerifyNode(reg.PubKey) || !rec.BindsTo(res.ID, node) {
		return
	}
	rec.VoidReason, rec.UpstreamStatus = protocol.VoidLateAfterTimeout, res.Status
	rec.Curated, rec.CuratedAtCost = b.nodeCurated(node), b.nodeCuratedAtCost(node)
	rec.SignBroker(b.priv)
	if _, err := b.db.Settle(t.Payer, node, 0, 0, rec); err != nil {
		log.Printf("late receipt request=%s node=%s: record failed: %v", res.ID, node, err)
		return
	}
	log.Printf("LATE receipt request=%s node=%s model=%s - after the 504, recorded at $0, not billed, not paid, not a strike", res.ID, node, t.Model)
}

// lateResultRaw is lateResult for a result's wire bytes (the dispatch inbox).
func (b *broker) lateResultRaw(node string, raw []byte) {
	var res protocol.JobResult
	if json.Unmarshal(raw, &res) == nil {
		b.lateResult(node, res)
	}
}
