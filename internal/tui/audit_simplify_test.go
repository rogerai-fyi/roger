package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRightArrowScrollsNotOpensDetail (audit #4): the big newcomer stumble was that
// arrow-right OPENED the per-station detail panel instead of navigating. Right must now
// be plain section navigation (the preset cycle, identical to pressing the next
// preset's number) and never open modeBandDetail. Only [i] opens the inspect view.
func TestRightArrowScrollsNotOpensDetail(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg([]offer{
		{NodeID: "nyx-gpt-oss-20b", Model: "gpt-oss-20b", Online: true, PriceOut: 0.30, Signal: 60},
	}))
	m, _ = m.Update(tickMsg{})

	// Right from BROWSE must NOT open the detail panel - it navigates (here: to SHARE,
	// the next preset after TUNE IN). This is the load-bearing "right scrolls" assertion.
	r, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if asModel(r).mode == modeBandDetail {
		t.Fatalf("arrow-right must NOT open the station detail panel (it now navigates); mode=%v", asModel(r).mode)
	}
	if asModel(r).mode != modeShare {
		t.Errorf("arrow-right from TUNE IN should navigate to the next section (SHARE), got %v", asModel(r).mode)
	}

	// [i] is the ONE inspect key and still opens the detail panel.
	i, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if asModel(i).mode != modeBandDetail {
		t.Errorf("[i] should still open the station detail panel, got %v", asModel(i).mode)
	}
}

// TestHelpGlossaryRenders (audit #6): /help carries a plain-language glossary mapping
// the radio jargon AND one line per signal-breakdown factor, so the numbers are
// interpretable. Nothing is renamed in the UI; the help screen teaches it.
func TestHelpGlossaryRenders(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 60
	m.mode = modeHelp
	v := stripANSI(m.View())

	if !strings.Contains(v, "glossary") {
		t.Fatalf("HELP should carry a glossary block:\n%s", v)
	}
	// The radio-jargon map: band=model, station=provider, on air=serving,
	// confidential=hardware-private (TEE), frequency code=private-band key.
	jargon := []string{
		"band", "a model",
		"station", "provider",
		"on air", "serving",
		"confidential", "hardware-private",
		"frequency code", "private-band",
	}
	for _, want := range jargon {
		if !strings.Contains(v, want) {
			t.Errorf("glossary missing jargon term/gloss %q:\n%s", want, v)
		}
	}
	// One plain line per signal-breakdown factor (supply/speed/latency/verified/
	// success/trust) so "signal 82 = supply 15 · …" is interpretable.
	for _, f := range []string{"supply", "speed", "latency", "verified", "success", "trust"} {
		if !strings.Contains(v, f) {
			t.Errorf("glossary missing signal factor %q:\n%s", f, v)
		}
	}
}

// TestSectionIndicatorAppearsOnce (audit #9): the "where am I" SECTION status is shown
// once - in the header section badge - not triplicated across the preset bar + header +
// footer. The header badge names the current section ("TUNE IN"); the preset bar above
// is the keyboard NAV menu (it lists every section + its key) and is not a duplicate
// "you are here" status. We assert the header line carries the badge and that the
// redundant "TUNE IN │ SHARE" toggle-pair + "mode BROWSE" restatement are gone.
func TestSectionIndicatorAppearsOnce(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	v := stripANSI(m.View())
	lines := strings.Split(v, "\n")

	// Exactly one line is the header section BADGE (names the current section + [s]).
	badgeLines := 0
	for _, l := range lines {
		if strings.Contains(l, "TUNE IN [s]") {
			badgeLines++
		}
	}
	if badgeLines != 1 {
		t.Errorf("the section badge ('TUNE IN [s]') should appear exactly once, got %d:\n%s", badgeLines, v)
	}

	// The old triple-indicator clutter is gone: no "TUNE IN │ SHARE" toggle-pair badge
	// and no redundant "mode BROWSE" restatement on the resting browse screen.
	if strings.Contains(v, "TUNE IN │ share") || strings.Contains(v, "TUNE IN │ SHARE") {
		t.Errorf("the header badge should NOT restate the whole TUNE IN│SHARE pair (de-dup #9):\n%s", v)
	}
	if strings.Contains(v, "mode BROWSE") {
		t.Errorf("the resting browse screen should NOT also say 'mode BROWSE' (de-dup #9):\n%s", v)
	}
}

// TestFooterDropsBrokerURL (audit #9): the footer is rule + one key-hint line +
// balance. The dead broker-URL line (it lives in /config) is gone, and the redundant
// 'c'-as-channel hint is dropped from the connected key-hint row (tab is kept).
func TestFooterDropsBrokerURL(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	v := stripANSI(m.View())
	// The broker URL must not ride in the footer anymore.
	if strings.Contains(v, "http://broker.local") {
		t.Errorf("footer should not carry the broker URL (it is in /config):\n%s", v)
	}

	// Connected key-hint row teaches 'tab channel', not the redundant 'tab/c channel'.
	cm := New("http://broker.local", "tester")
	cm.width, cm.height = 100, 40
	cm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	cv := stripANSI(cm.footer(100))
	if strings.Contains(cv, "tab/c channel") {
		t.Errorf("connected footer should drop the 'c' channel alias (keep tab):\n%s", cv)
	}
	if !strings.Contains(cv, "tab channel") {
		t.Errorf("connected footer should still teach 'tab channel':\n%s", cv)
	}
}
