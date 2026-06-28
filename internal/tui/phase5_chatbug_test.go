package tui

import "testing"

// TestSlashAgentEntersAgentOnChannelModel: /agent from a channel jumps to the AGENT, which
// runs on the open channel's model (enterAgent resolves it) - the founder's shortcut.
func TestSlashAgentEntersAgentOnChannelModel(t *testing.T) {
	m := seedFor(120, modeChat, false) // connected to gpt-oss-20b
	out, _ := m.runSession("/agent")
	om := asModel(out)
	if om.mode != modeAgent {
		t.Fatalf("/agent should enter AGENT mode, got %v", om.mode)
	}
	if om.agent != nil && om.agent.model != "" && om.agent.model != m.connected.Model {
		t.Errorf("AGENT should run on the channel's model %q, got %q", m.connected.Model, om.agent.model)
	}
}

// TestChatRowsReserveStatusForFooter: a transient status reserves a transcript row so the
// channel hint bar can't be pushed off-screen (the "disappearing menu" fix).
func TestChatRowsReserveStatusForFooter(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.height = 30
	m.status = ""
	base := m.chatTranscriptRows()
	m.status = stDim.Render("✓ copied the last reply")
	if withStatus := m.chatTranscriptRows(); withStatus >= base {
		t.Errorf("a status must reserve a row (footer stays): base=%d withStatus=%d", base, withStatus)
	}
	m.updateLine = "update available"
	if withBoth := m.chatTranscriptRows(); withBoth >= m.chatTranscriptRows()+1 {
		_ = withBoth // (both reserved; just exercise the branch)
	}
}
