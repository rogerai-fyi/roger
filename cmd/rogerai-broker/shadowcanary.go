package main

// shadowcanary.go: probes that look like customers (contract §14.B7 #2,
// features/security/routing_integrity.feature). A canary carries a pseudonym from the same
// derivation real users get, and a sampled share of canaries mirror the SHAPE of recent organic
// traffic for the model (tools present or not, prompt length), so a station cannot serve
// probes from the real model and customers from something cheaper by recognising either.
//
// The organic sample is a per-instance ring of recent request SHAPES (never content): a
// sampling hint for the probe, not a source of truth for anything a request depends on.

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

// probeIdentity is the identity canaries are pseudonymised from: b.pseudonym hashes it with
// the broker's secret, so the station sees "u_" + 16 hex like any consumer and cannot tell.
const probeIdentity = "\x00probe"

func (b *broker) probePseudonym(node string) string {
	if len(b.priv) == 0 { // a broker built without a key (some unit fixtures): same shape, unkeyed
		h := sha256.Sum256([]byte(probeIdentity + "|" + node))
		return "u_" + hex.EncodeToString(h[:8])
	}
	return b.pseudonym(probeIdentity, node)
}

const organicRing = 50 // shapes kept per model

type organicShape struct {
	tools        bool
	promptTokens int
}

// noteOrganic records one served-path request's shape for its model.
func (b *broker) noteOrganic(model string, tools bool, promptTokens int) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.organic == nil {
		b.organic = map[string][]organicShape{}
	}
	ring := append(b.organic[model], organicShape{tools: tools, promptTokens: promptTokens})
	if len(ring) > organicRing {
		ring = ring[len(ring)-organicRing:]
	}
	b.organic[model] = ring
}

// shadowShare is ROGERAI_SHADOW_CANARY_SHARE: the share of canaries that mirror a sampled
// organic shape (default 0.5).
func shadowShare() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("ROGERAI_SHADOW_CANARY_SHARE"), 64); err == nil && v >= 0 && v <= 1 {
		return v
	}
	return 0.5
}

// shadowShape draws a recent organic shape for model, or ok=false for a plain canary.
func (b *broker) shadowShape(model string) (organicShape, bool) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	ring := b.organic[model]
	if len(ring) == 0 || rand.Float64() >= shadowShare() {
		return organicShape{}, false
	}
	return ring[rand.Intn(len(ring))], true
}

// shadowTools is a realistic function definition a shadow canary carries (with tool_choice
// "none", so the challenge is still answered in text).
var shadowTools = []map[string]any{{"type": "function", "function": map[string]any{
	"name": "search_documents", "description": "Search the user's documents for a phrase.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"query": map[string]any{"type": "string"}}, "required": []string{"query"}},
}}}

// shadowContext is ordinary prose a shadow canary is padded with to reach a sampled prompt
// length; the challenge itself comes last, as a customer's question would.
const shadowContext = "Here are my notes from this week. The team met on Monday to review the release plan, " +
	"agreed to move the migration to the following sprint and asked for a short summary of open risks. " +
	"On Wednesday the support queue was quiet, with most tickets about password resets and invoices. "

// shadowPrompt pads prompt to about tokens prompt tokens (4 characters a token).
func shadowPrompt(prompt string, tokens int) string {
	need := tokens*4 - len(prompt)
	if need <= 0 {
		return prompt
	}
	var sb strings.Builder
	for sb.Len() < need {
		sb.WriteString(shadowContext)
	}
	return sb.String()[:need] + "\n\n" + prompt
}
