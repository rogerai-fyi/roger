package main

import (
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
const (
	defaultProbeInterval = 30 * time.Second // ROGERAI_PROBE_INTERVAL default (seconds)
	defaultProbePerOwner = 4                // ROGERAI_PROBE_PER_OWNER default
	// canaryMaxTokens is the per-probe completion budget. Sized so a reasoning model
	// can emit its reasoning channel AND still land a short answer; a small budget
	// false-failed reasoning flagships that spent it all on reasoning tokens.
	canaryMaxTokens = 384
)

// probeConfig holds the active-probe wiring (env, see .env.example).
type probeConfig struct {
	interval time.Duration // ROGERAI_PROBE_INTERVAL seconds (0 = OFF; default 30s)
	// perOwner caps how many of a single owner's nodes are probed per round, so a
	// 20-node owner is sampled (a few nodes/round, rotating) instead of hammered.
	// 0 = no per-owner cap.
	perOwner int
	round    uint64 // monotonic round counter (rotates the canary + the per-owner sample)
}

// loadProbe reads the active-probe config. ON by default (30s); set
// ROGERAI_PROBE_INTERVAL=0 to disable.
func loadProbe() probeConfig {
	interval := defaultProbeInterval
	if v := os.Getenv("ROGERAI_PROBE_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			interval = time.Duration(n) * time.Second // 0 = explicitly OFF
		}
	}
	perOwner := defaultProbePerOwner
	if v := os.Getenv("ROGERAI_PROBE_PER_OWNER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			perOwner = n
		}
	}
	c := probeConfig{interval: interval, perOwner: perOwner}
	if c.enabled() {
		log.Printf("active probe: ENABLED (every %s; canary + TTFT + clean tok/s, unbilled; per-owner cap %d/round)", c.interval, c.perOwner)
	} else {
		log.Printf("active probe: DISABLED (ROGERAI_PROBE_INTERVAL=0)")
	}
	return c
}

func (c probeConfig) enabled() bool { return c.interval > 0 }

// proberLoop runs every interval and probes the idle nodes once per round. Started
// from main when the probe is enabled. Each round is jittered (see probeOnce) so a
// fleet that all came on air together is not probed in a synchronized burst.
func (b *broker) proberLoop() {
	for range time.Tick(b.probe.interval) {
		b.probeOnce()
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
	var cands []cand
	b.mu.Lock()
	b.metricsMu.Lock()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		if b.inflight[n.NodeID] > 0 {
			continue // skip a node that is currently serving real traffic
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
	b.mu.Unlock()
	if t == nil {
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

	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()

	start := time.Now()
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
	} else {
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

// probeTTFT / probeQuality expose per-node probe results for the market/offer
// views and for pick. Concurrency-safe.
func (b *broker) probeTTFT(nodeID string) float64 {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.trust[nodeID].ttftMs
}

// probeFailing reports whether a node has failed enough consecutive probes to be
// deprioritized in pick. The streak rule (not a single failure) avoids penalizing
// a node that was merely busy on one probe.
func (b *broker) probeFailing(nodeID string) bool {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.trust[nodeID].probeFails >= 3
}
