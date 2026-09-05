// Package ctxsig recognizes CONTEXT-OVERFLOW error text across every server spelling.
//
// It is a deliberate LEAF (stdlib only): the broker needs this one judgement in its
// strike guard, and importing the full harness for a string matcher dragged client,
// capsule, brief, os/exec and x/text into the broker binary (audit, 2026-09-05).
// harness.IsContextOverflow / IsRequestTooLarge delegate here, so there is still
// exactly ONE spelling list - two copies would drift and the harness would compact
// on a shape the broker still struck, or the reverse.
package ctxsig

import "strings"

// IsOverflow reports whether raw is a server's context-overflow complaint, in any
// spelling seen in the wild. Apple's on-device foundation model says "Exceeded model
// context window size"; llama.cpp / vLLM / OpenAI-compatible servers phrase it as
// "context length exceeded", "maximum context length", "too many tokens", a full
// "kv cache", or llama-server's "exceeds the available context size".
func IsOverflow(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "context window") ||
		strings.Contains(low, "context length") ||
		strings.Contains(low, "context size") ||
		strings.Contains(low, "context_length_exceeded") ||
		strings.Contains(low, "maximum context") ||
		strings.Contains(low, "too many tokens") ||
		strings.Contains(low, "kv cache") ||
		IsRequestTooLarge(low)
}

// IsRequestTooLarge spots the same wall measured in BYTES: an HTTP-layer refusal
// ("request body size ... exceeded", "payload too large", "entity too large") whose
// cause and remedy are identical to a token overflow. Deliberately narrow - never a
// bare "413", which could appear inside a model's own answer.
func IsRequestTooLarge(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "request body size") ||
		strings.Contains(low, "payload too large") ||
		strings.Contains(low, "entity too large") ||
		strings.Contains(low, "body size exceeded")
}
