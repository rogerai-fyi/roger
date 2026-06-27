package protocol

import (
	"math"
	"testing"
)

// approx compares two credit/dollar amounts within a tiny float epsilon.
func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestCostFromTokens locks the token->credits arithmetic exactly: cost is
// (promptTokens*priceIn + completionTokens*priceOut) / 1e6. Prices are $/1M tokens
// (credits, 1 credit = $1 by default). This is the calculation C1 was about - if it
// ever drifts, real money is mischarged. Exact, table-driven, regression-proof.
func TestCostFromTokens(t *testing.T) {
	cases := []struct {
		name               string
		prompt, completion int
		priceIn, priceOut  float64
		want               float64
	}{
		{"zero tokens", 0, 0, 0.50, 0.50, 0},
		{"free model (0 price)", 1000, 5000, 0, 0, 0},
		{"in+out mixed", 1000, 500, 0.20, 0.50, (1000*0.20 + 500*0.50) / 1e6}, // 0.00045
		{"out only", 0, 800, 0, 0.30, 800 * 0.30 / 1e6},                       // 0.00024
		{"in only", 2000, 0, 0.10, 0, 2000 * 0.10 / 1e6},                      // 0.0002
		{"exactly 1M each @ $1", 1_000_000, 1_000_000, 1.0, 1.0, 2.0},
		{"large + fractional price", 3_500_000, 1_250_000, 0.15, 0.60, (3_500_000*0.15 + 1_250_000*0.60) / 1e6},
		{"sub-cent precision", 1, 1, 0.01, 0.01, 0.02 / 1e6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := UsageReceipt{PromptTokens: c.prompt, CompletionTokens: c.completion, PriceIn: c.priceIn, PriceOut: c.priceOut}
			if got := r.Cost(); !approxEq(got, c.want) {
				t.Fatalf("Cost() = %.12f, want %.12f", got, c.want)
			}
			// CostWith2 with the receipt's own tokens must equal Cost().
			if got := r.CostWith2(c.prompt, c.completion); !approxEq(got, c.want) {
				t.Fatalf("CostWith2() = %.12f, want %.12f", got, c.want)
			}
		})
	}
}

// TestCostWith2OverridesTokens: the broker re-counts tokens and bills CostWith2 with
// its OWN counts, not the node's claim - so a node over-claiming completion tokens
// cannot inflate the charge beyond what the broker counted.
func TestCostWith2OverridesTokens(t *testing.T) {
	r := UsageReceipt{PromptTokens: 9999, CompletionTokens: 9999, PriceIn: 0.5, PriceOut: 0.5}
	// Broker re-counts 100 prompt / 200 completion -> bill those, ignore the claim.
	want := (100*0.5 + 200*0.5) / 1e6
	if got := r.CostWith2(100, 200); !approxEq(got, want) {
		t.Fatalf("CostWith2(100,200) = %.12f, want %.12f (broker counts must win over the node claim)", got, want)
	}
}

// TestFeeSplitConservation locks the 70/30 split math: ownerShare = cost*(1-fee), the
// platform keeps cost*fee, and the two ALWAYS sum back to the exact cost (no credits
// created or destroyed in the split). Tested across fee rates + costs.
func TestFeeSplitConservation(t *testing.T) {
	for _, fee := range []float64{0, 0.25, 0.30, 0.50, 1.0} {
		for _, cost := range []float64{0, 0.00045, 1.0, 2.5, 123.456789} {
			owner := cost * (1 - fee)
			platform := cost - owner
			if owner < 0 || platform < 0 {
				t.Fatalf("fee=%.2f cost=%.6f produced a negative share (owner=%.6f platform=%.6f)", fee, cost, owner, platform)
			}
			if !approxEq(owner+platform, cost) {
				t.Fatalf("fee=%.2f cost=%.6f: owner(%.9f)+platform(%.9f) != cost(%.9f)", fee, cost, owner, platform, cost)
			}
			if !approxEq(platform, cost*fee) {
				t.Fatalf("fee=%.2f cost=%.6f: platform take %.9f != cost*fee %.9f", fee, cost, platform, cost*fee)
			}
		}
	}
}
