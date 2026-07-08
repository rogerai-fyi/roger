package main

import (
	"context"
	"encoding/json"
	"log"
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

// toolCanaryFn is the trivial single-parameter tool the canary offers. A model that honors
// tool-calls returns a tool_calls entry naming it; the LENIENT verdict (FOUNDER FLAG T4)
// accepts ANY well-formed tool_calls entry, so the exact name is not required (wantFn is
// threaded through toolCallOK so a STRICT ruling could require it without a signature change).
const toolCanaryFn = "roger_probe_ack"

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

// toolKey is the (node, model) verdict key for b.toolsOK. The verified bit is per-MODEL, not
// per-node: a node offering two models earns "tools" only for the model(s) that passed.
func toolKey(node, model string) string { return node + "\x00" + model }

// toolCanaryBody is the tiny unbilled /v1/chat/completions request the canary sends: a trivial
// single-parameter tool, tool_choice forcing a call, temperature 0, and a tiny max_tokens (T2).
// A provider that honors tool-calls answers with a tool_calls entry; one that ignores tool
// definitions answers in plain text (or errors), which the verdict reads as unproven.
func toolCanaryBody(model string) []byte {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Call the roger_probe_ack function with ok set to true."},
		},
		"temperature": 0,
		"max_tokens":  toolCanaryMaxTokens,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        toolCanaryFn,
				"description": "Acknowledge the probe by calling this function.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
					"required":   []string{"ok"},
				},
			},
		}},
		"tool_choice": "required",
	})
	return body
}

// toolCallOK is the PURE verdict the tool-call canary applies to a provider's
// /v1/chat/completions response - the twin of evalCanary's fingerprint check. ok == true ONLY
// when the response carries at least one WELL-FORMED tool_calls entry: a non-empty
// function.name AND JSON-parseable function.arguments. A plain-text answer, an empty tool_calls
// array, an unparseable body, or no choices all return false (unproven stays unproven).
//
// LENIENT default (FOUNDER FLAG T4): a valid tool_calls entry to a DIFFERENT function name
// still proves tool-calling - structure (a valid array with a name + parseable arguments)
// proves the provider HONORS the protocol. wantFn is threaded so a STRICT ruling could require
// the name match without changing the signature; today it is used only in the reason string.
func toolCallOK(body []byte, wantFn string) (ok bool, reason string) {
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
			if wantFn != "" && tc.Function.Name != wantFn {
				return true, "well-formed tool_calls to a different function (lenient)"
			}
			return true, "well-formed tool_calls"
		}
	}
	return false, "no well-formed tool_calls entry (plain text / empty array / malformed)"
}

// withVerifiedTools returns the offer's canonical capabilities with the VERIFIED "tools" bit
// unioned in when verified. It is the sole emission gate: declared caps are canonicalized (a
// node-declared "tools" was already stripped at registration, so any "tools" surviving here is
// a peer's authoritative stamp), and the local probe verdict adds "tools" for this instance.
// A nil result (nothing known) keeps the JSON key omitted - absence stays UNDETERMINED, never
// a positive "no tools" (features/trust/toolcall_probe.feature).
func withVerifiedTools(declared []string, verified bool) []string {
	caps := protocol.CanonicalCapabilities(declared)
	if !verified {
		return caps
	}
	return protocol.CanonicalCapabilities(append(caps, protocol.CapTools))
}

// stripDeclaredTools removes a node-declared "tools" from an offer's capabilities: "tools" is
// VERIFIED-not-declared, so a node can NEVER earn it by asserting it (unlike "vision", which
// stays declared). It is applied at the ONE node-facing door (registration), so the only way
// "tools" reaches an offer's stored Capabilities afterwards is a peer's authoritative stamp via
// the shared registry. It returns a fresh slice (copy-on-write) and never mutates the input.
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

	if ok {
		log.Printf("tool-call canary node=%s model=%s VERIFIED (well-formed tool_calls)", nodeID, model)
	} else if authoritative && changed {
		log.Printf("tool-call canary node=%s model=%s REGRESSED (no well-formed tool_calls) - dropping verified tools", nodeID, model)
	}

	// Mirror to the shared verdict store. A PASS re-marks every round (refreshing the freshness
	// TTL) even when the local bit was already set, so a still-honoring model never ages out. An
	// authoritative CLEAR retracts the shared field so peers drop it on their next sync.
	if b.shared == nil {
		return
	}
	switch {
	case ok:
		_ = b.shared.markToolsVerified(nodeID, model, toolsVerifiedTTL)
	case authoritative && changed:
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
	job := protocol.Job{ID: protocol.NewRequestID(), User: "probe", Body: toolCanaryBody(model)}

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
			b.applyToolVerdict(node.NodeID, model, res, authoritative)
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
		b.applyToolVerdict(node.NodeID, model, res, authoritative)
	case <-time.After(30 * time.Second):
		return // transient timeout: no verdict
	}
}

// applyToolVerdict evaluates a tool-call canary JobResult and records it. A non-2xx status is a
// transient upstream hiccup (rate-limit/5xx), NOT proof the model dropped tool support, so it
// is treated as a non-verdict (never clears). A 2xx body is the real verdict: toolCallOK.
func (b *broker) applyToolVerdict(nodeID, model string, res protocol.JobResult, authoritative bool) {
	if res.Status < 200 || res.Status >= 300 {
		b.recordToolProbe(nodeID, model, false, true, authoritative) // transient: no verdict
		return
	}
	ok, _ := toolCallOK(res.Body, toolCanaryFn)
	b.recordToolProbe(nodeID, model, ok, false, authoritative)
}
