package main

// jobcancel.go - negotiated job cancel (features/multinode/job_cancel.feature, founder ruling
// 2026-10-05). A node that sends X-Roger-Cancel: 1 on its polls is cancel-capable; when the
// consumer leaves, the broker queues a cancel for the dispatched job and the node's cancel poll
// (GET /agent/cancels) picks it up on whichever instance it is held.
//
// STATE: the capability and the cancels live in the SHARED store (sharedstore_cancel.go) so any
// instance can note, send and deliver them. The local maps below are used ONLY when no shared
// store is configured (single-instance dev); with a store configured, a store error fails safe:
// the node is treated as not cancel-capable (today's behaviour, C5) and a cancel that cannot be
// queued is dropped (the station finishes the job; billing is unaffected).

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// cancelHeader is sent by nodes that stop a job when told to (job_cancel.feature C1).
const cancelHeader = "X-Roger-Cancel"

// cancelCapTTL is how long one advertising poll keeps a node cancel-capable.
var cancelCapTTL = 2 * time.Minute

// cancelBufTTL is how long an undelivered cancel waits for the node's next cancel poll.
var cancelBufTTL = 2 * time.Minute

// cancelPollHold is how long GET /agent/cancels holds when nothing is queued.
var cancelPollHold = 25 * time.Second

// cancelEntry is one queued cancel, carrying its own expiry.
type cancelEntry struct {
	Job string `json:"job"`
	Exp int64  `json:"exp"` // unix ms
}

// cancelLocal is the single-instance (no shared store) fallback.
type cancelLocal struct {
	mu   sync.Mutex
	cap  map[string]int64
	buf  map[string][]cancelEntry
	wake map[string]chan struct{}
}

func (c *cancelLocal) init() {
	if c.cap == nil {
		c.cap, c.buf, c.wake = map[string]int64{}, map[string][]cancelEntry{}, map[string]chan struct{}{}
	}
}

func (c *cancelLocal) wakeCh(node string) chan struct{} {
	ch, ok := c.wake[node]
	if !ok {
		ch = make(chan struct{})
		c.wake[node] = ch
	}
	return ch
}

// noteCancelCapable records that node advertised cancel support on a poll.
func (b *broker) noteCancelCapable(node string) {
	until := time.Now().Add(cancelCapTTL).UnixMilli()
	if b.shared != nil {
		_ = b.shared.cancelCapSet(node, until, cancelCapTTL)
		return
	}
	b.cancels.mu.Lock()
	b.cancels.init()
	b.cancels.cap[node] = until
	b.cancels.mu.Unlock()
}

// cancelCapable reports whether node advertised cancel support recently (any instance).
func (b *broker) cancelCapable(node string) bool {
	now := time.Now().UnixMilli()
	if b.shared != nil {
		until, err := b.shared.cancelCapGet(node)
		return err == nil && until > now
	}
	b.cancels.mu.Lock()
	defer b.cancels.mu.Unlock()
	b.cancels.init()
	return b.cancels.cap[node] > now
}

// sendCancel queues a cancel for job on node when the node is cancel-capable (C1).
func (b *broker) sendCancel(node, job string) {
	if node == "" || job == "" || !b.cancelCapable(node) {
		return
	}
	e := cancelEntry{Job: job, Exp: time.Now().Add(cancelBufTTL).UnixMilli()}
	if b.shared != nil {
		raw, _ := json.Marshal(e)
		_ = b.shared.cancelPush(node, raw, cancelBufTTL)
		b.stats.cancelsSent.Add(1)
		return
	}
	b.cancels.mu.Lock()
	b.cancels.init()
	b.cancels.buf[node] = append(b.cancels.buf[node], e)
	close(b.cancels.wakeCh(node))
	delete(b.cancels.wake, node)
	b.cancels.mu.Unlock()
	b.stats.cancelsSent.Add(1)
}

// liveIDs keeps the unexpired job ids of raw entries.
func liveIDs(raws [][]byte, now int64) []string {
	var ids []string
	for _, raw := range raws {
		var e cancelEntry
		if json.Unmarshal(raw, &e) == nil && e.Job != "" && e.Exp > now {
			ids = append(ids, e.Job)
		}
	}
	return ids
}

// takeLocal drains the local buffer for node (unexpired entries only).
func (b *broker) takeLocal(node string) []string {
	b.cancels.mu.Lock()
	defer b.cancels.mu.Unlock()
	b.cancels.init()
	now := time.Now().UnixMilli()
	var ids []string
	for _, e := range b.cancels.buf[node] {
		if e.Exp > now {
			ids = append(ids, e.Job)
		}
	}
	delete(b.cancels.buf, node)
	return ids
}

// agentCancels is GET /agent/cancels?node=<id> (Bearer BridgeToken): the node's long-poll for
// the job ids to stop. 200 {"ids":[...]} or 204 after the hold.
func (b *broker) agentCancels(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	node := r.URL.Query().Get("node")
	t, tok := b.tunnelFor(node)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	answer := func(ids []string) {
		if len(ids) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"ids": ids})
	}
	hold := time.NewTimer(cancelPollHold)
	defer hold.Stop()
	if b.shared == nil {
		for {
			if ids := b.takeLocal(node); len(ids) > 0 {
				answer(ids)
				return
			}
			b.cancels.mu.Lock()
			b.cancels.init()
			ch := b.cancels.wakeCh(node)
			b.cancels.mu.Unlock()
			select {
			case <-ch:
			case <-hold.C:
				answer(nil)
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	// Shared: subscribe first, then drain the buffer, so a cancel pushed in between is either
	// buffered (no subscriber yet) or published to us - never lost.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	msgs, unsub, err := b.shared.cancelSubscribe(ctx, node)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "cancel delivery unavailable")
		return
	}
	defer unsub()
	now := time.Now().UnixMilli()
	if raws, derr := b.shared.cancelDrain(node); derr == nil {
		if ids := liveIDs(raws, now); len(ids) > 0 {
			answer(ids)
			return
		}
	}
	select {
	case raw, ok := <-msgs:
		if !ok {
			answer(nil)
			return
		}
		answer(liveIDs([][]byte{raw}, time.Now().UnixMilli()))
	case <-hold.C:
		answer(nil)
	case <-r.Context().Done():
	}
}
