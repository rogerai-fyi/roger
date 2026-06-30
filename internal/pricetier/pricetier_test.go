package pricetier

import (
	"strings"
	"testing"
)

// TestRender is the canonical render contract (previously duplicated as the broker's
// renderPriceTier, the TUI's priceTierBadge, and the client's priceTierLabel): FREE wins;
// tier 0 / out-of-range priced shows nothing; only tier 1 is editorialized ("good price");
// $$..$$$$ are neutral bars; the chip is NEVER negative wording.
func TestRender(t *testing.T) {
	cases := []struct {
		tier     int
		priceOut float64
		wantBars string
		wantChip string
	}{
		{0, 0.0, "FREE", ""},  // free
		{4, 0.0, "FREE", ""},  // free wins over any tier
		{2, -0.5, "FREE", ""}, // negative price is free
		{0, 0.05, "", ""},     // priced but unclassifiable: nothing
		{1, 0.05, "$", "good price"},
		{2, 0.10, "$$", ""},
		{3, 0.20, "$$$", ""},
		{4, 0.40, "$$$$", ""},
		{5, 0.20, "", ""},  // above range
		{-1, 0.20, "", ""}, // below range
	}
	for _, c := range cases {
		bars, chip := Render(c.tier, c.priceOut)
		if bars != c.wantBars || chip != c.wantChip {
			t.Errorf("Render(%d, %.2f) = %q,%q want %q,%q", c.tier, c.priceOut, bars, chip, c.wantBars, c.wantChip)
		}
		for _, bad := range []string{"expensive", "overpriced", "too ", "rip-off", "bad", "pricey", "costly"} {
			if chip != "" && strings.Contains(strings.ToLower(chip), bad) {
				t.Errorf("Render(%d) chip %q contains negative wording %q", c.tier, chip, bad)
			}
		}
	}
}

// TestLabel locks the flattened CLI cell to Render: FREE, the bars+chip join, or "".
func TestLabel(t *testing.T) {
	cases := []struct {
		tier     int
		priceOut float64
		want     string
	}{
		{4, 0, "FREE"},    // free wins over any tier
		{2, -0.5, "FREE"}, // negative price is free
		{1, 0.05, "$ good price"},
		{2, 0.20, "$$"},
		{3, 1.50, "$$$"},
		{4, 9.00, "$$$$"},
		{0, 0.20, ""},  // tier 0 unknown
		{5, 0.20, ""},  // above range
		{-1, 0.20, ""}, // below range
	}
	for _, c := range cases {
		if got := Label(c.tier, c.priceOut); got != c.want {
			t.Errorf("Label(%d, %v) = %q, want %q", c.tier, c.priceOut, got, c.want)
		}
	}
}
