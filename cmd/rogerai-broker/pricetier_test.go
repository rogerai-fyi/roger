package main

import (
	"strings"
	"testing"
)

// (approxEq is shared with price_lock_stream_test.go.)

// TestTierExternalBoundaries pins the EXTERNAL scale (vs a same-model reference): it
// grades discount depth, so the boundaries are inclusive on the LOW side (<=).
// Spec: features/pricing/price_tier.feature "graded against the same model's external reference".
func TestTierExternalBoundaries(t *testing.T) {
	cases := []struct {
		r    float64
		tier int
	}{
		{0.10, 1}, {0.25, 1}, // <=0.25 -> $
		{0.26, 2}, {0.50, 2}, // <=0.50 -> $$
		{0.51, 3}, {0.90, 3}, // <=0.90 -> $$$
		{0.91, 4}, {1.00, 4}, {2.50, 4}, // >0.90 -> $$$$
	}
	for _, c := range cases {
		if got := tierExternal(c.r); got != c.tier {
			t.Errorf("tierExternal(%.2f) = %d, want %d", c.r, got, c.tier)
		}
	}
}

// TestTierInternalBoundaries pins the INTERNAL scale (vs the live per-model median):
// it grades position among peers, boundaries inclusive on the LOW side (<=).
func TestTierInternalBoundaries(t *testing.T) {
	cases := []struct {
		r    float64
		tier int
	}{
		{0.20, 1}, {0.70, 1}, // <=0.70 -> $
		{0.71, 2}, {1.15, 2}, // <=1.15 -> $$
		{1.16, 3}, {2.00, 3}, // <=2.00 -> $$$
		{2.01, 4}, {5.00, 4}, // >2.00 -> $$$$
	}
	for _, c := range cases {
		if got := tierInternal(c.r); got != c.tier {
			t.Errorf("tierInternal(%.2f) = %d, want %d", c.r, got, c.tier)
		}
	}
}

// TestMedianOut covers the median helper: odd (middle), even (mean of two middle),
// empty (ok=false), single, and that it does not assume the input is pre-sorted.
func TestMedianOut(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want float64
		ok   bool
	}{
		{"empty", nil, 0, false},
		{"single", []float64{0.10}, 0.10, true},
		{"odd", []float64{0.30, 0.10, 0.20}, 0.20, true},
		{"even", []float64{0.20, 0.10, 0.10, 0.40}, 0.15, true},
		{"unsorted-outlier", []float64{0.10, 0.10, 0.10, 0.10, 5.00}, 0.10, true},
	}
	for _, c := range cases {
		got, ok := medianOut(c.in)
		if ok != c.ok || (ok && !approxEq(got, c.want)) {
			t.Errorf("medianOut(%s)=%v,%v want %v,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestPriceTierBaselineSelection covers priceTier's baseline choice + edges:
// FREE, external-ref-wins-over-median, external tiers, internal-median fallback,
// thin-market UNKNOWN, no-ref-no-peers UNKNOWN, and per-model isolation via refs.
// Spec: features/pricing/price_tier.feature (the whole contract).
func TestPriceTierBaselineSelection(t *testing.T) {
	peers4 := []float64{0.04, 0.10, 0.10, 0.12} // median 0.10, >=3 online
	cases := []struct {
		name     string
		priceOut float64
		refOut   float64
		peers    []float64
		want     int
	}{
		// FREE always wins, even with a reference.
		{"free-with-ref", 0.0, 0.20, peers4, 0},
		{"free-no-ref", 0.0, 0, nil, 0},

		// External reference present -> EXTERNAL scale, ignores peers.
		{"ext-deal", 0.04, 0.20, nil, 1},      // r=0.20 -> $
		{"ext-at-market", 0.20, 0.20, nil, 4}, // r=1.00 -> $$$$
		{"ext-mid", 0.08, 0.20, nil, 2},       // r=0.40 -> $$

		// External ref takes PRECEDENCE over the internal median.
		// 0.04 vs ref 0.20 -> 0.20 -> $ ; vs median 0.10 it would be 0.4 -> $$.
		{"ext-beats-median", 0.04, 0.20, peers4, 1},

		// No reference -> INTERNAL median fallback (>=3 online peers).
		{"int-deal", 0.04, 0, peers4, 1},      // 0.04/0.10=0.4 -> $
		{"int-at-market", 0.10, 0, peers4, 2}, // 1.0 -> $$

		// No reference + fewer than 3 peers -> UNKNOWN.
		{"thin-market", 0.10, 0, []float64{0.10, 0.50}, 0},
		// No reference + no peers (lone band) -> UNKNOWN.
		{"lone-band", 0.01, 0, []float64{0.01}, 0},
		// No reference + no peers at all -> UNKNOWN.
		{"no-data", 0.05, 0, nil, 0},

		// Cap: way above the reference -> $$$$ (capped, never >4).
		{"cap", 100.0, 0.20, nil, 4},
	}
	for _, c := range cases {
		if got := priceTier(c.priceOut, c.refOut, c.peers); got != c.want {
			t.Errorf("priceTier(%s: price=%.2f ref=%.2f peers=%v) = %d, want %d",
				c.name, c.priceOut, c.refOut, c.peers, got, c.want)
		}
	}
}

// TestPriceTierBoundaryFromDivision exercises the boundary via the REAL priceOut/baseline
// division (not the literal ratio), because float noise bites there: 0.070/0.10 ==
// 0.7000000000000001 > 0.70, which a naive `r <= 0.70` would drop from $ to $$. The spec
// (features/pricing/price_tier.feature) enumerates 0.070 -> tier 1, inclusive on the low side.
func TestPriceTierBoundaryFromDivision(t *testing.T) {
	peers := []float64{0.07, 0.10, 0.13} // median 0.10, 3 online peers, no external ref
	if got := priceTier(0.070, 0, peers); got != 1 {
		t.Errorf("priceTier(0.070, median 0.10) = %d, want 1 ($) at the inclusive boundary", got)
	}
	// external boundaries via division too.
	if got := priceTier(0.05, 0.20, nil); got != 1 { // 0.25 -> $
		t.Errorf("priceTier(0.05, ref 0.20) = %d, want 1", got)
	}
	if got := priceTier(0.10, 0.20, nil); got != 2 { // 0.50 -> $$
		t.Errorf("priceTier(0.10, ref 0.20) = %d, want 2", got)
	}
	if got := priceTier(0.18, 0.20, nil); got != 3 { // 0.90 -> $$$
		t.Errorf("priceTier(0.18, ref 0.20) = %d, want 3", got)
	}
}

// TestPerModelIsolation: the same dollar price lands in different tiers under
// different model references (a cheap 70B is not judged against an 8B).
func TestPerModelIsolation(t *testing.T) {
	if got := priceTier(0.40, 0.20, nil); got != 4 { // small-8b ref 0.20 -> r=2.0 -> $$$$
		t.Errorf("0.40 vs ref 0.20 = %d, want 4", got)
	}
	if got := priceTier(0.40, 2.00, nil); got != 1 { // big-70b ref 2.00 -> r=0.2 -> $
		t.Errorf("0.40 vs ref 2.00 = %d, want 1", got)
	}
}

// TestRenderPriceTier covers the FAVORABLE-ONLY display rule: tier 1 gets the only
// editorialized chip ("good price"); $$-$$$$ are bars only; FREE shows the FREE badge;
// tier 0 priced shows nothing (the raw price renders elsewhere). No negative wording.
func TestRenderPriceTier(t *testing.T) {
	cases := []struct {
		tier     int
		priceOut float64
		wantBars string
		wantChip string
	}{
		{0, 0.0, "FREE", ""}, // free
		{0, 0.05, "", ""},    // unknown-but-priced: no bars, no chip
		{1, 0.05, "$", "good price"},
		{2, 0.10, "$$", ""},
		{3, 0.20, "$$$", ""},
		{4, 0.40, "$$$$", ""},
	}
	for _, c := range cases {
		bars, chip := renderPriceTier(c.tier, c.priceOut)
		if bars != c.wantBars || chip != c.wantChip {
			t.Errorf("renderPriceTier(%d, %.2f) = %q,%q want %q,%q",
				c.tier, c.priceOut, bars, chip, c.wantBars, c.wantChip)
		}
		// Favorable-only: a chip, when present, is never negative.
		for _, bad := range []string{"expensive", "overpriced", "too ", "rip-off", "bad"} {
			if chip != "" && strings.Contains(strings.ToLower(chip), bad) {
				t.Errorf("renderPriceTier(%d) chip %q contains negative wording %q", c.tier, chip, bad)
			}
		}
	}
}

// TestNormalizeModelName: OpenRouter "vendor/model" ids and operator free-text collapse
// to the same key (lowercased, vendor prefix dropped, trimmed) so the ref lookup matches.
func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"qwen/qwen3-8b":                     "qwen3-8b",
		"meta-llama/llama-3.3-70b-instruct": "llama-3.3-70b-instruct",
		"Qwen3-8B":                          "qwen3-8b",
		"  GPT-OSS-120B  ":                  "gpt-oss-120b",
		"deepseek/deepseek-r1:free":         "deepseek-r1", // ":free"/":nitro" variant suffix dropped
	}
	for in, want := range cases {
		if got := normalizeModelName(in); got != want {
			t.Errorf("normalizeModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseOpenRouterModels parses the public /models payload into a model->out($/1M)
// map: completion is $/TOKEN, so x1e6; entries with a missing/zero/garbage completion
// price are skipped (never a 0 reference that would mislabel a band).
func TestParseOpenRouterModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"qwen/qwen3-8b","pricing":{"prompt":"0.0000001","completion":"0.0000002"}},
		{"id":"meta-llama/llama-3.3-70b-instruct","pricing":{"prompt":"0.0000004","completion":"0.0000004"}},
		{"id":"free/zero","pricing":{"completion":"0"}},
		{"id":"bad/garbage","pricing":{"completion":"abc"}}
	]}`)
	m, err := parseOpenRouterModels(body)
	if err != nil {
		t.Fatalf("parseOpenRouterModels: %v", err)
	}
	if !approxEq(m["qwen3-8b"], 0.20) {
		t.Errorf("qwen3-8b out = %v, want 0.20", m["qwen3-8b"])
	}
	if !approxEq(m["llama-3.3-70b-instruct"], 0.40) {
		t.Errorf("llama-70b out = %v, want 0.40", m["llama-3.3-70b-instruct"])
	}
	if _, ok := m["zero"]; ok {
		t.Error("a zero completion price should be skipped, not stored")
	}
	if _, ok := m["garbage"]; ok {
		t.Error("an unparseable completion price should be skipped, not stored")
	}
}
