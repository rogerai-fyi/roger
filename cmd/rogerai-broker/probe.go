package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// probe.go is the active canary + latency probe (see docs-internal/
// VERIFICATION-DESIGN.md, "Active probe + canary"). A broker goroutine
// periodically enqueues a broker-ORIGINATED canary job to each online node
// through the existing tunnel - a fixed deterministic prompt at temperature 0
// with small max_tokens - and measures:
//
//   - TTFT (time-to-first-result, best-effort: non-stream, so it is the full
//     round-trip; documented as a coarse liveness/latency signal),
//   - clean tok/s (free of organic queueing),
//   - a canary fingerprint check (liveness + a coarse model-size sniff).
//
// Probes are NOT billed (the result is discarded, no wallet is touched) and must
// not interfere with real traffic: low frequency, and nodes currently in-flight
// are skipped. Results feed the per-node trustState (probe.go writes it, pick
// reads it, the market view surfaces ttft + quality).

// canaryFingerprint is one deterministic probe challenge: a short instruction any
// correctly-deployed instruction model answers the same way at temperature 0, plus
// the stable token the answer must contain (case-folded). We search for the expected
// token as a SUBSTRING anywhere in the VISIBLE content (coarse on purpose -
// exact-string matching would be brittle to whitespace, reasoning preambles, and
// minor nondeterminism). Extracting the fingerprint is a STRONG positive signal, but
// a miss alone NEVER fails a responsive node: reasoning models legitimately wander or
// burn the whole budget on their reasoning channel before emitting the literal token.
// Only a transport error, a non-2xx, empty content, or a clearly wrong-family answer
// is a failure (see evalCanary for the liveness-vs-fingerprint split).
type canaryFingerprint struct {
	prompt string
	expect string
}

// canaryFingerprints is a small ROTATING set of deterministic challenges. Each
// round picks the next one (round-robin), so a node operator cannot hard-code a
// single canned answer to fake liveness - the prompt changes every probe, and the
// expected token with it. They are all short factual/format instructions a real
// instruction model answers identically at temperature 0, robust to GPU
// non-determinism. Keep them un-guessable as a SET, not just individually.
var canaryFingerprints = []canaryFingerprint{
	{prompt: "Reply with only the single word: BANANA", expect: "banana"},
	{prompt: "Reply with only the single word: ORANGE", expect: "orange"},
	{prompt: "Reply with only this exact word: PENGUIN", expect: "penguin"},
	{prompt: "Output only the number that is two plus three, as digits.", expect: "5"},
	{prompt: "Reply with only the uppercase word: TUNGSTEN", expect: "tungsten"},
	{prompt: "Reply with only the single word: SCARLET", expect: "scarlet"},
	{prompt: "Output only the result of seven minus four, as a digit.", expect: "3"},
	{prompt: "Reply with only this exact word: GRANITE", expect: "granite"},
}

// nextCanary returns the fingerprint for round n (round-robin over the set). Taking
// the round number keeps selection deterministic + testable and guarantees every
// fingerprint is exercised over a full cycle (no RNG-skew that could starve one).
func nextCanary(round uint64) canaryFingerprint {
	return canaryFingerprints[int(round%uint64(len(canaryFingerprints)))]
}

// Active-probe defaults. The probe is ON by default now (nodes get MEASURED before
// consumer traffic arrives, so the signal/pick are grounded the moment a node comes
// on air). Operators can still tune the cadence or turn it fully off via env.
// The PERFORMANCE probe (a real inference) is ADAPTIVE. A freshly-on-air node is
// probed at the FLOOR (ROGERAI_PROBE_INTERVAL); each idle round it survives without
// real traffic or fresh demand DOUBLES its personal interval up to the CEILING
// (ROGERAI_PROBE_CEILING), so a persistently-idle GPU collapses toward one probe
// every ~15m instead of every 30s. Real served traffic (a free measurement) and
// fresh demand (a /discover, /market, or a stale-candidate pick for the model) reset
// the backoff toward the floor so an actively-used or actively-browsed node stays
// fresh. NOTE: this is ONLY the expensive performance probe - cheap liveness (the
// heartbeat + nodeTTL) is fully decoupled and unchanged.
const (
	defaultProbeInterval = 30 * time.Second // ROGERAI_PROBE_INTERVAL default - the adaptive backoff FLOOR
	defaultProbeCeiling  = 15 * time.Minute // ROGERAI_PROBE_CEILING default - the idle backoff CAP
	defaultProbePerOwner = 4                // ROGERAI_PROBE_PER_OWNER default
	// canaryMaxTokens is the per-probe completion budget. Sized so a reasoning model
	// can emit its reasoning channel AND still land a short answer; a small budget
	// false-failed reasoning flagships that spent it all on reasoning tokens.
	canaryMaxTokens = 384
)

// probeConfig holds the active-probe wiring (env, see .env.example).
type probeConfig struct {
	interval time.Duration // ROGERAI_PROBE_INTERVAL seconds (0 = OFF; default 30s) - the backoff FLOOR
	// ceiling is the maximum per-node probe interval an idle node backs off to. The
	// loop still TICKS at the floor (the scheduling resolution); a backed-off node is
	// simply skipped until its much later next-due lands. Default 15m. Clamped >= floor.
	ceiling  time.Duration
	perOwner int    // max nodes of a single owner probed per round (0 = no cap)
	round    uint64 // monotonic round counter (rotates the canary + the per-owner sample)
}

// loadProbe reads the active-probe config. ON by default (30s floor -> 15m ceiling);
// set ROGERAI_PROBE_INTERVAL=0 to disable.
func loadProbe() probeConfig {
	interval := defaultProbeInterval
	if v := os.Getenv("ROGERAI_PROBE_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			interval = time.Duration(n) * time.Second // 0 = explicitly OFF
		}
	}
	ceiling := defaultProbeCeiling
	if v := os.Getenv("ROGERAI_PROBE_CEILING"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ceiling = time.Duration(n) * time.Second
		}
	}
	if ceiling < interval {
		ceiling = interval // a ceiling below the floor is meaningless: no backoff room
	}
	perOwner := defaultProbePerOwner
	if v := os.Getenv("ROGERAI_PROBE_PER_OWNER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			perOwner = n
		}
	}
	c := probeConfig{interval: interval, ceiling: ceiling, perOwner: perOwner}
	if c.enabled() {
		log.Printf("active probe: ENABLED (adaptive %s floor -> %s ceiling, doubling while idle; canary + TTFT + clean tok/s, unbilled; per-owner cap %d/round)", c.interval, c.ceiling, c.perOwner)
	} else {
		log.Printf("active probe: DISABLED (ROGERAI_PROBE_INTERVAL=0)")
	}
	return c
}

func (c probeConfig) enabled() bool { return c.interval > 0 }

// measurementStale reports whether a node's last measurement (probe or real traffic)
// is old enough to count as "not recently verified": older than the ceiling, the
// horizon an idle node backs off to. A never-measured node (zero time) is stale.
func (c probeConfig) measurementStale(lastMeasured, now time.Time) bool {
	if lastMeasured.IsZero() {
		return true
	}
	return now.Sub(lastMeasured) > c.ceiling
}

// stalenessFactor is a gentle recency/confidence multiplier (0.7..1.0) on the
// MEASURED signal terms. A node measured within the ceiling reads at full confidence
// (1.0); past the ceiling it earns a MODEST haircut that deepens linearly to a floor
// of 0.7 over one further ceiling-span, so a long-unmeasured node honestly reads "not
// recently verified" without cratering an otherwise-good idle node. A fresh
// measurement restores it to 1.0 immediately. A zero ceiling (probe off) => 1.0 (no
// staleness notion). age is now - lastMeasured.
func (c probeConfig) stalenessFactor(age time.Duration) float64 {
	if c.ceiling <= 0 || age <= c.ceiling {
		return 1.0
	}
	const floor = 0.7
	over := float64(age-c.ceiling) / float64(c.ceiling) // 0 at the horizon, 1 a ceiling later
	if over > 1 {
		over = 1
	}
	return 1.0 - (1.0-floor)*over
}

// backoffInterval is the per-node probe interval at backoff level lvl: the floor
// doubled lvl times, clamped to the ceiling. Level 0 = floor (freshly on air / just
// served real traffic / just demanded). Each idle round that passes a node over
// increments its level, so its effective cadence walks floor -> 2x -> 4x -> ... ->
// ceiling.
func (c probeConfig) backoffInterval(lvl int) time.Duration {
	d := c.interval
	for i := 0; i < lvl && d < c.ceiling; i++ {
		d *= 2
		if d <= 0 || d > c.ceiling { // overflow guard / clamp
			return c.ceiling
		}
	}
	if d > c.ceiling {
		d = c.ceiling
	}
	return d
}

// probeState is the per-node ADAPTIVE schedule for the expensive performance probe.
// It is the only state that makes idle probing lazy; liveness is untouched.
//
//   - nextDue: the earliest time this node is eligible for another performance probe.
//     The loop ticks at the floor but only probes nodes whose nextDue has passed.
//   - backoff: the current exponential level (0 = floor). Each idle probe round
//     increments it (so the interval doubles); real traffic or demand resets it to 0.
//   - lastMeasured: when this node's performance was last established by a PASSED probe
//     OR a real served request. Drives the staleness factor in the signal (market.go).
//
// Guarded by metricsMu (same lock as trust/tps), so it is consistent with the metrics
// it schedules around. Reset-on-restart is fine: a fresh broker just re-probes every
// node at the floor once and re-backs-off, which is the correct cold-start behaviour.
type probeState struct {
	nextDue      time.Time
	backoff      int
	lastMeasured time.Time
}

// probeSched returns the per-node schedule map, lazily initialised. Caller holds
// metricsMu.
func (b *broker) probeSchedLocked() map[string]*probeState {
	if b.probeSched == nil {
		b.probeSched = map[string]*probeState{}
	}
	return b.probeSched
}

// markMeasured records that a node's performance was just established for FREE by a
// real served request (the relay/stream settle path): reset its backoff to the floor
// and push the next probe out by one floor interval, so an actively-used node is
// barely probed. Also stamps lastMeasured so the signal reads it as freshly verified.
// Cheap + concurrency-safe; a no-op when the probe is disabled.
func (b *broker) markMeasured(nodeID string) {
	if !b.probe.enabled() {
		return
	}
	now := time.Now()
	b.metricsMu.Lock()
	sched := b.probeSchedLocked()
	st := sched[nodeID]
	if st == nil {
		st = &probeState{}
		sched[nodeID] = st
	}
	st.backoff = 0
	st.lastMeasured = now
	// We just measured it for free; the next probe is unnecessary until at least a
	// floor interval of silence, and is extended further as traffic keeps arriving.
	if due := now.Add(b.probe.interval); due.After(st.nextDue) {
		st.nextDue = due
	}
	b.metricsMu.Unlock()
}

// demandProbeSoonLocked is the just-in-time hook: a consumer is actively interested in
// a node (a /discover or /market browse, or a pick about to route to it on a STALE
// reading), so pull its next performance probe back toward the floor and reset the
// backoff. The probe is asynchronous - the in-flight browse/route is NOT blocked on it;
// it just refreshes the data for the next one. A node already due sooner is left alone.
// Caller holds metricsMu and gates on b.probe.enabled() (pick/market read metrics under
// that lock and schedule in the same critical section).
func (b *broker) demandProbeSoonLocked(nodeID string, now time.Time) {
	sched := b.probeSchedLocked()
	st := sched[nodeID]
	if st == nil {
		st = &probeState{}
		sched[nodeID] = st
	}
	st.backoff = 0
	if st.nextDue.IsZero() || st.nextDue.After(now) {
		st.nextDue = now // eligible on the next round (floor resolution)
	}
}

// measurementStalenessLocked returns the node's signal staleness-confidence factor
// (0.7..1.0; 1.0 = freshly measured within the ceiling). It folds the last-measured
// time through probeConfig.stalenessFactor so the market/discover signal MODESTLY
// discounts a long-unmeasured node. A node we have never measured (or a disabled
// probe) reads at full confidence here - it has no probe evidence to discount; the
// signal's neutral handling of unmeasured speed/latency already covers that case.
// Caller holds metricsMu.
func (b *broker) measurementStalenessLocked(nodeID string, now time.Time) float64 {
	if !b.probe.enabled() {
		return 1.0
	}
	st := b.probeSched[nodeID]
	if st == nil || st.lastMeasured.IsZero() {
		return 1.0 // never measured: nothing to discount (unmeasured terms are neutral)
	}
	return b.probe.stalenessFactor(now.Sub(st.lastMeasured))
}

// proberLoop ticks at the FLOOR interval - the scheduling resolution - and probes the
// nodes whose adaptive next-due has arrived. Started from main when the probe is
// enabled. Each round is jittered (see probeOnce) so a fleet that all came on air
// together is not probed in a synchronized burst.
// proberLoop runs probeOnce on a fixed cadence until stop is closed. The stop
// channel is a test seam: main passes nil (a nil channel case never fires, so the
// production select degenerates to "wait for the ticker forever" - byte-for-byte the
// old time.Tick loop), while a test passes a closeable channel to drive + halt it.
func (b *broker) proberLoop(stop <-chan struct{}) {
	t := time.NewTicker(b.probe.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.probeOnce()
		}
	}
}

// probeJitter is the cap on the per-round delay window added before a round's
// probes fire. Spreading the round over a window (rather than firing every probe at
// the tick) avoids a thundering herd against the nodes (and the broker tunnel) each
// interval. The effective window is min(probeJitter, interval/2) so it never bleeds
// into the next round (and stays small for short test intervals).
const probeJitter = 5 * time.Second

// jitterWindow is the effective per-round jitter span for this config.
func (c probeConfig) jitterWindow() time.Duration {
	w := probeJitter
	if half := c.interval / 2; half < w {
		w = half
	}
	if w < 0 {
		w = 0
	}
	return w
}

// probeOnce snapshots the online, idle nodes and probes a per-owner-capped sample
// of them. Busy nodes (in-flight > 0) are skipped so probes never compete with
// paying traffic. The per-owner cap + per-round rotation mean a large owner is
// sampled a few nodes at a time instead of all at once; per-probe jitter spreads
// the round so there is no synchronized burst.
func (b *broker) probeOnce() {
	round := atomic.AddUint64(&b.probe.round, 1) - 1
	fp := nextCanary(round)

	type target struct {
		node  protocol.NodeRegistration
		model string
	}

	// Group eligible (online + idle) nodes by owner so the per-owner cap is applied
	// per group. Owner identity is the account a node is bound to (AccountOfNode);
	// when there is no store (tests) each node is its own owner group.
	type cand struct {
		node  protocol.NodeRegistration
		model string
		owner string
	}
	now := time.Now()
	var cands []cand
	b.mu.Lock()
	b.metricsMu.Lock()
	sched := b.probeSchedLocked()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		if b.inflight[n.NodeID] > 0 {
			continue // skip a node that is currently serving real traffic
		}
		// Adaptive schedule: a node is only probed when its personal next-due has
		// arrived. A freshly-seen node has no state yet => due immediately (floor); an
		// idle node backs off (nextDue pushed out each round it is probed) toward the
		// ceiling; real traffic / demand reset it (markMeasured / demandProbeSoonLocked).
		st := sched[n.NodeID]
		if st == nil {
			st = &probeState{} // first sight: due now (zero nextDue), backoff 0
			sched[n.NodeID] = st
		}
		if !st.nextDue.IsZero() && st.nextDue.After(now) {
			continue // backed off: not due yet this round
		}
		var model string
		for _, o := range n.Offers {
			model = o.Model
			break // one probe per node per round is enough for liveness
		}
		if model == "" {
			continue
		}
		owner := n.NodeID // fallback: node is its own owner group
		if b.db != nil {
			if acct, ok, _ := b.db.AccountOfNode(n.NodeID); ok && acct != "" {
				owner = acct
			}
		}
		cands = append(cands, cand{node: n, model: model, owner: owner})
	}
	b.metricsMu.Unlock()
	b.mu.Unlock()

	// Stable order so the per-owner rotation is deterministic across rounds: nodes of
	// the same owner are visited in node-id order, and the round number rotates the
	// window so a different slice of a big owner's fleet is probed each round.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].owner != cands[j].owner {
			return cands[i].owner < cands[j].owner
		}
		return cands[i].node.NodeID < cands[j].node.NodeID
	})

	// Per-owner cap with rotation: for each owner, take perOwner nodes starting at a
	// round-dependent offset, so over successive rounds the whole fleet is covered.
	byOwner := map[string][]cand{}
	var owners []string
	for _, c := range cands {
		if _, seen := byOwner[c.owner]; !seen {
			owners = append(owners, c.owner)
		}
		byOwner[c.owner] = append(byOwner[c.owner], c)
	}
	var targets []target
	for _, ow := range owners {
		group := byOwner[ow]
		cap := b.probe.perOwner
		if cap <= 0 || cap >= len(group) {
			for _, c := range group {
				targets = append(targets, target{node: c.node, model: c.model})
			}
			continue
		}
		off := int(round % uint64(len(group)))
		for i := 0; i < cap; i++ {
			c := group[(off+i)%len(group)]
			targets = append(targets, target{node: c.node, model: c.model})
		}
	}

	// Advance the adaptive backoff for the nodes we are about to probe THIS round
	// (only the ones that survived the per-owner cap - a node deferred by the cap keeps
	// its earlier next-due and is picked up on a following round). Each probe round a
	// node sits through without real traffic doubles its personal interval up to the
	// ceiling, so a persistently-idle node collapses toward the ~15m cap. markMeasured
	// (real traffic) and demandProbeSoonLocked (browse/route) reset this.
	b.metricsMu.Lock()
	for _, t := range targets {
		st := sched[t.node.NodeID]
		if st == nil {
			st = &probeState{}
			sched[t.node.NodeID] = st
		}
		st.nextDue = now.Add(b.probe.backoffInterval(st.backoff))
		if st.backoff < 64 { // cap the level (backoffInterval already clamps to ceiling)
			st.backoff++
		}
	}
	b.metricsMu.Unlock()

	// Probe nodes concurrently: each probeNode blocks waiting for its result, so
	// running them in parallel keeps one slow/dead node from stalling the round.
	// Each probe waits a small random slice of the jitter window first so the round
	// is spread out (no thundering herd) rather than fired all at the tick.
	window := int64(b.probe.jitterWindow())
	for _, t := range targets {
		t := t
		var delay time.Duration
		if window > 0 {
			delay = time.Duration(rand.Int63n(window + 1))
		}
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			b.probeNode(t.node, t.model, fp)
		}()
	}
}

// probeNode enqueues one canary job to a node and records the result. It reuses
// the relay tunnel (jobs channel + result waiter) but bills NOTHING: the result
// body and receipt are discarded after measuring. fp is the rotating fingerprint
// for this round (so the challenge changes round to round - a node cannot hard-code
// the answer). User="probe" marks it unbilled; settleRequest/earnings are never
// touched on this path.
func (b *broker) probeNode(node protocol.NodeRegistration, model string, fp canaryFingerprint) {
	b.mu.Lock()
	t := b.tunnels[node.NodeID]
	mi := b.multiInstance && b.shared != nil
	b.mu.Unlock()
	// Single-instance needs a real local tunnel; multi-instance dispatches over the bus
	// (the poller may be on a PEER instance), so a nil/stub local tunnel is fine there.
	if t == nil && !mi {
		return
	}

	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": fp.prompt}},
		"temperature": 0,
		// canaryMaxTokens leaves room for a REASONING model (gpt-oss, deepseek, ...)
		// to emit its reasoning/harmony channel AND a short answer. A tiny budget
		// (the old 16) was exhausted by the reasoning channel before any answer
		// surfaced, false-failing perfectly healthy flagships. Liveness no longer
		// depends on the fingerprint landing, but the larger budget gives reasoning
		// models a fair shot at producing the literal answer (the strong signal).
		"max_tokens": canaryMaxTokens,
	})
	job := protocol.Job{ID: protocol.NewRequestID(), User: "probe", Body: body}
	start := time.Now()

	if mi {
		// MULTI-INSTANCE: the provider may be long-polling a PEER instance, so dispatch +
		// await over the Valkey bus exactly as relay/relayStream do. A local-only
		// t.jobs send would enqueue into a stub channel nobody drains whenever the poller
		// is on another instance, time out after 30s, and FALSE-FAIL a perfectly healthy
		// node (deprioritizing it / churning trust). busDispatchJob delivers to the poller
		// on whichever instance it lives. A dispatch error (no subscriber / bus blip) is not
		// a node-quality signal, so it skips the round rather than failing (see derr below).
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch, dcancel, derr := b.busDispatchJob(ctx, node.NodeID, job)
		if dcancel != nil {
			defer dcancel()
		}
		if derr != nil {
			// A dispatch that never reached the node is NOT evidence about the node's
			// quality, so SKIP this round (don't touch trust) rather than record a failure.
			// Both cases are transient and self-correct next interval:
			//   - errNoPoller (delivered==0): nobody subscribed at this instant - usually the
			//     node briefly BETWEEN long-polls (~25s re-poll gap), not death. Recording
			//     probeDead here is the exact false-positive bus dispatch was meant to remove;
			//     true death is caught by heartbeat liveness (markSeen TTL), not the probe.
			//   - any other bus error: a transient Valkey blip would otherwise mark the WHOLE
			//     fleet's probes dead at once. Skip and retry.
			return
		}
		select {
		case raw, ok := <-ch:
			if !ok {
				b.recordProbe(node.NodeID, probeDead, 0, 0, false)
				return
			}
			var res protocol.JobResult
			if json.Unmarshal(raw, &res) != nil {
				b.recordProbe(node.NodeID, probeDead, 0, 0, false)
				return
			}
			elapsed := time.Since(start)
			outcome, tps, matched := b.evalCanary(res, elapsed, fp)
			b.recordProbe(node.NodeID, outcome, float64(elapsed.Milliseconds()), tps, matched)
		case <-time.After(30 * time.Second):
			b.recordProbe(node.NodeID, probeDead, 0, 0, false)
		}
		return
	}

	// SINGLE-INSTANCE: dispatch through the local tunnel and await the result locally.
	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()

	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		// Could not even enqueue: transport/backpressure failure, not a fingerprint
		// miss. This is a real liveness failure.
		b.recordProbe(node.NodeID, probeDead, 0, 0, false)
		return
	}

	select {
	case res := <-resCh:
		elapsed := time.Since(start)
		outcome, tps, matched := b.evalCanary(res, elapsed, fp)
		b.recordProbe(node.NodeID, outcome, float64(elapsed.Milliseconds()), tps, matched)
	case <-time.After(30 * time.Second):
		b.recordProbe(node.NodeID, probeDead, 0, 0, false)
	}
}

// probeOutcome is the trichotomy evalCanary resolves a probe into. The key fix
// (see VERIFICATION-DESIGN.md): LIVENESS is separated from the FINGERPRINT. A node
// that returns a 2xx with non-empty content is ALIVE and counts as verified-serving,
// even when the literal fingerprint answer cannot be extracted - reasoning models
// (gpt-oss, deepseek) legitimately spend their budget reasoning and never emit the
// bare token. Only a transport/timeout error, a non-2xx, EMPTY content, or a clearly
// WRONG-family answer is a failure.
type probeOutcome int

const (
	probeDead  probeOutcome = iota // transport/timeout/non-2xx/empty: real failure
	probeAlive                     // responded with content; fingerprint inconclusive
	probePass                      // responded AND the expected fingerprint was found
	probeWrong                     // responded but a clearly WRONG-family answer: failure
)

func (o probeOutcome) failed() bool { return o == probeDead || o == probeWrong }

// evalCanary classifies a probe result and computes a clean tok/s sample. It returns
// the outcome, the tok/s (measured whenever the node responded, regardless of the
// fingerprint), and whether the exact fingerprint matched (a strong positive signal).
//
//   - non-2xx / empty content => probeDead (real failure).
//   - expected token present anywhere in the visible content => probePass.
//   - a DIFFERENT canary's answer present while ours is absent => probeWrong
//     (clearly wrong-family: the node is answering, but with the wrong fact).
//   - responded with content but neither => probeAlive (alive, fingerprint
//     inconclusive). This is the reasoning-model case: NOT a failure.
func (b *broker) evalCanary(res protocol.JobResult, elapsed time.Duration, fp canaryFingerprint) (outcome probeOutcome, tps float64, matched bool) {
	if res.Status < 200 || res.Status >= 300 {
		return probeDead, 0, false
	}
	// Visible answer text is what the fingerprint is checked against. Reasoning text
	// (the harmony/think channel) is a liveness signal only - a reasoning model can
	// burn the whole budget there and leave content empty, which is still ALIVE.
	text := completionText(res.Body)
	reasoning := probeReasoningText(res.Body)
	if strings.TrimSpace(text) == "" && strings.TrimSpace(reasoning) == "" {
		return probeDead, 0, false // truly empty body: dead
	}
	// The node responded => it is ALIVE. Measure tok/s now, before any fingerprint
	// reasoning, so latency/speed are recorded for every responsive node.
	if ct := res.Receipt.CompletionTokens; ct > 0 {
		if s := elapsed.Seconds(); s > 0 {
			tps = float64(ct) / s
		}
	}
	low := strings.ToLower(text)
	if strings.Contains(low, fp.expect) {
		return probePass, tps, true // strong positive signal
	}
	// Wrong-family: the visible answer contains a DIFFERENT canary's expected token
	// (a mutually-exclusive answer to a deterministic prompt) but not ours. A
	// reasoning preamble that merely mentions other words is unlikely to be flagged
	// because the prompts demand a single bare word and the tokens are distinct.
	if canaryWrongFamily(low, fp) {
		return probeWrong, tps, false
	}
	// Responded, but the fingerprint is inconclusive (reasoning model wandered or
	// burned the budget reasoning). ALIVE, not a failure.
	return probeAlive, tps, false
}

// canaryWrongFamily reports whether the visible content asserts a DIFFERENT canary's
// answer while omitting the expected one. It catches a node confidently answering the
// wrong fact (wrong model / refusal proxy echoing a canned word) without false-failing
// a reasoning model that simply never reached the literal answer.
//
// It is deliberately CONSERVATIVE: it only considers DISTINCTIVE other-canary tokens
// (a length>=4 alphabetic word like "banana"/"penguin"). Short or numeric expected
// tokens ("5", "3") are skipped, because a reasoning preamble incidentally contains
// digits ("step 5") and we must never let that false-fail a responsive node. A miss
// here just yields probeAlive (alive), which is the safe default.
func canaryWrongFamily(low string, fp canaryFingerprint) bool {
	for _, other := range canaryFingerprints {
		if other.expect == fp.expect {
			continue // same answer token (a different prompt may share it); not "wrong"
		}
		if !distinctiveCanaryToken(other.expect) {
			continue // too short/numeric to assert wrongness from a substring match
		}
		if strings.Contains(low, other.expect) {
			return true
		}
	}
	return false
}

// probeReasoningText extracts a reasoning model's THINKING channel from a chat
// completion (OpenAI-compatible reasoning servers expose it as choices[].message.
// reasoning_content or .reasoning). It is used by the probe ONLY as a liveness
// signal: a node that returned reasoning tokens responded even if it never emitted a
// final answer. It is intentionally separate from completionText (which feeds billing
// recount and must stay limited to the visible/billable content).
func probeReasoningText(body []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ""
	}
	var out strings.Builder
	for _, c := range resp.Choices {
		out.WriteString(c.Message.ReasoningContent)
		out.WriteString(c.Message.Reasoning)
	}
	return out.String()
}

// distinctiveCanaryToken reports whether an expected token is unique enough that its
// mere presence in the content is strong evidence of a specific (wrong) answer: a
// word of >=4 letters, all alphabetic. Numeric/short tokens are not distinctive.
func distinctiveCanaryToken(tok string) bool {
	if len(tok) < 4 {
		return false
	}
	for _, r := range tok {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// recordProbe folds one probe outcome into the node's trustState (EWMA ttft + tps,
// canary verdict, failure streak). The KEY rule: a node that RESPONDED with content
// is alive => verified-serving (probeOK true, streak reset), whether or not the exact
// fingerprint landed. Only probeDead/probeWrong increment the streak that pick uses to
// deprioritize a node. ttft/tps are recorded for every responsive probe. matched marks
// a clean fingerprint extraction (a strong positive), surfaced only in the log.
func (b *broker) recordProbe(nodeID string, outcome probeOutcome, ttftMs, tps float64, matched bool) {
	alive := !outcome.failed()

	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	tq.probes++
	tq.probed = true
	tq.probeOK = alive
	if alive {
		tq.probeFails = 0
		if ttftMs > 0 {
			tq.ttftMs = ewma(tq.ttftMs, ttftMs, 0.3)
		}
		if tps > 0 {
			tq.probeTPS = ewma(tq.probeTPS, tps, 0.3)
		}
		// A live probe is a fresh measurement: stamp lastMeasured so the signal's
		// staleness factor restores this node to full confidence (market.go).
		sched := b.probeSchedLocked()
		st := sched[nodeID]
		if st == nil {
			st = &probeState{}
			sched[nodeID] = st
		}
		st.lastMeasured = time.Now()
	}
	if !alive {
		tq.probeFails++
	}
	b.trust[nodeID] = tq
	fails := tq.probeFails
	b.metricsMu.Unlock()

	if alive {
		if tps > 0 {
			b.updateTPS(nodeID, tps) // fold the clean sample into the speed band
		}
		if matched {
			log.Printf("probe node=%s OK ttft=%.0fms tps=%.1f (fingerprint matched)", nodeID, ttftMs, tps)
		} else {
			// Responded with content but the fingerprint was inconclusive (e.g. a
			// reasoning model). Still ALIVE / verified-serving: not a failure.
			log.Printf("probe node=%s ALIVE ttft=%.0fms tps=%.1f (responded; fingerprint inconclusive)", nodeID, ttftMs, tps)
		}
	} else {
		reason := "no response/non-2xx/empty"
		if outcome == probeWrong {
			reason = "wrong-family answer"
		}
		log.Printf("probe node=%s FAIL (consecutive=%d) - canary/liveness: %s", nodeID, fails, reason)
	}
}

// ewma updates an EWMA, seeding it on the first sample (cur == 0).
func ewma(cur, sample, alpha float64) float64 {
	if cur <= 0 {
		return sample
	}
	return alpha*sample + (1-alpha)*cur
}

// probeDeadStreak is the SUSTAINED consecutive-probe-failure count past which a node's
// model is treated as NOT SERVING (its upstream is down/unloaded - it returns fast
// 5xx/empty). At/above this, the node is EXCLUDED from pick (a relay returns a clean "no
// station serving" instead of dispatching into a 504) and shown OFFLINE on /discover +
// /market (so a consumer never tunes into a dead channel). It is well above the inline
// deprioritize bar (probeFails>=3): a node must keep failing to be declared dead, not slow
// once. It still heartbeats, so the proberLoop keeps probing it; a single OK resets the
// streak and it becomes serving again automatically.
// (Callers test probeFails >= probeDeadStreak inline, reusing the trustState they already
// hold under metricsMu - no separate accessor needed, and no name clash with the probeDead
// probeOutcome.)
const probeDeadStreak = 6
