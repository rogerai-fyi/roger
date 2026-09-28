package main

// changelog.go tails the shared change log (features/multinode/registry_changelog.feature):
// a quiet tick reads only the entries after the last one applied, instead of re-reading every
// registration, cooldown and tools verdict. A full snapshot runs at boot, on any gap, and every
// changeReconcile as a safety net (it also covers instances whose code does not log).

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// changeReconcile is the safety-net full snapshot interval. var so scenarios can shorten it.
var changeReconcile = 60 * time.Second

// changeBatch bounds one tick's read; an instance further behind takes a snapshot instead.
const changeBatch = 1000

type changeTail struct {
	mu     sync.Mutex
	last   string    // id of the last entry applied ("" = never synced)
	lastFP string    // that entry's fingerprint: an emptied store can reuse the same id
	fullAt time.Time // last full snapshot
	// deferred holds the newest registration entry per node that applyRegistry skipped only
	// because this instance registered the node within its grace window; it is re-applied on
	// later ticks (its TS then decides), so a genuinely newer peer entry is never lost.
	deferred map[string]changeEntry
	// snapshots counts full snapshots taken (telemetry; a quiet fleet should see one per
	// changeReconcile, not one per tick).
	snapshots atomic.Int64
}

// syncChanges brings this instance up to date with the fleet's registry, cooldowns and tools
// verdicts: by the log when it can, by a full snapshot when it must.
func (b *broker) syncChanges() {
	vs, ok := b.shared.(*valkeyStore)
	if !ok || vs == nil {
		b.syncRegistry() // no log on this store: the full re-read, as before
		b.syncToolsVerified()
		b.syncCooling()
		return
	}
	t := &b.changes
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last != "" && time.Since(t.fullAt) < changeReconcile {
		entries, gap, err := vs.changesAfter(t.last, t.lastFP, changeBatch)
		if err != nil {
			return // store unreachable: keep the local view (C5)
		}
		if !gap {
			b.applyChanges(append(t.takeDeferred(), entries...))
			if len(entries) > 0 {
				e := entries[len(entries)-1]
				t.last, t.lastFP = e.id, e.fingerprint()
			}
			return
		}
	}
	// Full snapshot. Read the head FIRST: anything logged during the snapshot is applied
	// again on the next tick, and applying an entry twice changes nothing.
	head, headFP, err := vs.changeHead()
	if err != nil {
		return
	}
	// Every read must succeed before the position moves: a snapshot that failed part-way
	// must be retried on the next tick, not skipped until the next reconcile.
	regs, err1 := vs.allNodes()
	pregs, err2 := vs.allPrivateNodes()
	tools, err3 := vs.toolsVerified(toolsVerifiedTTL)
	cools, err4 := vs.cooling()
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return
	}
	graced := b.applyRegistry(regs, pregs)
	b.metricsMu.Lock()
	b.toolsMerged = tools
	b.metricsMu.Unlock()
	b.applyCooling(cools)
	// A registration skipped only for the local grace window is deferred, not dropped, so a
	// peer's newer registration still lands once the grace has passed.
	t.deferred = nil
	for id := range graced {
		if raw, ok := pregs[id]; ok {
			t.hold(changeEntry{kind: "preg", key: id, data: raw})
		} else if raw, ok := regs[id]; ok {
			t.hold(changeEntry{kind: "reg", key: id, data: raw})
		}
	}
	t.last, t.lastFP, t.fullAt = head, headFP, time.Now()
	t.snapshots.Add(1)
}

// hold keeps the newest skipped registration entry for a node (caller holds t.mu).
func (t *changeTail) hold(e changeEntry) {
	if t.deferred == nil {
		t.deferred = map[string]changeEntry{}
	}
	t.deferred[e.key] = e
}

// takeDeferred returns the deferred entries and clears them (caller holds t.mu).
func (t *changeTail) takeDeferred() []changeEntry {
	out := make([]changeEntry, 0, len(t.deferred))
	for _, e := range t.deferred {
		out = append(out, e)
	}
	t.deferred = nil
	return out
}

// applyChanges applies log entries in order. Registrations go through applyRegistry one entry
// at a time (a later entry for the same node must win); cooldowns and tools verdicts merge.
func (b *broker) applyChanges(entries []changeEntry) {
	for _, e := range entries {
		switch e.kind {
		case "reg", "preg":
			var graced map[string]bool
			if e.kind == "reg" {
				graced = b.applyRegistry(map[string][]byte{e.key: e.data}, nil)
			} else {
				graced = b.applyRegistry(nil, map[string][]byte{e.key: e.data})
			}
			if graced[e.key] {
				b.changes.hold(e)
			} else {
				delete(b.changes.deferred, e.key) // a later entry was applied: the deferred one is moot
			}
		case "cool":
			untilStr, model, _ := strings.Cut(string(e.data), "|")
			if sec, err := strconv.ParseInt(untilStr, 10, 64); err == nil {
				b.applyCooling(map[string]sharedCooling{e.key: {until: time.Unix(sec, 0), model: model}})
			}
		case "tok":
			ms, err := strconv.ParseInt(string(e.data), 10, 64)
			if err != nil || time.Since(time.UnixMilli(ms)) > toolsVerifiedTTL {
				continue
			}
			b.metricsMu.Lock()
			if b.toolsMerged == nil {
				b.toolsMerged = map[string]bool{}
			}
			b.toolsMerged[e.key] = true
			b.metricsMu.Unlock()
		case "tclr":
			b.metricsMu.Lock()
			delete(b.toolsMerged, e.key)
			b.metricsMu.Unlock()
		}
	}
}
