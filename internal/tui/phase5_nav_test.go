package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestStickyCursorSurvivesResort pins the wrong-tune fix: when a periodic re-scan re-sorts the
// band list, the cursor must FOLLOW the selected band (by name), not stay on a bare index that
// now points at a different band. Otherwise Enter mid-scroll tunes the wrong one.
func TestStickyCursorSurvivesResort(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	m.sortMode = sortStations
	m.bands = []band{{model: "alpha", online: true, stations: 2}, {model: "beta", online: true, stations: 1}}
	m.cursor = 1
	m.syncSelected() // sorted alpha(2),beta(1); index 1 == beta
	if m.selectedModel != "beta" {
		t.Fatalf("precondition: selectedModel=%q, want beta", m.selectedModel)
	}
	// a rescan flips the station counts -> beta now sorts FIRST (index 0)
	m.bands = []band{{model: "alpha", online: true, stations: 1}, {model: "beta", online: true, stations: 2}}
	m.clampBrowse()
	if got := m.visibleBands()[m.cursor].model; got != "beta" {
		t.Errorf("cursor must stick to 'beta' across the re-sort; landed on %q (cursor=%d)", got, m.cursor)
	}
}

// TestSKeySortsNotShare: lowercase s now cycles the sort (like S) and never opens SHARE.
func TestSKeySortsNotShare(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	before := m.sortMode
	out, _ := m.Update(keyMsgRunes('s'))
	om := asModel(out)
	if om.inShareSection() {
		t.Error("s must not open SHARE anymore")
	}
	if om.sortMode == before {
		t.Errorf("s should advance the sort dial (was %d, still %d)", before, om.sortMode)
	}
}

func keyMsgRunes(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
