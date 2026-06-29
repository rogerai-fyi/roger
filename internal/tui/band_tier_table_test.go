package tui

import (
	"strings"
	"testing"
)

// TestBandTierTag: the compact wide-table tier glyphs - "$".."$$$$" for an online priced
// band by its cheapest station's tier, and "" for free / offline / unknown.
func TestBandTierTag(t *testing.T) {
	if g := bandTierTag(band{online: true, minOut: 0.30, cheapest: &offer{PriceTier: 2, PriceOut: 0.30}}); g != "$$" {
		t.Errorf("tier-2 band tag = %q, want $$", g)
	}
	if g := bandTierTag(band{online: true, minOut: 0.40, cheapest: &offer{PriceTier: 4, PriceOut: 0.40}}); g != "$$$$" {
		t.Errorf("tier-4 band tag = %q, want $$$$", g)
	}
	if g := bandTierTag(band{online: false, cheapest: &offer{PriceTier: 2}}); g != "" {
		t.Errorf("offline band should have no tier tag, got %q", g)
	}
	if g := bandTierTag(band{online: true, minOut: 0, free: true, cheapest: &offer{PriceTier: 0}}); g != "" {
		t.Errorf("free band should have no tier tag, got %q", g)
	}
}

// TestPriceInOutTier: the price cell carries the tier next to the price when it fits, drops
// it (gracefully) when it would overflow the column, and keeps the honest offline "-".
func TestPriceInOutTier(t *testing.T) {
	cheap := band{online: true, minIn: 0.20, minOut: 0.30, cheapest: &offer{PriceTier: 1, PriceOut: 0.30}}
	got := priceInOutTier(cheap, 17)
	if !strings.Contains(got, "0.20·0.30") || !strings.Contains(got, "$") {
		t.Errorf("price+tier cell = %q, want the price AND a $-tier", got)
	}

	// Offline -> bare "-", no tier.
	if g := priceInOutTier(band{online: false}, 17); g != "-" {
		t.Errorf("offline price cell = %q, want '-'", g)
	}

	// A pricey band whose price+tier would overflow the 17-col cell: the tag is dropped so
	// the grid never breaks (the big number already reads as expensive).
	pricey := band{online: true, minIn: 100, minOut: 120, cheapest: &offer{PriceTier: 4, PriceOut: 120}}
	if g := priceInOutTier(pricey, 17); len([]rune(g)) > 17 || strings.Contains(g, "$$$$") {
		t.Errorf("over-wide price+tier should drop the tag and stay within 17 cols, got %q (%d runes)", g, len([]rune(g)))
	}
}

// TestBandRowShowsPriceTier (integration): the WIDE band-list row renders the tier tag
// next to the price, so price can be judged at a glance - the same tier the [i] detail view
// shows. Mirrors the in·out price row test.
func TestBandRowShowsPriceTier(t *testing.T) {
	out := browseRowView(t, 120, offer{
		NodeID: "a", Model: "qwen3-8b", PriceIn: 0.10, PriceOut: 0.40, PriceTier: 2, Online: true, Signal: 70,
	})
	if !strings.Contains(out, "0.10·0.40 $$") {
		t.Errorf("the wide band row should show the tier tag ($$) next to the price:\n%s", out)
	}
}

// TestBandRowFreeNoTier (integration): a free band's price cell reads "free" and carries
// NO $-tier tag (the "free" label already conveys the price).
func TestBandRowFreeNoTier(t *testing.T) {
	out := browseRowView(t, 120, offer{
		NodeID: "f", Model: "free-model", PriceIn: 0, PriceOut: 0, PriceTier: 0, Online: true, Signal: 50,
	})
	if !strings.Contains(out, "free") {
		t.Errorf("a free band's price cell should read 'free':\n%s", out)
	}
	if strings.Contains(out, "$$") { // the only multi-$ runs would be a tier tag
		t.Errorf("a free band must not show a $-tier tag:\n%s", out)
	}
}
