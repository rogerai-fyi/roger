package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPingWalkUpdateLifecycle drives pingWalkModel.Update through every branch: a window
// resize records dimensions, a key press quits early, a tick steps Ping right, hitting
// the right edge wraps to the left and counts a lap, and completing pingWalkLaps quits.
func TestPingWalkUpdateLifecycle(t *testing.T) {
	// WindowSizeMsg records width/height.
	var m tea.Model = pingWalkModel{}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	pm := m.(pingWalkModel)
	if pm.width != 40 || pm.height != 20 {
		t.Fatalf("resize not recorded: w=%d h=%d", pm.width, pm.height)
	}

	// A tick advances the frame and stride without quitting.
	m2, cmd := pm.Update(walkTickMsg{})
	pm2 := m2.(pingWalkModel)
	if pm2.frame != 1 || pm2.x != 2 {
		t.Fatalf("tick should step frame=1 x=2, got frame=%d x=%d", pm2.frame, pm2.x)
	}
	if cmd == nil {
		t.Fatal("a mid-walk tick should schedule the next tick")
	}

	// Drive ticks until Ping is about to fall off the right edge, then one more wraps it.
	walk := pingWalkModel{width: 20, height: 10}
	for i := 0; i < 100; i++ {
		nm, c := walk.Update(walkTickMsg{})
		walk = nm.(pingWalkModel)
		if walk.laps >= 1 {
			// First wrap: x reset to the left re-entry, lap counted.
			if walk.x != -pingWalkW {
				t.Fatalf("wrap should reset x to -pingWalkW, got %d", walk.x)
			}
			_ = c
			break
		}
	}
	if walk.laps < 1 {
		t.Fatal("Ping never completed a lap across the width")
	}

	// Keep ticking until the final lap quits (done=true, Quit cmd).
	for i := 0; i < 200 && !walk.done; i++ {
		nm, _ := walk.Update(walkTickMsg{})
		walk = nm.(pingWalkModel)
	}
	if !walk.done {
		t.Fatal("Ping never finished its laps")
	}

	// Any key bails out cleanly with tea.Quit.
	_, qcmd := pingWalkModel{width: 20}.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if qcmd == nil {
		t.Fatal("a key press should return a Quit command")
	}
}

// TestPingWalkView covers the View branches: a zero width renders empty, and a sized
// model paints the mascot indented mid-screen (negative pad clamps to 0).
func TestPingWalkView(t *testing.T) {
	if v := (pingWalkModel{}).View(); v != "" {
		t.Errorf("zero-width view should be empty, got %q", v)
	}
	// Negative x clamps the indent to 0 and still draws the figure.
	neg := pingWalkModel{width: 40, height: 10, x: -pingWalkW}.View()
	if !strings.Contains(stripANSI(neg), "R") {
		t.Errorf("view at the left re-entry should still draw Ping:\n%s", stripANSI(neg))
	}
	// A positive x indents the figure.
	pos := pingWalkModel{width: 40, height: 10, x: 6}.View()
	if !strings.Contains(pos, "      ") {
		t.Errorf("a positive x should indent the figure:\n%s", stripANSI(pos))
	}
}

// TestPingWalkInit returns the first tick command.
func TestPingWalkInit(t *testing.T) {
	if (pingWalkModel{}).Init() == nil {
		t.Fatal("Init should schedule the first walk tick")
	}
}
