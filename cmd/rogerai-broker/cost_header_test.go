package main

import "testing"

// TestFmtCostHeader: the X-RogerAI-Cost display header sends the EXACT cost, never the
// round6 truncation that collapsed a real sub-microcredit charge to a bare "0" (which the
// consumer then read as "$0.00", as if a paid turn were free). Free turns send "0"; tiny
// paid turns send their exact value; float noise is cleaned to 6 significant figures.
// (features/money/cost_precision.feature.)
func TestFmtCostHeader(t *testing.T) {
	cases := []struct {
		cost float64
		want string
	}{
		{0, "0"},
		{-1, "0"},
		{0.00000034, "0.00000034"}, // 34 tok @ $0.01/1M - round6 WOULD have zeroed this
		{0.00000036, "0.00000036"}, // the screenshot case (36 out tok)
		{0.00000285, "0.00000285"}, // above the 1e-6 grid - still exact, not snapped to 0.000003
		{0.045, "0.045"},
		{0.3, "0.3"},
		{0.1 + 0.2, "0.3"}, // float noise (0.30000000000000004) cleaned to 6 sig figs
	}
	for _, c := range cases {
		if got := fmtCostHeader(c.cost); got != c.want {
			t.Errorf("fmtCostHeader(%v) = %q, want %q", c.cost, got, c.want)
		}
	}

	// Regression guard: round6 (still used for the BALANCE/quality headers) would have
	// floored the sub-microcredit cost to 0 - prove the cost header no longer does.
	if round6(0.00000036) != 0 {
		t.Fatal("precondition: round6 should still floor 0.00000036 to 0 (the balance grid is unchanged)")
	}
	if fmtCostHeader(0.00000036) == "0" {
		t.Error("fmtCostHeader must NOT floor a real sub-microcredit charge to 0 (the bug this fixes)")
	}
}
