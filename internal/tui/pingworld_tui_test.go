package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// pwModel returns a sized browse model ready to drop into the screensaver.
func pwModel(md mode) model {
	m := seedFor(120, md, false)
	m.width, m.height = 120, 34
	return m
}

// TestEnterPingWorldZ: pressing z in BROWSE stashes prevMode and enters the fullscreen world,
// sized from the live terminal, with the beat still ticking.
func TestEnterPingWorldZ(t *testing.T) {
	out, cmd := pwModel(modeBrowse).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	mm := asModel(out)
	if mm.mode != modePingWorld {
		t.Fatalf("z should enter modePingWorld, got %v", mm.mode)
	}
	if mm.prevMode != modeBrowse {
		t.Errorf("prevMode = %v, want modeBrowse", mm.prevMode)
	}
	if mm.world.w != 120 || mm.world.h != 34 {
		t.Errorf("world sized %dx%d, want 120x34", mm.world.w, mm.world.h)
	}
	if cmd == nil {
		t.Error("entering the world should keep the tick alive (non-nil cmd)")
	}
}

// TestPingWorldAnyKeyWakes: ANY key - INCLUDING ctrl+c - wakes back to prevMode without
// quitting RogerAI and without leaking the keystroke into the restored view.
func TestPingWorldAnyKeyWakes(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'x'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC}, // must wake, NOT quit
		{Type: tea.KeyEnter},
	} {
		m := pwModel(modeChat)
		m.prevMode = modeChat
		m.mode = modePingWorld
		out, cmd := m.Update(k)
		mm := asModel(out)
		if mm.mode != modeChat {
			t.Errorf("key %v should wake to prevMode (modeChat), got %v", k.Type, mm.mode)
		}
		if cmd == nil {
			t.Errorf("key %v should resume the beat (non-nil cmd)", k.Type)
		}
		if !strings.Contains(stripANSI(mm.status), "welcome back") {
			t.Errorf("key %v: expected a 'welcome back' toast, got %q", k.Type, stripANSI(mm.status))
		}
	}
}

// TestPingWorldTickAdvances: while the world is up, the shared tick advances the world frame
// and reschedules (it owns the beat, bypassing compact/idle slow-tick).
func TestPingWorldTickAdvances(t *testing.T) {
	m := pwModel(modeBrowse)
	m.mode = modePingWorld
	m.compact = true // even in compact, the world keeps the fast beat
	out, cmd := m.Update(tickMsg{})
	if mm := asModel(out); mm.world.frame != 1 {
		t.Errorf("tick should advance world frame to 1, got %d", mm.world.frame)
	}
	if cmd == nil {
		t.Error("tick in the world should reschedule (non-nil cmd)")
	}
}

// TestWindowSizeSizesWorld: a resize keeps the screensaver fullscreen.
func TestWindowSizeSizesWorld(t *testing.T) {
	out, _ := pwModel(modeBrowse).Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	if mm := asModel(out); mm.world.w != 200 || mm.world.h != 50 {
		t.Errorf("resize should size the world, got %dx%d", mm.world.w, mm.world.h)
	}
}

// TestSlashPingEntersWorld: /ping and /zen from a channel drop into the world.
func TestSlashPingEntersWorld(t *testing.T) {
	for _, verb := range []string{"/ping", "/zen"} {
		out, cmd := pwModel(modeChat).runSession(verb)
		if mm := asModel(out); mm.mode != modePingWorld {
			t.Errorf("%s should enter modePingWorld, got %v", verb, mm.mode)
		}
		if cmd == nil {
			t.Errorf("%s should keep the tick alive", verb)
		}
	}
}

// TestPalettePingEntersWorld: /ping from the command palette (m.run) enters the world too.
func TestPalettePingEntersWorld(t *testing.T) {
	out, _ := pwModel(modeBrowse).run("ping")
	if mm := asModel(out); mm.mode != modePingWorld {
		t.Errorf("palette ping should enter modePingWorld, got %v", mm.mode)
	}
}

// TestPingWorldViewIsFullscreen: View() in modePingWorld is JUST the world - no preset bar /
// header chrome - and matches the standalone world's frame exactly.
func TestPingWorldViewIsFullscreen(t *testing.T) {
	m := pwModel(modeBrowse)
	m.mode = modePingWorld
	m.world = pingWorldModel{w: 80, h: 24, frame: 5, seed: 7}
	got := m.View()
	if got != m.world.View() {
		t.Error("modePingWorld View() must be exactly the world (no TUI chrome)")
	}
	if strings.Contains(stripANSI(got), "TUNE IN") {
		t.Error("the screensaver View() leaked the preset bar (TUNE IN)")
	}
}

// TestPingInHelpListing: /help advertises /ping so it isn't a hidden command.
func TestPingInHelpListing(t *testing.T) {
	out, _ := pwModel(modeChat).runSession("/help")
	body := stripANSI(strings.Join(asModel(out).transcript, "\n"))
	if !strings.Contains(body, "/ping") {
		t.Errorf("/help should list /ping; got:\n%s", body)
	}
}
