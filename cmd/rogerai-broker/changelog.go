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
		entries, gap, err := vs.changesAfter(t.last, changeBatch)
		if err != nil {
			return // store unreachable: keep the local view (C5)
		}
		if !gap {
			b.applyChanges(append(t.takeDeferred(), entries...))
			if len(entries) > 0 {
				t.last = entries[len(entries)-1].id
			}
			return
		}
	}
	// Full snapshot. Read the head FIRST: anything logged during the snapshot is applied
	// again on the next tick, and applying an entry twice changes nothing.
	head, err := vs.changeHead()
	if err != nil {
		return
	}
	b.syncRegistry()
	b.syncToolsVerified()
	b.syncCooling()
	t.last, t.fullAt, t.deferred = head, time.Now(), nil
	t.snapshots.Add(1)
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
				if b.changes.deferred == nil {
					b.changes.deferred = map[string]changeEntry{}
				}
				b.changes.deferred[e.key] = e
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
