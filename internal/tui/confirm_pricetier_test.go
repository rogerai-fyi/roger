package tui

import (
	"strings"
	"testing"
)

// TestConfirmViewShowsPriceTier (#6): the tune-in confirm screen must show the price
// SIGNAL (the $-tier + a "good price" chip on the best tier), not just the raw price -
// so the operator can tell at a glance whether a channel is a good deal BEFORE opening
// it. The band list already shows it (priceTierSuffix / bandTierSuffix); the confirm
// view must match.
func TestConfirmViewShowsPriceTier(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseSeed(100)
	m.mode = modeConnectConfirm
	st := &offer{NodeID: "sage-badger", Model: "Qwen3-Coder-Next", PriceIn: 0.05, PriceOut: 0.20, Online: true, TPS: 8, PriceTier: 1}
	bd := band{model: "Qwen3-Coder-Next", online: true, stations: 1, minOut: 0.20, maxOut: 0.20, cheapest: st}
	m.q = quote{b: bd, limit: Limit{}, typical: 800, estReply: 0.20 * 800 / 1e6}

	out := stripANSI(m.confirmView(100))
	if !strings.Contains(out, "good price") {
		t.Fatalf("confirm view should show the price-tier signal ('good price' for tier 1), got:\n%s", out)
	}
}
