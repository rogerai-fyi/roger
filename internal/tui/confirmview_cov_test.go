package tui

import (
	"strings"
	"testing"
)

// TestConfirmViewDetail renders the tune-in confirm screen with the detail panel
// expanded over a multi-station band and a known balance: it must show the under-cap
// note, the live range row (only present when stations>1), the input price, and the
// "~N replies" balance line - all the showDetail/haveBal branches.
func TestConfirmViewDetail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseSeed(100)
	m.mode = modeConnectConfirm
	m.showDetail = true
	m.haveBal = true
	m.balance = 4.00
	st := &offer{NodeID: "nyx-home", Model: "gpt-oss-20b", PriceIn: 0.20, PriceOut: 0.30, Online: true, TPS: 62}
	bd := band{model: "gpt-oss-20b", online: true, stations: 3, minOut: 0.30, maxOut: 0.90, cheapest: st}
	m.q = quote{b: bd, limit: Limit{MaxOut: 1.50}, typical: 800, estReply: 0.30 * 800 / 1e6}
	m.connected = st

	out := stripANSI(m.confirmView(100))
	for _, want := range []string{"TUNE IN", "under your", "cap", "live range", "input price", "balance", "replies"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirm detail view missing %q:\n%s", want, out)
		}
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("confirm view rendered blank")
	}
}

// TestConfirmViewMinimal renders the default (collapsed) confirm: a single-station band,
// no balance, no cap - so the detail block, the cap note, and the range row are all
// absent, and the simple accept/deny footer still renders.
func TestConfirmViewMinimal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseSeed(80)
	m.mode = modeConnectConfirm
	m.showDetail = false
	m.haveBal = false
	st := &offer{NodeID: "solo", Model: "gpt-oss-20b", PriceIn: 0.10, PriceOut: 0.20, Online: true, TPS: 30}
	bd := band{model: "gpt-oss-20b", online: true, stations: 1, minOut: 0.20, maxOut: 0.20, cheapest: st}
	m.q = quote{b: bd, limit: Limit{}, typical: 800, estReply: 0.20 * 800 / 1e6}

	out := stripANSI(m.confirmView(80))
	if !strings.Contains(out, "accept") || !strings.Contains(out, "deny") {
		t.Errorf("minimal confirm missing the accept/deny footer:\n%s", out)
	}
	if strings.Contains(out, "live range") {
		t.Errorf("a single-station confirm should NOT show a live range row:\n%s", out)
	}
	if strings.Contains(out, "under your") {
		t.Errorf("a no-cap confirm should NOT show the under-cap note:\n%s", out)
	}
}
