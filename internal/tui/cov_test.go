package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPureHelpers covers the small free helpers: firstLine, normalizeUpstream,
// cornerEyeFor, and SetVersion.
func TestPureHelpers(t *testing.T) {
	if firstLine("one\ntwo\nthree") != "one" {
		t.Errorf("firstLine should return the first line")
	}
	if firstLine("solo") != "solo" {
		t.Errorf("firstLine(no newline) should return the whole string")
	}
	if normalizeUpstream("http://x:1/v1/chat/completions") == "" {
		t.Error("normalizeUpstream should pass through to node.NormalizeUpstream")
	}
	// cornerEyeFor: streaming swells •->O every cornerCadence ticks (a calm, deliberate pulse).
	if cornerEyeFor(poseStreaming, 0) != "•" || cornerEyeFor(poseStreaming, cornerCadence) != "O" {
		t.Errorf("cornerEyeFor(streaming) frames wrong")
	}
	if cornerEyeFor(poseWaiting, 5) != "•" {
		t.Errorf("cornerEyeFor(idle) should be the dot")
	}
	// SetVersion: empty is a no-op; a value is accepted (with/without leading v).
	SetVersion("")
	SetVersion("9.9.9")
	SetVersion("v9.9.10")
}

// TestPingWalkModel covers the Ping-walk easter-egg sub-model: Init schedules a tick,
// Update advances on a tick and quits on a key, and View renders within a sized frame.
func TestPingWalkModel(t *testing.T) {
	var m tea.Model = pingWalkModel{}
	if cmd := m.Init(); cmd == nil {
		t.Error("pingWalkModel.Init should schedule a walk tick")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// A walk tick advances the animation (and schedules the next).
	m, cmd := m.Update(walkTickMsg{})
	if cmd == nil {
		t.Error("a walk tick should schedule the next tick")
	}
	if strings.TrimSpace(m.View()) == "" {
		t.Error("pingWalkModel.View should render something after a tick")
	}
	// Any key quits.
	if _, qcmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}); qcmd == nil {
		t.Error("a key should quit the ping walk")
	}
	// The walkTick cmd itself produces a walkTickMsg.
	if walkTick()() == nil {
		t.Error("walkTick cmd should produce a message")
	}
}

// TestModelHelperAccessors covers the small model method helpers via a seeded model.
func TestModelHelperAccessors(t *testing.T) {
	m := seedFor(120, modeShare, false)
	if m.sectionName() != "SHARE" {
		t.Errorf("sectionName(share) = %q, want SHARE", m.sectionName())
	}
	tm := seedFor(120, modeBrowse, false)
	if tm.sectionName() != "TUNE IN" {
		t.Errorf("sectionName(browse) = %q, want TUNE IN", tm.sectionName())
	}
	// balDollars: no balance -> "-", with balance -> a dollar string.
	m.haveBal = false
	if m.balDollars() != "-" {
		t.Errorf("balDollars(no bal) = %q, want -", m.balDollars())
	}
	m.haveBal = true
	m.balance = 12.34
	if m.balDollars() == "-" {
		t.Error("balDollars(with bal) should render a dollar amount")
	}
	// These just must not panic on a seeded model.
	_ = m.atOnAirLimit()
	_ = m.cursorOnConnected()
}

// TestDriveUpdate drives the main model through a batch of real key + window messages in
// each section, exercising the Update/View switch without launching the program. It is a
// no-panic + non-empty-render smoke over the interactive surface.
func TestDriveUpdate(t *testing.T) {
	keys := []tea.Msg{
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, // help
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, // close help
		tea.KeyMsg{Type: tea.KeyTab},                       // section toggle
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyUp},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}}, // compact toggle
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, // SHARE section
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, // TUNE IN section
	}
	for _, md := range []mode{modeBrowse, modeShare} {
		m := seedFor(120, md, false)
		var model tea.Model = m
		for _, k := range keys {
			model, _ = model.Update(k)
			if strings.TrimSpace(model.View()) == "" {
				t.Fatalf("View went blank after %T in mode %v", k, md)
			}
		}
	}
}

// TestDriveMessages feeds the model each async message type it handles (the broker /
// flow / agent results), exercising those Update cases + the resulting renders.
func TestDriveMessages(t *testing.T) {
	msgs := []tea.Msg{
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tickMsg{},
		balanceMsg{balance: 12.5, loggedIn: true},
		payoutStatusMsg{},
		topupMsg("https://pay.example/checkout"),
		grantListMsg{},
		grantMsg{secret: "rog-grant_abc"},
		loginMsg("octocat"),
		logoutMsg{},
		flowErrMsg("a flow failed"),
		errMsg("an error"),
		chatErrMsg("chat turn failed"),
		topupMsg(""), // empty checkout url branch
	}
	var model tea.Model = seedFor(120, modeBrowse, false)
	for _, msg := range msgs {
		model, _ = model.Update(msg)
		if strings.TrimSpace(model.View()) == "" {
			t.Fatalf("View went blank after %T", msg)
		}
	}
}
