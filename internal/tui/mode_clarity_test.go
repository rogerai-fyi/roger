package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestModeHeadersDistinct: the AGENT (tool-calling) and TUNE-IN (basic chat) views share
// the same shape, so they MUST read as visibly distinct - the founder's "sometimes it's
// not obvious which mode I'm in". AGENT spells out "· tools"; TUNE-IN spells out "chat (no
// tools)"; and the accent bars use different colors (red vs mono).
func TestModeHeadersDistinct(t *testing.T) {
	// The distinctive HEADER phrases (the always-present preset bar carries both bare words
	// "AGENT" and "TUNE IN", so we key off the header-only "· tools" / "· chat" tags).

	// TUNE-IN (chat) header.
	chat := stripANSI(seedFor(120, modeChat, false).View())
	if !strings.Contains(chat, "TUNE-IN · chat") {
		t.Errorf("the chat view should be headed 'TUNE-IN · chat':\n%s", chat)
	}
	if !strings.Contains(chat, "no tools") {
		t.Errorf("the chat view should say it has no tools:\n%s", chat)
	}
	if strings.Contains(chat, "TOOLS:") {
		t.Errorf("the chat view must not show the AGENT TOOLS control line:\n%s", chat)
	}

	// AGENT header: the DIAL DECK (design overhaul §6) - the ◉ LOCK lamp + call sign +
	// "· AGENT" - plus the AGENT-only "TOOLS:" control line under the input. The old
	// "AGENT · tools" heading tag moved to that control line (increments 1/4).
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	am, _ := base.enterAgent()
	agent := stripANSI(asModel(am).View())
	if !strings.Contains(agent, "· AGENT") || !strings.Contains(agent, "TOOLS:") {
		t.Errorf("the AGENT view should show the dial deck '· AGENT' + the TOOLS control line:\n%s", agent)
	}
	if strings.Contains(agent, "TUNE-IN · chat") {
		t.Errorf("the AGENT view must not show the TUNE-IN · chat header:\n%s", agent)
	}

	// The AGENT deck leads with the green ◉ LOCK lamp; TUNE-IN uses a mono bar - a real
	// accent distinction (lamp token vs mono ink), independent of the colorless render.
	if lampStyle(roleSignal).GetForeground() == stDim.GetForeground() {
		t.Error("the AGENT green ◉ LOCK lamp must differ from the mono ink")
	}
}

// TestChatShiftTabEntersAgent: shift+tab in the tuned-in chat opens the model in the
// AGENT (tools) - the discoverable bridge the founder asked for (like tab peeks at BROWSE).
func TestChatShiftTabEntersAgent(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.chatIn.Focus()
	var tm tea.Model = m
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if asModel(tm).mode != modeAgent {
		t.Errorf("shift+tab in chat should enter AGENT, got mode %v", asModel(tm).mode)
	}
}

// TestChatFooterAdvertisesAgent: the chat footer teaches the shift+tab -> agent path so it
// is discoverable (the founder: "it's not obvious you can open a tuned-in model in /agent").
func TestChatFooterAdvertisesAgent(t *testing.T) {
	for _, compact := range []bool{false, true} {
		out := stripANSI(seedFor(120, modeChat, compact).View())
		if !strings.Contains(out, "agent") {
			t.Errorf("the chat footer (compact=%v) should advertise the agent switch:\n%s", compact, out)
		}
	}
}
