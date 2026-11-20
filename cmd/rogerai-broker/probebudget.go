package main

// probebudget.go - what Core's checks of a node may cost (features/serving/probe_accounting.feature).
// One instance probes a node per round, holding a shared lease, and publishes the reading so
// every instance routes on it (R2); every check of a node, of any kind and from any instance,
// draws on one budget (R4). A check still looks exactly like a customer request to the node.

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// checkBudgetHour and checkBudgetDay bound Core's checks of one node in any hour and any
	// day: canaries, tool-call checks and Tower-carried canaries together, first sight included.
	checkBudgetHour = 12
	checkBudgetDay  = 60
	// toolBudgetDay is the share of a node's day budget tool-call checks may take, so liveness
	// canaries (48 a day at an idle node's 30 minute ceiling) always have room.
	toolBudgetDay = 12
	// probeLeaseTTL caps how long a round's probe lease lasts: the jitter before the canary and the
	// canary itself. The prober releases it as soon as its reading is published (or its check is
	// refused), so the next round finds it free; the cap only covers a prober that died.
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
	var toolTok string
	if kind == "tools" {
		var ok bool
		if toolTok, ok = b.toolCheckBudget(node, now); !ok {
			return false // tool-call checks keep within their share, leaving room for liveness
		}
	}
	ok, shared := b.sharedCheckBudget(node, now)
	if !shared {
		ok = b.localCheckBudget(node, now)
	}
	if !ok && toolTok != "" {
		b.dropToolCheck(node, toolTok, now) // not sent: its share of the day is given back
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
	if b.shared == nil || !b.multiInstance || !b.shared.healthy() {
		return true // no store to share through: probe as before, without waiting on it per node
	}
	if raw, found, err := b.shared.cacheGet(probeReadingKey(node)); err == nil && found {
		var r probeReading
		if json.Unmarshal(raw, &r) == nil && r.By != b.instanceID && now.Sub(time.UnixMilli(r.At)) < freshFor {
			b.applyPeerReading(node, r)
			b.noteLost(node, now) // and take the newer one, if a peer's probe is in flight now
			return false
		}
	}
	won, err := b.shared.setIfAbsent("probelease:"+node, b.instanceID, probeLeaseTTL)
	if err != nil || won {
		return true
	}
	b.noteLost(node, now)
	return false
}

// noteLost records that a peer has this node's probe, so its reading is taken when published.
func (b *broker) noteLost(node string, now time.Time) {
	b.checkMu.Lock()
	if b.probeLost == nil {
		b.probeLost = map[string]time.Time{}
	}
	b.probeLost[node] = now
	b.checkMu.Unlock()
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
	b.releaseProbe(node) // the round is over: the next one, on either instance, finds it free
}

// takePeerReadings adopts the published reading of every node whose probe this instance lost
// to a peer, once that reading is newer than the loss. A lost node whose reading never comes
// (the peer died mid-probe) is forgotten after a lease, and this instance probes it again on
// its own schedule.
func (b *broker) takePeerReadings(now time.Time) {
	if b.shared == nil {
		return
	}
	b.takeAgeingReadings(now)
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
	var reassert []string
	if r.OK && b.shared != nil {
		pfx := node + "\x00"
		for k, ok := range b.toolsOK {
			if ok && strings.HasPrefix(k, pfx) {
				reassert = append(reassert, strings.TrimPrefix(k, pfx))
			}
		}
	}
	b.metricsMu.Unlock()
	if r.OK && r.TPS > 0 {
		b.updateTPS(node, r.TPS) // the speed band, as the prober's own reading feeds it
	}
	// A passing reading re-asserts the verified tools mark here too when THIS instance is the
	// node's poll host (only the host writes it, so a stale bit elsewhere never re-poisons a
	// regression the host cleared): the host need not be the instance that ran the canary.
	if len(reassert) > 0 && b.authoritativeFor(node, time.Now()) {
		for _, model := range reassert {
			_ = b.shared.markToolsVerified(node, model, toolsVerifiedTTL)
		}
	}
}

// releaseProbe frees this instance's probe lease on a node once its round is over.
func (b *broker) releaseProbe(node string) {
	if b.shared != nil && b.multiInstance {
		_ = b.shared.delIfEqual("probelease:"+node, b.instanceID)
	}
}

// toolCheckBudget draws a tool-call check from its own day share (toolBudgetDay), across instances
// when the store answers, on this instance alone when it does not.
func (b *broker) toolCheckBudget(node string, now time.Time) (token string, ok bool) {
	if b.shared != nil && b.shared.healthy() {
		tok := randHex(8)
		ok, err := b.shared.edgeSlotReserve("checks-tools-d:"+node, tok, now.Add(24*time.Hour), now, toolBudgetDay)
		if err == nil {
			return tok, ok
		}
	}
	b.checkMu.Lock()
	defer b.checkMu.Unlock()
	if b.checkTimes == nil {
		b.checkTimes = map[string][]time.Time{}
	}
	key := node + "\x00tools"
	kept := b.checkTimes[key][:0:0]
	for _, at := range b.checkTimes[key] {
		if now.Sub(at) < 24*time.Hour {
			kept = append(kept, at)
		}
	}
	if len(kept) >= toolBudgetDay {
		b.checkTimes[key] = kept
		return "", false
	}
	b.checkTimes[key] = append(kept, now)
	return "local", true
}

// dropToolCheck gives back a tool-call slot whose check the main budget refused.
func (b *broker) dropToolCheck(node, token string, now time.Time) {
	if token != "local" {
		_ = b.shared.edgeSlotDrop("checks-tools-d:"+node, token)
		return
	}
	b.checkMu.Lock()
	defer b.checkMu.Unlock()
	key := node + "\x00tools"
	if ts := b.checkTimes[key]; len(ts) > 0 && ts[len(ts)-1].Equal(now) {
		b.checkTimes[key] = ts[:len(ts)-1]
	}
}

// takeAgeingReadings adopts a peer's newer reading for any node whose own reading is past half its
// freshness window, not only after a lost round: an instance whose round falls long before its
// peer's would otherwise let the node's reading (and its verified mark) lapse until its next round.
func (b *broker) takeAgeingReadings(now time.Time) {
	if !b.multiInstance || !b.shared.healthy() {
		return
	}
	b.mu.Lock()
	ids := make([]string, 0, len(b.nodes))
	for id := range b.nodes {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	half := b.probe.freshFor() / 2
	for _, id := range ids {
		b.metricsMu.Lock()
		st := b.probeSchedLocked()[id]
		ageing := st == nil || now.Sub(st.lastMeasured) > half
		b.metricsMu.Unlock()
		if !ageing || !b.ageCheckDue(id, now) {
			continue
		}
		raw, found, err := b.shared.cacheGet(probeReadingKey(id))
		var r probeReading
		// Only a reading still fresh: an old one (a lost publish, a pruned node) must not mark a
		// node measured, or a curated station skip this instance's first-sight canary.
		if err != nil || !found || json.Unmarshal(raw, &r) != nil || r.By == b.instanceID ||
			now.Sub(time.UnixMilli(r.At)) > b.probe.freshFor() {
			continue
		}
		b.applyPeerReading(id, r) // a no-op unless newer than the one already taken
	}
}

// ageCheckEvery is how often an ageing node's shared reading is looked up on this instance.
const ageCheckEvery = time.Minute

// ageCheckDue rate-limits takeAgeingReadings to one store read per node per ageCheckEvery, so a
// failing or never-measured node does not cost a read on every tick.
func (b *broker) ageCheckDue(node string, now time.Time) bool {
	b.checkMu.Lock()
	defer b.checkMu.Unlock()
	if b.ageCheckAt == nil {
		b.ageCheckAt = map[string]time.Time{}
	}
	if now.Sub(b.ageCheckAt[node]) < ageCheckEvery {
		return false
	}
	b.ageCheckAt[node] = now
	return true
}
