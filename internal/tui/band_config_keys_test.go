package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// THE BAND CARD'S KEYS. The card is a HUB: every row routes into the editor that already
// owns that setting and comes back. These drive each route, because a hub whose spokes are
// untested is a screen that looks right and does nothing.

func cardKeys(t *testing.T) model {
	t.Helper()
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	mm, _ := m.openBandConfig("grok-4.6", modeBrowse)
	return asModel(mm)
}

// e and t hand off to the [3] CONFIG spend-limit editor, parked on THIS band's row and on
// the field the key names - so the edit uses the same buffer and the same save path.
func TestTheCardRoutesToTheSpendEditor(t *testing.T) {
	for _, tc := range []struct {
		key   string
		field int
	}{{"e", 0}, {"t", 1}} {
		m := cardKeys(t)
		m.bands = []band{{model: "grok-4.6", online: true, cheapest: &offer{Model: "grok-4.6"}}}
		out, _ := m.onBandConfigKey(keyMsg(tc.key))
		gm := asModel(out)
		if gm.mode != modeLimits {
			t.Errorf("%q did not open the spend editor (mode %v)", tc.key, gm.mode)
			continue
		}
		if gm.editField != tc.field {
			t.Errorf("%q opened field %d, want %d", tc.key, gm.editField, tc.field)
		}
		// And it must remember to come BACK to the card rather than stranding the operator
		// on a table they never opened.
		if !gm.limReturnSet || gm.limReturn != modeBandConfig {
			t.Errorf("%q did not set the return path back to the card", tc.key)
		}
	}
}

// n opens the rotate confirm for the band behind this model.
func TestTheCardRoutesToTheRotateConfirm(t *testing.T) {
	m := cardKeys(t)
	out, _ := m.onBandConfigKey(keyMsg("n"))
	if got := asModel(out).mode; got != modeBandRotateConfirm {
		t.Errorf("n did not open the rotate confirm (mode %v)", got)
	}
}

// l opens the naming input, seeded with the current label so it edits rather than clears.
func TestTheCardRoutesToTheNameInput(t *testing.T) {
	m := cardKeys(t)
	m.rcBands[0].Label = "home gpu"
	out, _ := m.onBandConfigKey(keyMsg("l"))
	gm := asModel(out)
	if gm.mode != modeBandLabel {
		t.Fatalf("l did not open the name input (mode %v)", gm.mode)
	}
	if gm.cfgLabelIn.Value() != "home gpu" {
		t.Errorf("the input did not seed from the current name, got %q", gm.cfgLabelIn.Value())
	}
	// esc backs out to the card without writing.
	back, cmd := gm.onBandLabelKey(keyMsg2(tea.KeyEsc))
	if asModel(back).mode != modeBandConfig || cmd != nil {
		t.Error("esc should return to the card and write nothing")
	}
	// enter commits - an EMPTY name is a legitimate value (it clears), so it must still act.
	gm.cfgLabelIn.SetValue("")
	_, cmd = gm.onBandLabelKey(keyMsg2(tea.KeyEnter))
	if cmd == nil {
		t.Error("enter on an empty name must still commit - empty clears the name")
	}
}

// The provider keys are REFUSED for a band this machine does not serve, and say why -
// inviting someone to price or air a model they do not own is a promise the product
// cannot keep.
func TestTheCardRefusesProviderKeysForABandYouDoNotServe(t *testing.T) {
	m := privateTab(t)
	m.ctrl.SetRows(nil)
	m.syncShareCache()
	m.bands = []band{{model: "deepseek-v4-flash", online: true, cheapest: &offer{Model: "deepseek-v4-flash"}}}
	mm, _ := m.openBandConfig("deepseek-v4-flash", modeBrowse)
	card := asModel(mm)

	for _, key := range []string{"a", "h", "p"} {
		out, _ := card.onBandConfigKey(keyMsg(key))
		gm := asModel(out)
		if gm.mode != modeBandConfig {
			t.Errorf("%q acted on a band this machine does not serve (mode %v)", key, gm.mode)
		}
		if strings.TrimSpace(stripANSI(gm.status)) == "" {
			t.Errorf("%q refused silently - the card must say why", key)
		}
	}
}

// esc returns to the list that opened the card; r re-scans without leaving it.
func TestTheCardEscapesAndRescans(t *testing.T) {
	m := cardKeys(t)
	out, _ := m.onBandConfigKey(keyMsg2(tea.KeyEsc))
	if got := asModel(out).mode; got != modeBrowse {
		t.Errorf("esc returned to mode %v, want the list that opened the card", got)
	}
	m2 := cardKeys(t)
	out2, cmd := m2.onBandConfigKey(keyMsg("r"))
	if cmd == nil {
		t.Error("r issued no re-scan")
	}
	if asModel(out2).mode != modeBandConfig {
		t.Error("r moved the operator off the card")
	}
}

// ⏎ uses the band. For one served here that is the DIRECT channel - the same opener the
// PRIVATE tab uses, so the two surfaces cannot disagree about reachability.
func TestTheCardTunesInDirect(t *testing.T) {
	m := cardKeys(t)
	out, _ := m.onBandConfigKey(keyMsg2(tea.KeyEnter))
	gm := asModel(out)
	if gm.mode != modeChat {
		t.Fatalf("⏎ did not open a channel (mode %v)", gm.mode)
	}
	if gm.chatLocalChat == "" {
		t.Error("the channel is not bound to the local server - it went through the broker")
	}
}

// The provider toggles route through the SAME controller calls [2] SHARE makes - one
// behaviour, two doors. These drive the lookup half (which model's row) without standing
// up a broker: a model that is not in the share table must be a no-op rather than a panic
// or a toggle of whatever happened to be at that index.
func TestTheCardsTogglesActOnlyOnTheirOwnRow(t *testing.T) {
	m := cardKeys(t)
	m.cfgModel = "not-on-this-machine"
	for _, fn := range []func() (tea.Model, tea.Cmd){m.cfgToggleOnAir, m.cfgTogglePrivate, m.cfgOpenPricing} {
		out, _ := fn()
		if asModel(out).mode != modeBandConfig {
			t.Error("a toggle for a model with no share row moved the operator somewhere")
		}
	}
}

// p opens the REAL pricing editor with the share cursor parked on this band's row, so the
// editor prices the model the card is about rather than whatever the cursor last touched.
func TestTheCardPricesTheRightRow(t *testing.T) {
	m := cardKeys(t)
	m.ctrl.SetLoggedIn(true)
	m.loggedIn = true
	out, _ := m.cfgOpenPricing()
	gm := asModel(out)
	if gm.mode != modeShareEditor {
		t.Fatalf("p did not open the pricing editor (mode %v)", gm.mode)
	}
	if gm.edModel != "grok-4.6" {
		t.Errorf("the editor is pricing %q, not the card's band", gm.edModel)
	}
}

// ⏎ on a band with NO private band of ours is an ordinary market tune-in: the card hands
// it to the dial rather than inventing a second connect path, and parks the cursor on that
// band so the confirm names the right one.
func TestTheCardTunesInAMarketBand(t *testing.T) {
	m := privateTab(t)
	m.ctrl.SetRows(nil)
	m.syncShareCache()
	m.rcBands = nil // no bands of our own at all
	m.bands = []band{
		{model: "deepseek-v4-flash", online: true, stations: 1,
			cheapest: &offer{Model: "deepseek-v4-flash", Online: true}},
	}
	m.scanned = true
	mm, _ := m.openBandConfig("deepseek-v4-flash", modeBrowse)
	out, _ := asModel(mm).cfgUse()
	gm := asModel(out)
	if gm.mode == modeBandConfig {
		t.Errorf("⏎ on a market band did nothing (status %q)", stripANSI(gm.status))
	}
	if gm.chatLocalChat != "" {
		t.Error("a market band was opened as a DIRECT channel")
	}
}

// A band nothing is serving must say so rather than opening a dead channel.
func TestTheCardSaysWhenNothingServesTheBand(t *testing.T) {
	m := privateTab(t)
	m.ctrl.SetRows(nil)
	m.syncShareCache()
	m.rcBands = nil
	m.bands = nil
	mm, _ := m.openBandConfig("ghost-model", modeBrowse)
	out, _ := asModel(mm).cfgUse()
	if !strings.Contains(stripANSI(asModel(out).status), "nothing is serving") {
		t.Errorf("an unreachable band opened silently: %q", stripANSI(asModel(out).status))
	}
}
