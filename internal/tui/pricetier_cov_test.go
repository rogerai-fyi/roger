package tui

import (
	"strings"
	"testing"
)

// TestPriceTierBadge mirrors the broker's renderPriceTier contract on the TUI side so the
// two surfaces read alike: FREE wins; tier 0 priced shows nothing; only tier 1 is
// editorialized ("good price"); $$..$$$$ are neutral; never any negative wording.
func TestPriceTierBadge(t *testing.T) {
	cases := []struct {
		tier     int
		price    float64
		wantBars string
		wantChip string
	}{
		{0, 0.0, "FREE", ""},
		{0, 0.05, "", ""},
		{1, 0.05, "$", "good price"},
		{2, 0.10, "$$", ""},
		{3, 0.20, "$$$", ""},
		{4, 0.40, "$$$$", ""},
	}
	for _, c := range cases {
		bars, chip := priceTierBadge(c.tier, c.price)
		if bars != c.wantBars || chip != c.wantChip {
			t.Errorf("priceTierBadge(%d,%.2f) = %q,%q want %q,%q", c.tier, c.price, bars, chip, c.wantBars, c.wantChip)
		}
		for _, bad := range []string{"expensive", "overpriced", "too ", "rip-off"} {
			if chip != "" && strings.Contains(strings.ToLower(chip), bad) {
				t.Errorf("tier %d chip %q contains negative wording %q", c.tier, chip, bad)
			}
		}
	}
}

// TestBandTierSuffixRenders covers the band-row suffix: an online band shows its cheapest
// station's tier ($ + a "good price" chip on tier 1; bare $$$$ on tier 4, no negatives);
// an offline band and a free band show no $-tier suffix.
func TestBandTierSuffixRenders(t *testing.T) {
	o1 := offer{PriceOut: 0.05, PriceTier: 1, Online: true}
	if s := stripANSI(bandTierSuffix(band{online: true, minOut: 0.05, cheapest: &o1})); !strings.Contains(s, "$") || !strings.Contains(s, "good price") {
		t.Errorf("tier-1 band suffix = %q, want $ + good price", s)
	}
	o4 := offer{PriceOut: 0.40, PriceTier: 4, Online: true}
	s4 := stripANSI(bandTierSuffix(band{online: true, minOut: 0.40, cheapest: &o4}))
	if !strings.Contains(s4, "$$$$") {
		t.Errorf("tier-4 band suffix = %q, want $$$$", s4)
	}
	for _, bad := range []string{"expensive", "overpriced", "rip-off"} {
		if strings.Contains(strings.ToLower(s4), bad) {
			t.Errorf("tier-4 suffix %q contains negative wording %q", s4, bad)
		}
	}
	if bandTierSuffix(band{online: false}) != "" {
		t.Error("an offline band should have no $-tier suffix")
	}
	// A free band (price 0) shows no $-tier (the row's FREE tag already conveys it).
	oFree := offer{PriceOut: 0, PriceTier: 0, Online: true}
	if bandTierSuffix(band{online: true, minOut: 0, cheapest: &oFree}) != "" {
		t.Error("a free band should have no $-tier suffix")
	}
}
