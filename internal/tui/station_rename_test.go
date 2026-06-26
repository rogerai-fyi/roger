package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/agent"
)

// keyRunes builds a printable-rune key message (what typing a callsign produces).
func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// TestStationSeededFromHooks: the live station is seeded from the host hook (slugged),
// NOT the hostname, so the first derived node id already carries the friendly callsign.
func TestStationSeededFromHooks(t *testing.T) {
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{Station: "Brave Otter"})
	if mm.station != "brave-otter" {
		t.Fatalf("station not seeded/slugged from hook: %q", mm.station)
	}
	// The node id the share path would mint carries the station + model, no host/port.
	id := agent.ShareNodeID(mm.station, "gpt-oss-20b", 0)
	if id != "brave-otter-gpt-oss-20b" {
		t.Fatalf("derived node id wrong: %q", id)
	}
}

// TestStationRenamePersistsAndDisplays: pressing `n`, typing a new callsign, and enter
// updates the live station, calls SaveStation (persistence), and the SHARE view shows
// the new name - the founder's #2 (rename in the TUI).
func TestStationRenamePersistsAndDisplays(t *testing.T) {
	var saved string
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{
		Station:     "brave-otter",
		SaveStation: func(s string) { saved = s },
	})
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.setShareRows([]shareRow{{model: "gpt-oss-20b", ctx: 32768}})
	var m tea.Model = mm

	// Enter rename mode - the buffer seeds with the current callsign so the owner can
	// edit it in place.
	m, _ = m.Update(keyRunes("n"))
	if !asModel(m).renaming {
		t.Fatal("`n` did not enter rename mode")
	}
	if got := asModel(m).stationEdit; got != "brave-otter" {
		t.Fatalf("rename buffer not seeded with current station: %q", got)
	}
	// Clear the seed, then type a new callsign (free-text; it gets slugged on commit).
	for range "brave-otter" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "Swift Fox" {
		m, _ = m.Update(keyRunes(string(r)))
	}
	if got := asModel(m).stationEdit; got != "Swift Fox" {
		t.Fatalf("edit buffer wrong: %q", got)
	}
	// Commit.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = asModel(m)
	if mm.renaming {
		t.Fatal("still renaming after enter")
	}
	if mm.station != "swift-fox" {
		t.Fatalf("station not renamed/slugged: %q", mm.station)
	}
	if saved != "swift-fox" {
		t.Fatalf("SaveStation not called with the slugged name: %q", saved)
	}
	// The new station appears in the SHARE view; the node id it derives carries it.
	if v := m.View(); !strings.Contains(stripANSI(v), "swift-fox") {
		t.Errorf("SHARE view does not show the renamed station:\n%s", stripANSI(v))
	}
	if id := agent.ShareNodeID(mm.station, "gpt-oss-20b", 0); id != "swift-fox-gpt-oss-20b" {
		t.Errorf("renamed node id wrong: %q", id)
	}
}

// TestStationRenameCancel: esc cancels without touching the live station or persisting.
func TestStationRenameCancel(t *testing.T) {
	var saved string
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{
		Station:     "brave-otter",
		SaveStation: func(s string) { saved = s },
	})
	mm.mode = modeShare
	mm.setShareRows([]shareRow{{model: "gpt-oss-20b", ctx: 32768}})
	var m tea.Model = mm
	m, _ = m.Update(keyRunes("n"))
	m, _ = m.Update(keyRunes("zzz"))
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = asModel(m)
	if mm.station != "brave-otter" {
		t.Fatalf("esc changed the station: %q", mm.station)
	}
	if saved != "" {
		t.Fatalf("esc persisted a station: %q", saved)
	}
}

// TestStationRenameBlankKeepsCurrent: committing a blank/punctuation-only callsign does
// NOT blank the station (it slugs to "" and we keep the current one).
func TestStationRenameBlankKeepsCurrent(t *testing.T) {
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{Station: "brave-otter"})
	mm.mode = modeShare
	mm.setShareRows([]shareRow{{model: "gpt-oss-20b", ctx: 32768}})
	var m tea.Model = mm
	m, _ = m.Update(keyRunes("n"))
	m, _ = m.Update(keyRunes("   "))
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asModel(m).station; got != "brave-otter" {
		t.Fatalf("blank rename blanked/changed the station: %q", got)
	}
}
