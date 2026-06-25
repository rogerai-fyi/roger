package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// recount.go is the broker side of L1 - the independent token re-count (see
// docs-internal/VERIFICATION-DESIGN.md, "L1"). After a response SETTLES, the
// broker (in a goroutine, OFF the hot path) posts the completion text to the
// tokenizer-sidecar and reconciles the sidecar's count against the node's
// self-reported completion_tokens. A node that over-reports past a tolerance
// band (exact re-counts only) accrues a per-node DISCREPANCY against its trust
// score and is logged. Settlement has already happened, so for now this is a
// FLAG + accumulate; enforced re-bill/refund lands with async settlement.

// recountConfig holds the L1 re-count wiring (env, see .env.example).
type recountConfig struct {
	url       string  // TOKENIZER_URL (empty = disabled)
	tolerance float64 // ROGERAI_RECOUNT_TOLERANCE (default 0.02 = 2%)
	client    *http.Client
}

// loadRecount reads the L1 re-count config. Disabled (no-op) when TOKENIZER_URL
// is unset, so the broker runs fine with no sidecar.
func loadRecount() recountConfig {
	c := recountConfig{
		url:       os.Getenv("TOKENIZER_URL"),
		tolerance: 0.02,
		client:    &http.Client{Timeout: 4 * time.Second},
	}
	if v := os.Getenv("ROGERAI_RECOUNT_TOLERANCE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			c.tolerance = f
		}
	}
	if c.url == "" {
		log.Printf("L1 re-count: DISABLED (set TOKENIZER_URL to the tokenizer-sidecar, e.g. http://127.0.0.1:9099)")
	} else {
		log.Printf("L1 re-count: enabled via %s (tolerance=%.0f%%)", c.url, c.tolerance*100)
	}
	return c
}

func (c recountConfig) enabled() bool { return c.url != "" }

// sidecarCount asks the tokenizer-sidecar to count text under model. Returns the
// token count and whether the count was exact.
func (c recountConfig) sidecarCount(model, text string) (tokens int, exact bool, ok bool) {
	body, _ := json.Marshal(map[string]string{"model": model, "text": text})
	resp, err := c.client.Post(c.url+"/count", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, false
	}
	var out struct {
		Tokens int  `json:"tokens"`
		Exact  bool `json:"exact"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0, false, false
	}
	return out.Tokens, out.Exact, true
}

// settleRecount runs ONE broker re-count of the completion and returns the completion
// token count to BILL: min(claimed, brokerRecount) when an EXACT re-count exists (P0-2,
// capping an over-reporting node at settle), else `claimed` unchanged (re-count
// disabled / sidecar unreachable / heuristic-only / node under-reported - we never
// inflate a node's claim, and the coarse heuristic is too imprecise to bill on). It
// ALSO folds the sample into the node's trust state + the promotion-hold flag in a
// goroutine (OFF the hot path), reusing this single sidecar result so the relay path
// never double-calls the sidecar. Returns `claimed` immediately when re-count is off.
func (b *broker) settleRecount(nodeID, requestID, model, completion string, claimed int) int {
	if !b.recount.enabled() || completion == "" || claimed <= 0 {
		return claimed
	}
	recounted, exact, ok := b.recount.sidecarCount(model, completion)
	if !ok {
		return claimed // sidecar down: fail open, bill the claim, do not penalize
	}
	// Trust scoring + the P0-2 promotion-hold flag, off the hot path (observeRecount
	// takes the lock + may write the recount_holds row).
	go b.observeRecount(nodeID, requestID, claimed, recounted, exact)
	if exact && recounted > 0 && recounted < claimed {
		return recounted // settle on the smaller, broker-verified count
	}
	return claimed
}

// settleRecountPrompt is the INPUT twin of settleRecount: it returns the prompt
// (input) token count to BILL, capping an over-reporting node on the input axis the
// same way settleRecount caps the output axis. It has TWO defenses:
//
//  1. A HARD, fail-CLOSED byte floor independent of the sidecar: no tokenizer can emit
//     more tokens than the prompt has UTF-8 bytes, so a claim ABOVE len(body) bytes is
//     arithmetically impossible. We clamp it to the byte count AND flag the owner for an
//     immediate (zero-doubt) strike. This holds even with NO tokenizer sidecar, closing
//     the largest input-inflation case outright.
//  2. When a sidecar is configured, the same exact-recount cap as the output axis:
//     bill min(claimed, brokerRecount) and fold the discrepancy into the SAME trust /
//     promotion-hold path (observeRecountInput), so an input over-report trips the hold
//     exactly like a completion over-report.
//
// It never inflates a claim (we only ever bill the lesser count), and it returns the
// claim unchanged when re-count is off and the byte floor was not breached.
func (b *broker) settleRecountPrompt(nodeID, requestID, model, prompt string, claimed, bodyLen int) int {
	// Defense 1: the zero-doubt byte floor (no sidecar needed).
	if bodyLen > 0 && claimed > bodyLen {
		b.flagImpossibleInput(nodeID, requestID, claimed, bodyLen)
		claimed = bodyLen // clamp to the only physically-possible upper bound
	}
	// Defense 2: the sidecar input re-count (when configured).
	if !b.recount.enabled() || prompt == "" || claimed <= 0 {
		return claimed
	}
	recounted, exact, ok := b.recount.sidecarCount(model, prompt)
	if !ok {
		return claimed // sidecar down: the byte floor above is the fail-closed backstop
	}
	go b.observeRecountInput(nodeID, requestID, claimed, recounted, exact)
	if exact && recounted > 0 && recounted < claimed {
		return recounted // settle on the smaller, broker-verified input count
	}
	return claimed
}

// observeRecountInput folds one INPUT re-count into the node's trust state, mirroring
// observeRecount but on the prompt axis. Only an EXACT re-count can flag a discrepancy.
// An input over-report past tolerance records a discrepancy, holds the node's lots from
// promotion (the SAME machinery as the output axis), and accrues an owner strike.
func (b *broker) observeRecountInput(nodeID, requestID string, claimed, recounted int, exact bool) {
	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	tq.recounts++
	tq.lastClaimed = claimed
	tq.lastRecount = recounted
	tq.lastExact = exact
	flagged := false
	if exact && recounted > 0 && claimed > 0 {
		over := float64(claimed-recounted) / float64(recounted)
		if over > b.recount.tolerance {
			tq.discrepancies++
			flagged = true
		}
	}
	b.trust[nodeID] = tq
	disc := tq.discrepancies
	total := tq.recounts
	b.metricsMu.Unlock()

	if flagged {
		if b.db != nil {
			if err := b.db.SetNodeRecountHold(nodeID, true); err != nil {
				log.Printf("L1: SetNodeRecountHold(%s) failed: %v (lots may still auto-promote)", nodeID, err)
			}
		}
		// Owner-keyed strike (anti-rotation): an input over-report is an accumulating
		// signal toward warn/ban, with the claimed-vs-recount evidence bound to the owner.
		b.flagRecountOver(nodeID, requestID, "input", claimed, recounted)
		log.Printf("L1 INPUT DISCREPANCY node=%s claimed=%d recount=%d tol=%.0f%% (node discrepancies=%d/%d) - flagged + earnings HELD from promotion pending review",
			nodeID, claimed, recounted, b.recount.tolerance*100, disc, total)
	}
}

// recountAsync re-counts the completion off the hot path and reconciles it
// against the node's claim. Safe to call as `go b.recountAsync(...)`; it is a
// no-op when re-count is disabled. It never touches the settle path or the
// already-signed receipt - it only updates per-node trust counters and logs.
func (b *broker) recountAsync(nodeID, model, completion string, claimed int) {
	if !b.recount.enabled() || completion == "" {
		return
	}
	tokens, exact, ok := b.recount.sidecarCount(model, completion)
	if !ok {
		return // sidecar down/unreachable: fail open, do not penalize the node
	}
	b.observeRecount(nodeID, "", claimed, tokens, exact)
}

// observeRecount folds one re-count into the node's trust state. Only EXACT
// re-counts can flag a discrepancy (the heuristic is an outlier gate, too coarse
// to penalize on). A discrepancy is recorded when the node's claimed completion
// tokens exceed the re-count by more than the tolerance band.
func (b *broker) observeRecount(nodeID, requestID string, claimed, recounted int, exact bool) {
	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	tq.recounts++
	tq.lastClaimed = claimed
	tq.lastRecount = recounted
	tq.lastExact = exact
	flagged := false
	if exact && recounted > 0 && claimed > 0 {
		// Over-reporting only: claimed materially ABOVE our independent count.
		over := float64(claimed-recounted) / float64(recounted)
		if over > b.recount.tolerance {
			tq.discrepancies++
			flagged = true
		}
	}
	b.trust[nodeID] = tq
	disc := tq.discrepancies
	total := tq.recounts
	b.metricsMu.Unlock()

	if flagged {
		// P0-2: hold this node's earning lots from auto-promoting to payable until the
		// discrepancy is reviewed (an over-reporting node must not cash out on schedule).
		// Idempotent; persisted so the hold survives a broker restart.
		if b.db != nil {
			if err := b.db.SetNodeRecountHold(nodeID, true); err != nil {
				log.Printf("L1: SetNodeRecountHold(%s) failed: %v (lots may still auto-promote)", nodeID, err)
			}
		}
		// Owner-keyed strike (anti-rotation): an output over-report accrues toward
		// warn/ban with the claimed-vs-recount evidence bound to the owner account. A
		// requestID is present on the settle path (the async probe path passes "").
		if requestID != "" {
			b.flagRecountOver(nodeID, requestID, "output", claimed, recounted)
		}
		log.Printf("L1 DISCREPANCY node=%s claimed=%d recount=%d tol=%.0f%% (node discrepancies=%d/%d) - flagged + earnings HELD from promotion pending review",
			nodeID, claimed, recounted, b.recount.tolerance*100, disc, total)
	}
}

// trustState is the per-node L1 + probe trust/quality accumulator surfaced in
// the market view and folded into pick. All counters are broker-measured.
type trustState struct {
	recounts      int // exact+heuristic re-counts observed
	discrepancies int // exact re-counts where the node over-reported past tolerance
	lastClaimed   int
	lastRecount   int
	lastExact     bool

	// probe-fed (see probe.go)
	probes     int
	probeFails int     // consecutive probe failures (streak); reset on success
	probeOK    bool    // last probe passed the canary fingerprint
	probed     bool    // has at least one probe completed
	ttftMs     float64 // EWMA time-to-first-token (ms) from probes
	probeTPS   float64 // EWMA clean tok/s from probes
}

// trustScore is a 0..1 quality signal for a node: starts optimistic, knocked
// down by L1 discrepancies and recent probe failures. Surfaced as `quality` and
// used to deprioritize repeatedly-failing nodes in pick.
func (b *broker) trustScore(nodeID string) float64 {
	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	b.metricsMu.Unlock()
	return tq.score()
}

// verifiedServing reports whether the node has a recent PASSED canary - hard
// evidence it is actually answering correctly (not just heartbeat-alive). Feeds the
// signal's verified-serving term and pick's reliability.
func (t trustState) verifiedServing() bool {
	return t.probed && t.probeOK && t.probeFails == 0
}

func (t trustState) score() float64 {
	s := 1.0
	// L1: each discrepancy as a fraction of re-counts pulls the score down.
	if t.recounts > 0 && t.discrepancies > 0 {
		s -= float64(t.discrepancies) / float64(t.recounts)
	}
	// Probe: a failing canary, or a recent failure streak, pulls it down hard.
	if t.probed && !t.probeOK {
		s -= 0.5
	}
	if t.probeFails > 0 {
		s -= 0.2 * float64(t.probeFails)
	}
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	return s
}

// completionText extracts the assistant completion text from an OpenAI
// chat-completions response body (non-stream) for re-counting. Tolerates the
// string content form (launch is text-only); returns "" if it can't parse.
func completionText(body []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ""
	}
	var out bytes.Buffer
	for _, c := range resp.Choices {
		if c.Message.Content != "" {
			out.WriteString(c.Message.Content)
		} else if c.Text != "" {
			out.WriteString(c.Text)
		}
	}
	return out.String()
}

// qualityOK is the lightweight output-quality validation for the smart-router v2
// reward signal (spec 3): a served response counts as a quality success only when it
// carries real assistant content. A 200-with-empty-body (or a body we can't parse to
// any completion text) does NOT count, so junk can never increment successCount and
// shrink a node's UCB exploration radius. Best-effort + fail-OPEN-ish: an unparseable
// body that still has bytes is treated as content (we do not penalize a node for a
// response shape we don't model), but a structurally-empty completion is rejected.
func qualityOK(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	if txt := completionText(body); txt != "" {
		return qualityOKText(txt)
	}
	// No parseable completion text but a non-trivial body: don't reject (unknown shape).
	return len(bytes.TrimSpace(body)) > 2
}

// qualityOKText reports whether a completion string is non-trivial (has at least one
// non-whitespace character). The empty/whitespace-only completion is the leech the
// reward signal must reject.
func qualityOKText(s string) bool {
	return strings.TrimSpace(s) != ""
}

// producedUsableOutput is the VOID gate predicate (P0): a request produced usable
// output ONLY when the node did not error AND a non-empty completion was returned. It
// is false - so the charge is VOIDED ($0, no earning, hold refunded) - when ANY of:
//   - the node returned an error (status >= 400),
//   - the completion is empty/whitespace, OR
//   - the completion is empty yet the node CLAIMED completion tokens (claim-without-text).
//
// claimedCompletion is the node's self-reported completion_tokens; completion is the
// extracted assistant text (relay: completionText(body); stream: the captured text).
func producedUsableOutput(status int, completion string, claimedCompletion int) bool {
	if status >= 400 {
		return false
	}
	if strings.TrimSpace(completion) == "" {
		return false
	}
	if completion == "" && claimedCompletion > 0 {
		return false
	}
	return true
}

// recountModel is the model id to tokenize under: prefer the receipt's claimed
// model (the canonical tokenizer key), fall back to the request model.
func recountModel(rec protocol.UsageReceipt, reqModel string) string {
	if rec.Model != "" {
		return rec.Model
	}
	return reqModel
}
