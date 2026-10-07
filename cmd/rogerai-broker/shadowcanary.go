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
	"math/rand"
	"os"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// probeIdentity prefixes the identity a canary is pseudonymised from. Founder ruling 2026-10-06:
// the identity ROTATES PER PROBE (a fresh nonce each time), so every canary carries a pseudonym
// derived from the broker's secret exactly like a customer's ("u_" + 16 hex) and no two canaries
// share one: a station sees no stable probe user to recognise. Grading and verified status do not
// read the pseudonym.
const probeIdentity = "\x00probe|"

func (b *broker) probePseudonym(node string) string {
	return b.pseudonym(probeIdentity+protocol.NewRequestID(), node)
}

const organicRing = 50 // shapes kept per model

type organicShape struct {
	tools        bool
	stream       bool
	promptTokens int
	temperature  *float64 // as the request stated it (nil = left out)
	maxTokens    int      // as the request stated it (0 = left out)
}

// noteOrganic records one served-path request's shape for its model.
func (b *broker) noteOrganic(model string, shape organicShape) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.organic == nil {
		b.organic = map[string][]organicShape{}
	}
	ring := append(b.organic[model], shape)
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

// organicSample draws any recent organic shape for model (ok=false with no sample): a canary
// takes its sampling parameters from it.
func (b *broker) organicSample(model string) (organicShape, bool) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	ring := b.organic[model]
	if len(ring) == 0 {
		return organicShape{}, false
	}
	return ring[rand.Intn(len(ring))], true
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

// canaryStream reports whether the next canary for model is sent as a stream. Canaries take
// the organic stream share of the model's recent traffic (§14.B7 #2): the share is carried as
// a running debt, so the canaries track it exactly over time and a model whose traffic mostly
// streams has its very next canary streamed. No organic sample: never a stream.
func (b *broker) canaryStream(model string) bool {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	ring := b.organic[model]
	if len(ring) == 0 {
		return false
	}
	streamed := 0
	for _, s := range ring {
		if s.stream {
			streamed++
		}
	}
	if b.canaryStreamDebt == nil {
		b.canaryStreamDebt = map[string]float64{}
	}
	debt := b.canaryStreamDebt[model] + float64(streamed)/float64(len(ring))
	stream := debt >= 0.5
	if stream {
		debt--
	}
	b.canaryStreamDebt[model] = debt
	return stream
}

// shadowToolSets are realistic function definitions a shadow canary carries (with tool_choice
// "none", so the challenge is still answered in text); one is drawn per canary.
var shadowToolSets = [][]map[string]any{
	{{"type": "function", "function": map[string]any{
		"name": "search_documents", "description": "Search the user's documents for a phrase.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}},
	{{"type": "function", "function": map[string]any{
		"name": "get_weather", "description": "Get the current weather for a city.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"city": map[string]any{"type": "string"}, "unit": map[string]any{"type": "string", "enum": []string{"c", "f"}}},
			"required": []string{"city"}}}}},
	{{"type": "function", "function": map[string]any{
		"name": "create_calendar_event", "description": "Add an event to the user's calendar.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"title": map[string]any{"type": "string"}, "start": map[string]any{"type": "string"}},
			"required": []string{"title", "start"}}}}},
}

func shadowToolSet() []map[string]any { return shadowToolSets[rand.Intn(len(shadowToolSets))] }

// shadowContexts is ordinary prose a shadow canary is padded with to reach a sampled prompt
// length, one drawn per canary; the challenge itself comes last, as a customer's question would.
var shadowContexts = []string{
	"Here are my notes from this week. The team met on Monday to review the release plan, " +
		"agreed to move the migration to the following sprint and asked for a short summary of open risks. " +
		"On Wednesday the support queue was quiet, with most tickets about password resets and invoices. ",
	"I am planning a trip in the spring and comparing two routes. The coastal road is longer but has " +
		"better places to stop, while the inland highway saves about two hours. My budget covers four nights, " +
		"and I would like at least one day without driving. ",
	"Our garden club met on Saturday. We swapped seedlings, talked about the late frost and agreed to " +
		"share the cost of a new compost bin. Two members offered to water the shared beds in August while " +
		"others are away. ",
	"The quarterly numbers came in slightly ahead of plan. Subscriptions grew in the smaller accounts, " +
		"churn held steady, and support costs fell after the new help pages went live. The board wants a " +
		"short note on what changed. ",
}

// shadowPrompt pads prompt to about tokens prompt tokens (4 characters a token).
func shadowPrompt(prompt string, tokens int) string {
	need := tokens*4 - len(prompt)
	if need <= 0 {
		return prompt
	}
	var sb strings.Builder
	pad := shadowContexts[rand.Intn(len(shadowContexts))]
	for sb.Len() < need {
		sb.WriteString(pad)
	}
	return sb.String()[:need] + "\n\n" + prompt
}
