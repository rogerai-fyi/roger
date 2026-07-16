package tui

// The tick loop carries a GENERATION token so it can never stack parallel chains. The
// pre-push claude-audit caught the bug: the BROWSE dial-glide kick (`return m, tick()` on
// every up/down) spawned a duplicate bubbletea tick chain per keypress - accumulating loops
// that double the animation cadence and re-poll /discover (the 429 flicker). Fix: a kick
// bumps m.tickGen; a tickMsg from an older generation is stale and is dropped (no frame
// advance, no reschedule), so exactly ONE chain stays live.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A live tick (current gen) advances the clock; a STALE tick (old gen) is dropped entirely.
func TestTickGenDropsStaleChains(t *testing.T) {
	m := browseSeed(120)
	m.agentBusy = true // force `animating` so a live tick actually advances m.frame
	m.mode = modeAgent

	// Current-gen tick: advances the frame.
	startFrame := m.frame
	nm, _ := m.Update(tickMsg{gen: m.tickGen})
	m = asModel(nm)
	if m.frame != startFrame+1 {
		t.Fatalf("a current-gen tick should advance the frame: %d -> %d", startFrame, m.frame)
	}

	// Stale-gen tick: must NOT advance the frame and must NOT reschedule (a dead chain).
	before := m.frame
	nm, cmd := m.Update(tickMsg{gen: m.tickGen - 1})
	m = asModel(nm)
	if m.frame != before {
		t.Errorf("a stale-gen tick must not advance the frame (%d -> %d)", before, m.frame)
	}
	if cmd != nil {
		t.Error("a stale-gen tick must not reschedule (nil cmd) - else the dead chain lives on")
	}
}

// Each BROWSE navigation kick bumps the generation, so N up/down presses collapse to ONE
// live chain (the newest gen), never N parallel loops.
func TestDialKickCollapsesToOneChain(t *testing.T) {
	m := browseSeed(120) // BROWSE with bands; up/down kicks the dial glide
	g0 := m.tickGen
	for i := 0; i < 3; i++ {
		nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = asModel(nm)
		if cmd == nil {
			t.Fatalf("nav %d: down should return a (kick) cmd", i)
		}
	}
	if m.tickGen != g0+3 {
		t.Errorf("3 navigations should bump the gen to %d (each kick orphans the prior chain), got %d", g0+3, m.tickGen)
	}
	// After the kicks, only the LATEST gen is live: a tick at any earlier gen is dropped.
	before := m.frame
	nm, cmd := m.Update(tickMsg{gen: g0}) // the pre-navigation chain
	if asModel(nm).frame != before || cmd != nil {
		t.Error("a pre-navigation (stale) chain must be dead after the kicks")
	}
}

// The initial chain is gen 0 (Init seeds it without kicking), so a plain tickMsg{} still
// drives a freshly-created model - the many existing `Update(tickMsg{})` tests stay valid.
func TestInitialChainIsGenZero(t *testing.T) {
	m := browseSeed(120)
	if m.tickGen != 0 {
		t.Fatalf("a fresh model starts at gen 0, got %d", m.tickGen)
	}
	m.agentBusy, m.mode = true, modeAgent
	f := m.frame
	nm, _ := m.Update(tickMsg{}) // gen 0
	if asModel(nm).frame != f+1 {
		t.Error("tickMsg{} (gen 0) must drive a fresh (gen-0) model")
	}
}
