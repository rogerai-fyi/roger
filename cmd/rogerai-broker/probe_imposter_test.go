package main

// probe_imposter_test.go - the canary must not verify a station whose upstream
// answers as a DIFFERENT model (features/curated/curated_probes.feature, "A canary
// refuses to verify an imposter"). Live catch 2026-09-04: a band advertised
// Qwen3.8-27B, the upstream served wave-pico-293m to any model id, and the band
// wore the check mark - /clear could never fix a persona that was the server.

import (
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestImposterModelDetection(t *testing.T) {
	cases := []struct {
		band, resp string
		imposter   bool
	}{
		// the live catch itself
		{"Qwen3.8-27B", "wave-pico-293m", true},
		// naming variants are honest and common - never false-fail these
		{"qwen-3.8-27b", "Qwen3.8-27B", false},
		{"gpt-oss-120b", "openai/gpt-oss-120b", false},
		{"deepseek/deepseek-v3.2", "deepseek-v3.2", false},
		{"llama-3.3-70b-instruct", "llama-3.3-70b-instruct:latest", false},
		{"qwen3.8-27b", "qwen3.8-27b-q4_k_m", false},
		// suffix-only variants share the stem: same family, never imposters
		{"qwen3.8-27b-coder", "qwen3.8-27b-instruct", false},
		{"llama-3.3-70b-instruct", "llama-3.3-70b-versatile", false},
		// DIFFERENT WEIGHTS are different models, whatever the shared stem
		{"qwen3.8-27b", "qwen3.8-4b", true},
		{"gpt-oss-120b", "gpt-oss-20b", true},
		{"wave-pico-293m", "wave-giga-9b", true},
		// known accepted limit: pure-word family variants are indistinguishable from
		// honest fine-tunes by name alone; the mark is withheld-not-struck anyway
		{"claude-opus-4.8", "claude-haiku-4.5", false},
		// an empty response model says nothing - many servers omit it
		{"qwen3.8-27b", "", false},
		// clearly unrelated pairs fail
		{"gpt-oss-120b", "gemma-4-31b", true},
		{"claude-haiku-4.5", "mistral-large-2512", true},
	}
	for _, c := range cases {
		if got := imposterModel(c.band, c.resp); got != c.imposter {
			t.Errorf("imposterModel(%q, %q) = %v, want %v", c.band, c.resp, got, c.imposter)
		}
	}
}

func TestEvalCanaryFailsAnImposter(t *testing.T) {
	b, _, _, _ := newBandBroker(t)
	fp := canaryFingerprints[0]
	body := []byte(`{"model":"wave-pico-293m","choices":[{"message":{"content":"` + fp.expect + `"}}],` +
		`"usage":{"completion_tokens":3}}`)
	res := protocol.JobResult{Status: 200, Body: body, Receipt: protocol.UsageReceipt{CompletionTokens: 3}}
	outcome, _, _, _ := b.evalCanary(res, 50*time.Millisecond, fp, "Qwen3.8-27B")
	if outcome != probeMismatch {
		t.Fatalf("an imposter response earned outcome %v, want probeMismatch", outcome)
	}
	if outcome.failed() {
		t.Fatal("a mismatch must not be a strike - honest alias bands would quarantine")
	}
	// the same body under its own band verifies
	outcome, _, _, _ = b.evalCanary(res, 50*time.Millisecond, fp, "wave-pico-293m")
	if outcome != probePass {
		t.Fatalf("the honest band got %v, want probePass", outcome)
	}
}

// The concierge pick reads LIVENESS, not the identity mark: an honest alias band
// with a model mismatch and no traffic yet must stay pickable (audit catch).
func TestMismatchKeepsProvenLive(t *testing.T) {
	b, _, _, _ := newBandBroker(t)
	b.trust = map[string]trustState{}
	b.probe = loadProbe()
	b.recordProbe("alias-node", probeMismatch, 100, 10, false, true)
	tq := b.trust["alias-node"]
	if !tq.provenLive() {
		t.Fatal("a mismatch node with a completed canary must stay proven-live")
	}
	if tq.verifiedServing() {
		t.Fatal("but it must not wear the verification mark")
	}
	// a later clean pass restores the mark
	b.recordProbe("alias-node", probePass, 100, 10, true, true)
	if !b.trust["alias-node"].verifiedServing() {
		t.Fatal("a clean pass must clear the withheld mark")
	}
}

// An imposter that ALSO answers a wrong-family token keeps its strike - two
// independent wrongness signals are a bad node, not an honest alias.
func TestImposterPlusWrongFamilyStillStrikes(t *testing.T) {
	b, _, _, _ := newBandBroker(t)
	var other string
	for _, f := range canaryFingerprints {
		if f.expect != canaryFingerprints[0].expect && len(f.expect) >= 4 {
			other = f.expect
			break
		}
	}
	if other == "" {
		t.Skip("no distinctive second canary token")
	}
	body := []byte(`{"model":"wave-pico-293m","choices":[{"message":{"content":"` + other + `"}}],"usage":{"completion_tokens":3}}`)
	res := protocol.JobResult{Status: 200, Body: body, Receipt: protocol.UsageReceipt{CompletionTokens: 3}}
	outcome, _, _, _ := b.evalCanary(res, 50*time.Millisecond, canaryFingerprints[0], "Qwen3.8-27B")
	if outcome != probeWrong {
		t.Fatalf("imposter+wrong-family got %v, want probeWrong (the strike it always earned)", outcome)
	}
}

// The paid spine withholds the verified BOOST on a mismatch - ranked as
// never-positively-proven (0.7), never as failing.
func TestSpineWithholdsVerifiedBoostOnMismatch(t *testing.T) {
	clean := reliabilityFactor(true, true, 0, false, 1, true, 1)
	mis := reliabilityFactor(true, true, 0, true, 1, true, 1)
	unproven := reliabilityFactor(false, false, 0, false, 1, true, 1)
	if !(mis < clean) {
		t.Fatalf("mismatch kept the verified boost: clean=%v mismatch=%v", clean, mis)
	}
	if mis != unproven {
		t.Fatalf("mismatch should rank as never-proven: mismatch=%v unproven=%v", mis, unproven)
	}
	failing := reliabilityFactor(true, false, 2, false, 1, true, 1)
	if !(mis > failing) {
		t.Fatalf("mismatch must never rank as failing: mismatch=%v failing=%v", mis, failing)
	}
}
