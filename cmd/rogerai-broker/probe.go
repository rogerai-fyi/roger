package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
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

// canaryPrompt is the fixed, deterministic probe prompt. It is a short factual
// instruction any correctly-deployed instruction model answers the same way at
// temperature 0, so the answer fingerprint is robust to GPU non-determinism. A
// wrong model, a filter/refusal proxy, or a dead upstream fails it.
const canaryPrompt = "Reply with only the single word: BANANA"

// canaryExpect is the stable token the canary answer must contain (case-folded).
// Kept coarse on purpose - exact-string matching would be brittle to whitespace
// and minor nondeterminism; we check for the expected token as a substring.
const canaryExpect = "banana"

// probeConfig holds the active-probe wiring (env, see .env.example).
type probeConfig struct {
	interval time.Duration // ROGERAI_PROBE_INTERVAL seconds (0 = off)
}

// loadProbe reads the active-probe config. Off by default (interval 0).
func loadProbe() probeConfig {
	var secs int
	if v := os.Getenv("ROGERAI_PROBE_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	c := probeConfig{interval: time.Duration(secs) * time.Second}
	if c.enabled() {
		log.Printf("active probe: enabled (every %s; canary + TTFT + clean tok/s, unbilled)", c.interval)
	} else {
		log.Printf("active probe: DISABLED (set ROGERAI_PROBE_INTERVAL seconds to enable)")
	}
	return c
}

func (c probeConfig) enabled() bool { return c.interval > 0 }

// proberLoop runs every interval and probes each online, idle node once. Started
// from main when ROGERAI_PROBE_INTERVAL > 0.
func (b *broker) proberLoop() {
	for range time.Tick(b.probe.interval) {
		b.probeOnce()
	}
}

// probeOnce snapshots the online nodes and probes each idle one. Busy nodes
// (in-flight > 0) are skipped so probes never compete with paying traffic.
func (b *broker) probeOnce() {
	type target struct {
		node  protocol.NodeRegistration
		model string
	}
	var targets []target
	b.mu.Lock()
	b.metricsMu.Lock()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		if b.inflight[n.NodeID] > 0 {
			continue // skip a node that is currently serving real traffic
		}
		for _, o := range n.Offers {
			targets = append(targets, target{node: n, model: o.Model})
			break // one probe per node per round is enough for liveness
		}
	}
	b.metricsMu.Unlock()
	b.mu.Unlock()

	// Probe nodes concurrently: each probeNode blocks waiting for its result, so
	// running them in parallel keeps one slow/dead node from stalling the round.
	for _, t := range targets {
		go b.probeNode(t.node, t.model)
	}
}

// probeNode enqueues one canary job to a node and records the result. It reuses
// the relay tunnel (jobs channel + result waiter) but bills NOTHING: the result
// body and receipt are discarded after measuring.
func (b *broker) probeNode(node protocol.NodeRegistration, model string) {
	b.mu.Lock()
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	if t == nil {
		return
	}

	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": canaryPrompt}},
		"temperature": 0,
		"max_tokens":  16,
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
		b.recordProbe(node.NodeID, false, 0, 0)
		return
	}

	select {
	case res := <-resCh:
		elapsed := time.Since(start)
		ok, tps := b.evalCanary(res, elapsed)
		b.recordProbe(node.NodeID, ok, float64(elapsed.Milliseconds()), tps)
	case <-time.After(30 * time.Second):
		b.recordProbe(node.NodeID, false, 0, 0)
	}
}

// evalCanary checks a probe result against the canary fingerprint and computes a
// clean tok/s sample. The fingerprint is deliberately COARSE (best-effort): a
// 2xx status, non-empty completion, and the expected token present. This catches
// dead / filtered / refusal-wall / blatantly-wrong-model nodes; a same-family
// quant that still answers short prompts correctly is NOT caught here (that is
// L2's job, documented).
func (b *broker) evalCanary(res protocol.JobResult, elapsed time.Duration) (ok bool, tps float64) {
	if res.Status < 200 || res.Status >= 300 {
		return false, 0
	}
	text := completionText(res.Body)
	if strings.TrimSpace(text) == "" {
		return false, 0
	}
	if !strings.Contains(strings.ToLower(text), canaryExpect) {
		return false, 0
	}
	if ct := res.Receipt.CompletionTokens; ct > 0 {
		if s := elapsed.Seconds(); s > 0 {
			tps = float64(ct) / s
		}
	}
	return true, tps
}

// recordProbe folds one probe outcome into the node's trustState (EWMA ttft +
// tps, canary pass/fail, failure streak). A failure increments a streak used by
// pick to deprioritize repeatedly-failing nodes; a pass resets it. Probe tok/s
// also feeds the shared TPS EWMA so the market speed band reflects clean samples.
func (b *broker) recordProbe(nodeID string, ok bool, ttftMs, tps float64) {
	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	tq.probes++
	tq.probed = true
	tq.probeOK = ok
	if ok {
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

	if ok {
		if tps > 0 {
			b.updateTPS(nodeID, tps) // fold the clean sample into the speed band
		}
		log.Printf("probe node=%s OK ttft=%.0fms tps=%.1f", nodeID, ttftMs, tps)
	} else {
		log.Printf("probe node=%s FAIL (consecutive=%d) - canary/liveness", nodeID, fails)
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
