package main

// probebudget.go - what Core's checks of a node may cost (features/serving/probe_accounting.feature).
// One instance probes a node per round, holding a shared lease, and publishes the reading so
// every instance routes on it (R2); every check of a node, of any kind and from any instance,
// draws on one budget (R4). A check still looks exactly like a customer request to the node.

import (
	"encoding/json"
	"time"
)

const (
	// checkBudgetHour and checkBudgetDay bound Core's checks of one node in any hour and any
	// day: canaries, tool-call checks and Tower-carried canaries together, first sight included.
	checkBudgetHour = 12
	checkBudgetDay  = 60
	// probeLeaseTTL caps how long a round's probe lease lasts. The lease is held for less than
	// the probe floor (leaseTTL), so the next round always finds it free, whichever instance
	// held it; a peer whose round fell in the same window found it taken.
	probeLeaseTTL = probeJitter + 40*time.Second
	// probeResultTTL keeps a published reading past two ceilings, so a peer that ticks late
	// still takes it.
	probeResultTTL = 2 * time.Hour
)

var (
	// checkClock is the budget's clock; a test runs a day through it.
	checkClock = time.Now
	// probeCheckSent is a test seam: told of every check the budget let through.
	probeCheckSent func(b *broker, node, kind string)
)

// allowCheck spends one of the node's checks, or says no when its budget is spent. kind is
// "canary", "tools" or "tower". With the shared store down, each instance keeps the budget on
// its own, so no instance checks faster than the budget allows by itself.
func (b *broker) allowCheck(node, kind string) bool {
	now := checkClock()
	ok, shared := b.sharedCheckBudget(node, now)
	if !shared {
		ok = b.localCheckBudget(node, now)
	}
	if ok && probeCheckSent != nil {
		probeCheckSent(b, node, kind)
	}
	return ok
}

// sharedCheckBudget draws on the node's budget in the shared store: a sliding hour and day
// of reservations. shared is false when the store did not answer.
func (b *broker) sharedCheckBudget(node string, now time.Time) (ok, shared bool) {
	if b.shared == nil || !b.shared.healthy() {
		return false, false
	}
	tok := randHex(8)
	hour, err := b.shared.edgeSlotReserve("checks-h:"+node, tok, now.Add(time.Hour), now, checkBudgetHour)
	if err != nil {
		return false, false
	}
	if !hour {
		return false, true
	}
	day, err := b.shared.edgeSlotReserve("checks-d:"+node, tok, now.Add(24*time.Hour), now, checkBudgetDay)
	if err != nil {
		return true, true // the hour was spent and holds; the day cannot be read
	}
	if !day {
		_ = b.shared.edgeSlotDrop("checks-h:"+node, tok)
		return false, true
	}
	return true, true
}

// localCheckBudget is the same sliding hour and day, kept by this instance alone.
func (b *broker) localCheckBudget(node string, now time.Time) bool {
	b.checkMu.Lock()
	defer b.checkMu.Unlock()
	if b.checkTimes == nil {
		b.checkTimes = map[string][]time.Time{}
	}
	times := b.checkTimes[node][:0:0]
	inHour := 0
	for _, at := range b.checkTimes[node] {
		if now.Sub(at) < 24*time.Hour {
			times = append(times, at)
			if now.Sub(at) < time.Hour {
				inHour++
			}
		}
	}
	if inHour >= checkBudgetHour || len(times) >= checkBudgetDay {
		b.checkTimes[node] = times
		return false
	}
	b.checkTimes[node] = append(times, now)
	return true
}

// claimProbe takes this round's probe of a node for this instance. A peer's reading still fresh
// within this instance's interval for the node is taken instead of probing (the instances' rounds
// need not collide to share); a lost lease is noted so the reading the winner is about to publish
// is taken. A store that does not answer claims nothing and every instance probes as it did.
func (b *broker) claimProbe(node string, now time.Time, freshFor time.Duration) bool {
	if b.shared == nil || !b.multiInstance {
		return true
	}
	if raw, found, err := b.shared.cacheGet(probeReadingKey(node)); err == nil && found {
		var r probeReading
		if json.Unmarshal(raw, &r) == nil && r.By != b.instanceID && now.Sub(time.UnixMilli(r.At)) < freshFor {
			b.applyPeerReading(node, r)
			return false
		}
	}
	won, err := b.shared.setIfAbsent("probelease:"+node, b.instanceID, b.leaseTTL())
	if err != nil || won {
		return true
	}
	b.checkMu.Lock()
	if b.probeLost == nil {
		b.probeLost = map[string]time.Time{}
	}
	b.probeLost[node] = now
	b.checkMu.Unlock()
	return false
}

// probeReading is one node's reading as the instance that probed it holds it after the probe.
type probeReading struct {
	At            int64   `json:"at"` // unix ms
	By            string  `json:"by"`
	OK            bool    `json:"ok"`
	Completed     bool    `json:"completed"`
	Mismatch      bool    `json:"mismatch"`
	Fails         int     `json:"fails"`
	TTFTMs        float64 `json:"ttft_ms"`
	TPS           float64 `json:"tps"`
	Probes        int     `json:"probes"`
	LastMeasured  int64   `json:"last_measured"` // unix ms, 0 = never
	ProbeSchedule int64   `json:"last_probe"`    // unix ms
}

func probeReadingKey(node string) string { return "probe-reading:" + node }

// publishProbe shares this instance's reading of a node after its probe.
func (b *broker) publishProbe(node string, tq trustState, lastMeasured time.Time) {
	if b.shared == nil || !b.multiInstance {
		return
	}
	r := probeReading{At: time.Now().UnixMilli(), By: b.instanceID, OK: tq.probeOK, Completed: tq.probeCompleted,
		Mismatch: tq.modelMismatch, Fails: tq.probeFails, TTFTMs: tq.ttftMs, TPS: tq.probeTPS, Probes: tq.probes,
		ProbeSchedule: time.Now().UnixMilli()}
	if !lastMeasured.IsZero() {
		r.LastMeasured = lastMeasured.UnixMilli()
	}
	raw, _ := json.Marshal(r)
	_ = b.shared.cacheSet(probeReadingKey(node), raw, probeResultTTL)
}

// takePeerReadings adopts the published reading of every node whose probe this instance lost
// to a peer, once that reading is newer than the loss. A lost node whose reading never comes
// (the peer died mid-probe) is forgotten after a lease, and this instance probes it again on
// its own schedule.
func (b *broker) takePeerReadings(now time.Time) {
	if b.shared == nil {
		return
	}
	b.checkMu.Lock()
	lost := make(map[string]time.Time, len(b.probeLost))
	for n, at := range b.probeLost {
		lost[n] = at
	}
	b.checkMu.Unlock()
	for node, at := range lost {
		raw, found, err := b.shared.cacheGet(probeReadingKey(node))
		var r probeReading
		b.checkMu.Lock()
		applied := b.probeApplied[node]
		b.checkMu.Unlock()
		// A reading this instance already took is not the one its lost round is waiting for.
		if err != nil || !found || json.Unmarshal(raw, &r) != nil || r.By == b.instanceID ||
			r.At < at.Add(-probeLeaseTTL).UnixMilli() || r.At <= applied {
			if now.Sub(at) > probeLeaseTTL {
				b.forgetLost(node, at)
			}
			continue
		}
		b.applyPeerReading(node, r)
		b.forgetLost(node, at)
	}
}

func (b *broker) forgetLost(node string, at time.Time) {
	b.checkMu.Lock()
	if b.probeLost[node].Equal(at) {
		delete(b.probeLost, node)
	}
	b.checkMu.Unlock()
}

// applyPeerReading sets this instance's reading of a node to the peer's, as measured.
func (b *broker) applyPeerReading(node string, r probeReading) {
	b.checkMu.Lock()
	if b.probeApplied == nil {
		b.probeApplied = map[string]int64{}
	}
	if r.At <= b.probeApplied[node] {
		b.checkMu.Unlock()
		return
	}
	b.probeApplied[node] = r.At
	b.checkMu.Unlock()

	b.metricsMu.Lock()
	tq := b.trust[node]
	tq.probed, tq.probeOK, tq.probeCompleted, tq.modelMismatch = true, r.OK, r.Completed, r.Mismatch
	tq.probeFails, tq.ttftMs, tq.probeTPS = r.Fails, r.TTFTMs, r.TPS
	if r.Probes > tq.probes {
		tq.probes = r.Probes
	}
	b.trust[node] = tq
	st := b.probeSchedLocked()[node]
	if st == nil {
		st = &probeState{}
		b.probeSchedLocked()[node] = st
	}
	if r.LastMeasured > 0 {
		if lm := time.UnixMilli(r.LastMeasured); lm.After(st.lastMeasured) {
			st.lastMeasured = lm
		}
	}
	if lp := time.UnixMilli(r.ProbeSchedule); lp.After(st.lastProbe) {
		st.lastProbe = lp
	}
	b.metricsMu.Unlock()
}

// leaseTTL is the probe lease's life: under the probe floor, so a lease never blocks the next
// round (the holder's own included), and never under a few seconds.
func (b *broker) leaseTTL() time.Duration {
	return max(min(probeLeaseTTL, b.probe.interval-time.Second), 5*time.Second)
}
