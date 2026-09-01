package tui

// Pre-push audit round 5, TUI trio - each was a real key/paint bug:
//  - withoutCurated treated minIn==0 as "unset", so under the U filter a FREE human
//    station's 0 in-price was overwritten by a later paid one (groupBands seeds from the
//    first station and this claims parity with it);
//  - with the cursor UP on the monthly-budget row, d cleared (and b opened the card of)
//    the un-highlighted band still under limCursor - editing a thing you are not looking at.

import "testing"

func TestHideCuratedKeepsAFreeHumanInPrice(t *testing.T) {
	b := band{
		model: "m", online: true, stations: 3, curated: 1, curatedProvider: "openrouter",
		all: []offer{
			{Model: "m", Online: true, PriceIn: 0, PriceOut: 0},          // FREE human
			{Model: "m", Online: true, PriceIn: 5, PriceOut: 5},          // paid human
			{Model: "m", Online: true, Curated: true, PriceIn: 1.3, PriceOut: 1.3},
		},
	}
	nb := b.withoutCurated()
	if nb.stations != 2 || nb.curated != 0 {
		t.Fatalf("stations=%d curated=%d, want 2 human / 0 curated", nb.stations, nb.curated)
	}
	if nb.minIn != 0 {
		t.Fatalf("minIn = %v, want 0: the free station's in-price is the band's headline, exactly as groupBands seeds it", nb.minIn)
	}
	if !nb.free {
		t.Fatal("the free human station must keep the band marked free")
	}
}

func TestBudgetRowKeysNeverActOnTheBandBelow(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeLimits
	m.limModels = []string{"m1"}
	m.limCursor = 0
	m.limOnBudget = true // the cursor is UP on the wallet's monthly-budget row
	m.editField = -1     // browsing the table, not editing a field
	m.limits = &LimitStore{Models: map[string]Limit{"m1": {MinTPS: 2}}}

	out, _ := m.onLimitsKey(keyMsg("d"))
	gm := asModel(out)
	if got := gm.limits.resolve("m1"); got.MinTPS != 2 {
		t.Fatalf("d on the budget row cleared band m1's limits (MinTPS=%v): the key acted on a row the cursor is not on", got.MinTPS)
	}

	m2 := browseSeed(100)
	m2.mode = modeLimits
	m2.limModels = []string{"m1"}
	m2.limCursor = 0
	m2.limOnBudget = true
	m2.editField = -1
	m2.limits = &LimitStore{Models: map[string]Limit{}}
	out2, _ := m2.onLimitsKey(keyMsg("b"))
	if asModel(out2).mode != modeLimits {
		t.Fatalf("b on the budget row opened the band card of the un-highlighted row (mode=%v)", asModel(out2).mode)
	}
}
