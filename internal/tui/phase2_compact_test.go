package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func altM() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true} }

// TestAltMMinimizesFromAnywhere: the founder's "press m from anywhere to minimize" - alt+m is
// the typing-safe global that toggles the dense compact windowshade from EVERY mode, including
// the text-entry ones (chat / AGENT / command palette) where plain m is a literal character.
func TestAltMMinimizesFromAnywhere(t *testing.T) {
	for _, md := range []mode{modeBrowse, modeChat, modeAgent, modeCommand, modeShare, modeLimits} {
		m := seedFor(120, md, false)
		if m.compact {
			t.Fatalf("mode %v precondition: should start expanded", md)
		}
		out, _ := m.Update(altM())
		if !asModel(out).compact {
			t.Errorf("alt+m in mode %v should minimize (compact=true)", md)
		}
	}
}

// TestAltMDoesNotTypeIntoChat: alt+m must be intercepted globally, never inserted as 'm' into
// a focused chat input (the whole reason it's alt-chorded, not plain m, in text-entry modes).
func TestAltMDoesNotTypeIntoChat(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.chatIn.SetValue("")
	m.chatIn.Focus()
	out, _ := m.Update(altM())
	om := asModel(out)
	if om.chatIn.Value() != "" {
		t.Errorf("alt+m must not type 'm' into the chat input; got %q", om.chatIn.Value())
	}
	if !om.compact {
		t.Error("alt+m from chat should still minimize")
	}
}

// TestAltMRestoresFromCompact: minimize toggles - alt+m from compact expands back.
func TestAltMRestoresFromCompact(t *testing.T) {
	m := seedFor(120, modeChat, true)
	if !m.compact {
		t.Fatal("precondition: should start compact")
	}
	out, _ := m.Update(altM())
	if asModel(out).compact {
		t.Error("alt+m from compact should restore (expand)")
	}
}

// TestSlashCompactToggles: /compact + /min minimize from a channel, and the palette verb works
// too - the discoverable, typing-safe routes alongside alt+m.
func TestSlashCompactToggles(t *testing.T) {
	for _, v := range []string{"/compact", "/min"} {
		out, _ := seedFor(120, modeChat, false).runSession(v)
		if !asModel(out).compact {
			t.Errorf("%s should minimize to compact", v)
		}
	}
	if out, _ := seedFor(120, modeBrowse, false).run("compact"); !asModel(out).compact {
		t.Error("palette 'compact' should minimize")
	}
}

// TestPlainMStillMinimizesInBrowse: regression - the convenient single-key m must keep working
// on the nav screens (the presetForKey path), unchanged by adding the alt+m global.
func TestPlainMStillMinimizesInBrowse(t *testing.T) {
	out, _ := seedFor(120, modeBrowse, false).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !asModel(out).compact {
		t.Error("plain m in browse should still minimize (presetForKey path)")
	}
}
