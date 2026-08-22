package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ONE CARD PER BAND (band_config.go).
//
// Everything about a single model used to be scattered across four screens - on air and
// visibility in [2] SHARE, earnings in its pricing editor, spend caps in [3] CONFIG, the
// dial and its code in BASE STATION - and no screen anywhere could answer "how is this band
// set up?". The card is the detail view that was missing.

func cardFor(t *testing.T, model string) model {
	t.Helper()
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	m.limits.set("grok-4.6", Limit{MaxOut: 1.25, MinTPS: 8})
	mm, _ := m.openBandConfig(model, modeBrowse)
	return asModel(mm)
}

// THE HEADLINE: one screen carries every setting that applies, each naming its key.
func TestTheCardCarriesEverySettingForABandItServes(t *testing.T) {
	out := stripANSI(cardFor(t, "grok-4.6").bandConfigView(100))
	for _, want := range []string{
		"on air",       // [2] SHARE
		"visibility",   // [2] SHARE (h)
		"band",         // BASE STATION
		"name",         // the label, which had no write path at all
		"you earn",     // the pricing editor
		"max $/1M out", // [3] CONFIG
		"min t/s",      // [3] CONFIG
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the card is missing %q - it is still split across screens:\n%s", want, out)
		}
	}
	// And every controllable row names the key that changes it.
	for _, key := range []string{"a  toggle", "h  ", "n  new code", "l  name", "p  set price", "e  cap", "t  refuse"} {
		if !strings.Contains(out, key) {
			t.Errorf("a row does not name its key (%q):\n%s", key, out)
		}
	}
}

// An EXISTING spend cap must be visible and editable from the card even for a model that
// is not currently on the market - otherwise the card silently drops a setting the
// operator already made, which is worse than not showing the section at all.
func TestTheCardShowsASetCapEvenOffMarket(t *testing.T) {
	m := cardFor(t, "grok-4.6")
	if _, onMarket := m.bandForModel("grok-4.6"); onMarket {
		t.Skip("this lock is only meaningful for a model that is NOT on the dial")
	}
	out := stripANSI(m.bandConfigView(100))
	if !strings.Contains(out, "1.25") {
		t.Errorf("a spend cap the operator already set is invisible on the card:\n%s", out)
	}
}

// Sections are CONDITIONAL. A market band this machine does not serve must not offer a
// provider section - inviting someone to price a model they do not own is a promise the
// product cannot keep.
func TestTheCardHidesTheProviderHalfForABandYouDoNotServe(t *testing.T) {
	m := privateTab(t)
	m.ctrl.SetRows(nil)
	m.syncShareCache()
	m.bands = []band{{model: "deepseek-v4-flash", online: true, cheapest: &offer{Model: "deepseek-v4-flash"}}}
	mm, _ := m.openBandConfig("deepseek-v4-flash", modeBrowse)
	out := stripANSI(asModel(mm).bandConfigView(100))
	if strings.Contains(out, "you earn") || strings.Contains(out, "THIS MACHINE") {
		t.Errorf("the card offered provider settings for a band this machine does not serve:\n%s", out)
	}
	if !strings.Contains(out, "does not serve") {
		t.Errorf("the card must say why the provider half is absent:\n%s", out)
	}
}

// THE CARD NEVER SHOWS A CODE. Only the hash is stored, so there is nothing to show and a
// placeholder would read as the real thing. The cosmetic dial is fine - it is what /bands
// already prints.
func TestTheCardNeverRendersACode(t *testing.T) {
	m := cardFor(t, "grok-4.6")
	m.tuneFreq = "8F3K-QQ21-ZZ90"
	out := stripANSI(m.bandConfigView(100))
	if strings.Contains(out, "8F3K") {
		t.Errorf("the card rendered a frequency code:\n%s", out)
	}
	if !strings.Contains(out, "145.225 MHz") {
		t.Errorf("the card must still show the cosmetic dial:\n%s", out)
	}
}

// FREE is a word, and an unset cap is "-". A printed 0.00 reads as a measured charge, and
// a printed 0 cap reads as "refuse everything" - the opposite of no cap.
func TestTheCardNeverPrintsAMisleadingZero(t *testing.T) {
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	mm, _ := m.openBandConfig("grok-4.6", modeBrowse)
	out := stripANSI(asModel(mm).bandConfigView(100))
	if strings.Contains(out, "$0.00") {
		t.Errorf("the card priced a free band at $0.00:\n%s", out)
	}
	if !strings.Contains(out, "free") {
		t.Errorf("a free band must say so in words:\n%s", out)
	}
}

// An UNSET cap reads "-  (no cap)", never 0: a printed zero cap reads as "refuse
// everything", which is the opposite of what no cap means. Asserted on a MARKET band,
// because a model this machine merely serves has nothing metered and correctly shows no
// spend section at all.
func TestAnUnsetCapNeverPrintsAsZero(t *testing.T) {
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	m.bands = []band{{model: "deepseek-v4-flash", online: true, cheapest: &offer{Model: "deepseek-v4-flash"}}}
	mm, _ := m.openBandConfig("deepseek-v4-flash", modeBrowse)
	out := stripANSI(asModel(mm).bandConfigView(100))
	if !strings.Contains(out, "(no cap)") {
		t.Errorf("an unset cap must say (no cap), never 0:\n%s", out)
	}
}

// The card must not run off the screen, and its footer must fit beside the balance.
func TestTheCardFitsEveryWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100, 120} {
		m := cardFor(t, "grok-4.6")
		m.width = w
		for _, ln := range strings.Split(m.bandConfigView(w), "\n") {
			if got := lipgloss.Width(ln); got > w {
				t.Errorf("w=%d: a card line is %d wide: %q", w, got, stripANSI(ln))
			}
		}
		for _, ln := range strings.Split(m.footer(w), "\n") {
			if got := lipgloss.Width(ln); got > w {
				t.Errorf("w=%d: a card footer line is %d wide: %q", w, got, stripANSI(ln))
			}
		}
	}
}

// b opens the card from EVERY list that shows a band - one key, learned once.
func TestBOpensTheCardFromEveryList(t *testing.T) {
	cases := []struct {
		name  string
		setup func() model
	}{
		{"the open market", func() model {
			m := privateTab(t)
			m.tuneTab = tabOpenMarket
			m.bands = []band{{model: "grok-4.6", online: true, cheapest: &offer{Model: "grok-4.6"}}}
			return m
		}},
		{"the private tab", func() model { return privateTab(t) }},
		{"[2] SHARE", func() model {
			m := privateTab(t)
			m.mode = modeShare
			return m
		}},
		{"[3] CONFIG", func() model {
			m := privateTab(t)
			m.bands = []band{{model: "grok-4.6", online: true, cheapest: &offer{Model: "grok-4.6"}}}
			mm := &m
			mm.enterLimits() // the real entry: it is what leaves the view NOT mid-edit
			return *mm
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tm tea.Model = tc.setup()
			tm, _ = tm.Update(keyMsg("b"))
			gm := asModel(tm)
			if gm.mode != modeBandConfig {
				t.Fatalf("b did not open the band card from %s (mode %v)", tc.name, gm.mode)
			}
			if gm.cfgModel == "" {
				t.Error("the card opened with no band")
			}
		})
	}
}

// esc returns to the list that opened it, not to a screen the operator never chose.
func TestTheCardReturnsWhereItCameFrom(t *testing.T) {
	for _, back := range []mode{modeShare, modeLimits, modeBrowse} {
		m := privateTab(t)
		mm, _ := m.openBandConfig("grok-4.6", back)
		out, _ := asModel(mm).closeBandConfig()
		if got := asModel(out).mode; got != back {
			t.Errorf("the card returned to mode %v, want %v", got, back)
		}
	}
}
