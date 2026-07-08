package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// toolcall.go is the TOOL-CALL CAPABILITY PROBE: the broker's own canary that turns the
// INFERRED "agent-ready" reading into a VERIFIED one. It is the fourth trust pillar next to
// verified-serving (the liveness canary), confidential (◆), and lineage receipts.
//
// The rule (features/trust/toolcall_probe.feature): "tools" is a VERIFIED capability, NOT a
// declared one. A model earns "tools" ONLY when this canary confirms the provider HONORS an
// OpenAI tool-call request (a well-formed tool_calls response to a "call this function"
// prompt). A node CANNOT earn it by declaring it - unlike "vision" (declared-not-probed).
//
// Wiring (reuse, don't rebuild - the minimization rung):
//   - the tool canary rides the EXISTING probe schedule/backoff/jitter/per-owner cap: it is a
//     SECOND assertion folded into the SAME probeOnce round as the liveness canary (T1), never
//     a new faster loop.
//   - the verdict is a PURE function over the response body (toolCallOK), the twin of
//     evalCanary's fingerprint check - table-tested with no live node.
//   - the earned bit is stamped on b.toolsOK (like probeOK/verifiedServing) and materialized
//     into the offer's Capabilities as "tools" on the /discover + /market read.
//   - multi-instance: the bit mirrors to the shared registry and is read as a UNION, exactly
//     like the registry/liveness pattern, so two instances neither double-probe nor split it.

// toolCanaryFn is the BASE name of the trivial single-parameter tool the canary offers. Each
// probe suffixes it with a fresh random nonce (toolCanaryFn+"_"+nonce), so the tool a model is
// asked to call is never the same twice - closing the fingerprint hole (PR #33 review, minor #4)
// a canned well-formed tool_calls could otherwise walk through to earn the badge unearned.
const toolCanaryFn = "roger_probe_ack"

// newToolNonce mints a fresh per-probe nonce: 8 bytes of crypto/rand hex (randomness in the
// BROKER is fine, unlike deterministic workflow scripts). The nonce is woven into the canary's
// tool name AND the token argument the prompt asks the model to echo, and toolCallOK requires the
// response to reference it - so a hostile node cannot pre-can a reply for a value it can't guess.
// crypto/rand.Read never partially fills without erroring; on the astronomically-unlikely error
// we fall back to a time-seeded token so the nonce is never empty (empty would re-open leniency).
func newToolNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// toolCanaryMaxTokens is the canary's completion budget: TINY (FOUNDER FLAG T2). A tool call
// is a few tokens of arguments; we never need the reasoning headroom the liveness canary
// leaves. The job is unbilled (User="probe") and the result is discarded after the verdict.
const toolCanaryMaxTokens = 64

// toolsVerifiedTTL is the freshness window for a shared verified-tools field: a verified model
// must be re-proven (a passing canary re-marks it) within this window or it ages out of the
// union as UNDETERMINED. Set comfortably above the probe ceiling (15m) so a model probed on the
// idle backoff stays fresh; it is the backstop for an authoritative host that dies WITHOUT a
// regression (a real regression clears the field immediately via clearToolsVerified).
const toolsVerifiedTTL = 45 * time.Minute

// toolsRefreshEvery throttles the served-traffic refresh of a verified model's shared field (see
// markMeasured): a continuously-busy node that probeOnce keeps skipping still keeps its verified
// bit fresh from real traffic, but the hot settle path re-marks Valkey at most this often (well
// under toolsVerifiedTTL, so the field never lapses between refreshes).
const toolsRefreshEvery = 15 * time.Minute

// toolKey is the (node, model) verdict key for b.toolsOK. The verified bit is per-MODEL, not
// per-node: a node offering two models earns "tools" only for the model(s) that passed.
func toolKey(node, model string) string { return node + "\x00" + model }

// toolCanaryBody is the tiny unbilled /v1/chat/completions request the canary sends: a trivial
// single-parameter tool, tool_choice forcing a call, temperature 0, and a tiny max_tokens (T2).
// A provider that honors tool-calls answers with a tool_calls entry; one that ignores tool
// definitions answers in plain text (or errors), which the verdict reads as unproven.
//
// The nonce is woven in TWO ways so a genuine model has an unpredictable token to echo (and a
// canned reply has nothing to echo): the forced tool's NAME is toolCanaryFn+"_"+nonce, and its
// single "token" parameter is what the prompt tells the model to set to the nonce. toolCallOK
// accepts the nonce appearing in EITHER channel (name suffix or arguments), so a model that
// honors tool-calls passes however it surfaces the call, while a fingerprinted reply built for a
// different (or no) nonce fails. Single-parameter still (T2): the one param is "token".
func toolCanaryBody(model, nonce string) []byte {
	fn := toolCanaryFn + "_" + nonce
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Call the " + fn + " function, setting token to \"" + nonce + "\"."},
		},
		"temperature": 0,
		"max_tokens":  toolCanaryMaxTokens,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        fn,
				"description": "Acknowledge the probe by calling this function with token set to the value in the instruction.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"token": map[string]any{"type": "string"}},
					"required":   []string{"token"},
				},
			},
		}},
		"tool_choice": "required",
	})
	return body
}

// toolCallOK is the PURE verdict the tool-call canary applies to a provider's
// /v1/chat/completions response - the twin of evalCanary's fingerprint check. ok == true ONLY
// when the response carries at least one WELL-FORMED tool_calls entry - a non-empty
// function.name AND JSON-parseable function.arguments - that ALSO references THIS probe's nonce
// (in the function name suffix or the arguments). A plain-text answer, an empty tool_calls
// array, an unparseable body, or no choices all return false (unproven stays unproven).
//
// The nonce is the anti-fingerprint gate (PR #33 review, minor #4): the canary randomizes both
// the tool name and a token the model must echo, so a CANNED/replayed well-formed tool_calls -
// built for a prior probe or a fixed fingerprint - cannot reference the current nonce and fails.
// It stays LENIENT about STRUCTURE and about WHERE the nonce appears (name or args) so a genuine
// model passes however it surfaces the forced call (FOUNDER FLAG T4: a different function name is
// still fine PROVIDED it echoes the nonce token). An empty nonce is a test affordance only (the
// live probe always mints one via newToolNonce): with no nonce it degrades to the structural
// check, which the broker never does in production.
func toolCallOK(body []byte, nonce string) (ok bool, reason string) {
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, "unparseable response body"
	}
	if len(resp.Choices) == 0 {
		return false, "no choices in response"
	}
	for _, ch := range resp.Choices {
		for _, tc := range ch.Message.ToolCalls {
			if tc.Function.Name == "" {
				continue // a tool_calls entry with no function name is not well-formed
			}
			// arguments is a STRING carrying JSON; it must be valid JSON (an empty object "{}"
			// counts). A model that emits `{not json` did not honor the protocol.
			if !json.Valid([]byte(tc.Function.Arguments)) {
				continue
			}
			// The nonce must appear in THIS call - the name suffix or the echoed arguments - so a
			// canned/fingerprinted reply that cannot know the fresh nonce is rejected. Lenient
			// about which channel carries it; strict that it is present.
			if nonce != "" && !strings.Contains(tc.Function.Name, nonce) && !strings.Contains(tc.Function.Arguments, nonce) {
				continue // well-formed but does not reference this probe's nonce (canned/replayed)
			}
			return true, "well-formed tool_calls referencing the probe nonce"
		}
	}
	return false, "no well-formed tool_calls referencing the nonce (plain text / empty array / malformed / canned)"
}

// withVerifiedTools is the SOLE emission gate for the "tools" capability. It STRIPS any "tools"
// sitting in the offer's declared/stored capabilities and re-adds it ONLY from the probe verdict
// (verified). This is verified-not-declared enforced at the READ, not just at the register door:
// a "tools" can reach a stored/mirrored/re-hydrated offer WITHOUT passing register's strip - the
// shared-registry mirror, the lazy tunnel learn, and the DB re-hydrate all ingest raw regs, and
// a mixed-version rolling deploy can mirror a pre-strip declared "tools". Stripping at emission
// means such a bit is NEVER trusted (and a failing canary can never leave a stale declared bit
// stranded). A nil result keeps the JSON key omitted - absence stays UNDETERMINED, never a
// positive "no tools" (features/trust/toolcall_probe.feature).
func withVerifiedTools(declared []string, verified bool) []string {
	caps := protocol.CanonicalCapabilities(stripDeclaredTools(declared)) // never trust a stored/mirrored declared "tools"
	if !verified {
		return caps
	}
	return protocol.CanonicalCapabilities(append(caps, protocol.CapTools))
}

// stripDeclaredTools removes a "tools" value from a capability list: "tools" is VERIFIED-not-
// declared, so a node can NEVER earn it by asserting it (unlike "vision", which stays declared).
// It is applied at BOTH the node-facing register door AND at emission (withVerifiedTools), so no
// ingestion path (register, shared-registry mirror, lazy learn, DB re-hydrate) can leak a
// declared "tools" to the public feed. It returns a fresh slice (copy-on-write), never mutating.
func stripDeclaredTools(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		if strings.ToLower(strings.TrimSpace(c)) == protocol.CapTools {
			continue
		}
		out = append(out, c)
	}
	return out
}

// recordToolProbe folds ONE tool-call canary verdict into b.toolsOK and mirrors it. ok is the
// toolCallOK result; transient marks a dispatch error / 429 / timeout (a NON-verdict that must
// NOT clear an earned bit - the twin of the liveness probe's "a dispatch that never reached the
// node is not evidence"). authoritative is whether THIS instance hosts the node's live poll
// (single-instance is always authoritative): only the authoritative host CLEARS the bit on a
// definitive regression, so a non-authoritative peer's failed cross-instance probe never yanks
// a verdict the host proved.
//
//   - transient        -> no change (retry next round).
//   - ok               -> set verified (monotonic; a peer that also proves it is harmless).
//   - definitive fail  -> clear IFF authoritative (a real regression); else leave it.
//
// The verdict is FIRST-CLASS SHARED STATE, not a per-instance map: on a change it writes the
// shared toolsok field (markToolsVerified on a pass, clearToolsVerified on an authoritative
// regression) AND updates this instance's merged read map immediately, so a host's regression
// clear propagates to every peer on the next sync (a peer can never re-poison a cleared verdict
// - the bug a per-instance monotonic map had). b.toolsOK stays this instance's OWN verdict,
// which is the emission source ONLY in single-instance mode.
func (b *broker) recordToolProbe(nodeID, model string, ok, transient, authoritative bool) {
	if transient {
		return // a non-verdict is not evidence: never clears, never sets
	}
	key := toolKey(nodeID, model)
	changed := false
	b.metricsMu.Lock()
	if b.toolsOK == nil {
		b.toolsOK = map[string]bool{}
	}
	if b.toolsMerged == nil {
		b.toolsMerged = map[string]bool{}
	}
	switch {
	case ok:
		if !b.toolsOK[key] {
			b.toolsOK[key] = true
			changed = true
		}
		b.toolsMerged[key] = true // reflect our own fresh verdict at once (the sync reconciles peers)
	case authoritative:
		if b.toolsOK[key] {
			delete(b.toolsOK, key)
			changed = true
		}
		delete(b.toolsMerged, key) // an authoritative clear drops it locally too, pending the shared del
	}
	b.metricsMu.Unlock()

	// Log only on a TRANSITION (changed), not every probe round - a verified model re-proves on
	// every cadence tick, and an unconditional VERIFIED line would spam the log at probe rate.
	if ok && changed {
		log.Printf("tool-call canary node=%s model=%s VERIFIED (well-formed tool_calls)", nodeID, model)
	} else if !ok && authoritative && changed {
		log.Printf("tool-call canary node=%s model=%s REGRESSED (no well-formed tool_calls) - dropping verified tools", nodeID, model)
	}

	// Mirror to the shared verdict store. A PASS re-marks every round (refreshing the freshness
	// TTL) even when the local bit was already set, so a still-honoring model never ages out. An
	// authoritative definitive fail CLEARS the shared field UNCONDITIONALLY (idempotent HDEL) -
	// NOT gated on the local `changed`: after a restart b.toolsOK is empty while the shared field
	// may still be set (or a peer proved it), so gating on `changed` would leave a regressed model
	// falsely VERIFIED for up to toolsVerifiedTTL. The clear is cheap and safe to repeat.
	if b.shared == nil {
		return
	}
	switch {
	case ok:
		_ = b.shared.markToolsVerified(nodeID, model, toolsVerifiedTTL)
	case authoritative:
		_ = b.shared.clearToolsVerified(nodeID, model)
	}
}

// syncToolsVerified refreshes the in-memory merged verdict map from the shared store (the UNION
// across instances, fresh fields only). It runs on the same sync loop as the liveness/registry
// merge, keeping the hot /discover + /market read purely in-memory. A shared error leaves the
// last merged view in place (degrade, don't flap). No-op single-instance (own toolsOK is truth).
func (b *broker) syncToolsVerified() {
	if b.shared == nil {
		return
	}
	merged, err := b.shared.toolsVerified(toolsVerifiedTTL)
	if err != nil {
		return
	}
	b.metricsMu.Lock()
	b.toolsMerged = merged
	b.metricsMu.Unlock()
}

// toolsVerifiedForLocked reports whether a (node, model) carries a VERIFIED tool-call bit for
// EMISSION. Single-instance reads this instance's own probe verdict (b.toolsOK); multi-instance
// reads the shared UNION (b.toolsMerged), so a host's regression clear is honoured everywhere
// and a peer never surfaces a verdict the host retracted. Caller holds metricsMu.
func (b *broker) toolsVerifiedForLocked(nodeID, model string) bool {
	if b.shared != nil {
		return b.toolsMerged[toolKey(nodeID, model)]
	}
	return b.toolsOK[toolKey(nodeID, model)]
}

// authoritativeFor reports whether THIS instance hosts the node's live poll and may therefore
// CLEAR a verified bit on a definitive regression. Single-instance (no shared store) is always
// authoritative. It mirrors the /discover probe-dead veto gate (enrichOffersForNode): a
// multi-instance PEER that merely mirrors the node must not yank a verdict the host proved.
func (b *broker) authoritativeFor(nodeID string, now time.Time) bool {
	if b.shared == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	at := b.localPollAt[nodeID]
	return !at.IsZero() && now.Sub(at) < nodeTTL
}

// probeToolCall dispatches the tool-call canary to one node's chat model in the SAME probe
// round as the liveness canary and records the verdict. It reuses probeNode's dispatch shape
// (single-instance local tunnel; multi-instance bus) but bills nothing (User="probe") and
// discards the body after the verdict. A dispatch error / no-poller / timeout is TRANSIENT (a
// non-verdict): it never clears an earned bit. authoritative (this instance hosts the poll) is
// resolved by the caller and threaded so only the host clears on a definitive regression.
func (b *broker) probeToolCall(node protocol.NodeRegistration, model string, authoritative bool) {
	b.mu.Lock()
	t := b.tunnels[node.NodeID]
	mi := b.multiInstance && b.shared != nil
	b.mu.Unlock()
	if t == nil && !mi {
		return
	}
	nonce := newToolNonce() // fresh per probe: the model must echo it, defeating a canned reply
	job := protocol.Job{ID: protocol.NewRequestID(), User: "probe", Body: toolCanaryBody(model, nonce)}

	if mi {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch, dcancel, derr := b.busDispatchJob(ctx, node.NodeID, job)
		if dcancel != nil {
			defer dcancel()
		}
		if derr != nil {
			return // transient dispatch failure: no verdict
		}
		select {
		case raw, okc := <-ch:
			if !okc {
				return
			}
			var res protocol.JobResult
			if json.Unmarshal(raw, &res) != nil {
				return
			}
			b.applyToolVerdict(node.NodeID, model, res, authoritative, nonce)
		case <-time.After(30 * time.Second):
			return // transient timeout: no verdict
		}
		return
	}

	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		return // could not enqueue: transient, no verdict
	}
	select {
	case res := <-resCh:
		b.applyToolVerdict(node.NodeID, model, res, authoritative, nonce)
	case <-time.After(30 * time.Second):
		return // transient timeout: no verdict
	}
}

// applyToolVerdict evaluates a tool-call canary JobResult and records it. A non-2xx status is a
// transient upstream hiccup (rate-limit/5xx), NOT proof the model dropped tool support, so it
// is treated as a non-verdict (never clears). A 2xx body is the real verdict: toolCallOK, which
// requires the response to reference THIS probe's nonce (threaded from probeToolCall) so a canned
// well-formed tool_calls cannot earn the badge.
func (b *broker) applyToolVerdict(nodeID, model string, res protocol.JobResult, authoritative bool, nonce string) {
	if res.Status < 200 || res.Status >= 300 {
		b.recordToolProbe(nodeID, model, false, true, authoritative) // transient: no verdict
		return
	}
	ok, _ := toolCallOK(res.Body, nonce)
	b.recordToolProbe(nodeID, model, ok, false, authoritative)
}
