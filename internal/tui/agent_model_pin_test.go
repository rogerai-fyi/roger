package tui

import (
	"strings"
	"testing"
)

// TestModelPickSurvivesTurnsOnOpenChannel is the founder's exact bug: tuned to one
// band, /model picks another, and the NEXT ask must run on the pick - not snap back
// to the open channel. Only tuning a DIFFERENT channel re-points the agent.
func TestModelPickSurvivesTurnsOnOpenChannel(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "qwen-node", Model: "Qwen3.6-27B", Online: true}
	nm, _ := base.enterAgent()
	m := asModel(nm)
	if m.agent.model != "Qwen3.6-27B" {
		t.Fatalf("agent should start on the open channel, got %q", m.agent.model)
	}

	// Pick deepseek while the Qwen channel is still open.
	m.pickAgentModel("deepseek-v4-flash")
	if m.agent.model != "deepseek-v4-flash" || !m.agentPicked {
		t.Fatalf("pick did not take: model=%q picked=%v", m.agent.model, m.agentPicked)
	}

	// The next turn re-resolves (refreshAgentModel) - the pick must hold.
	m.refreshAgentModel()
	if m.agent.model != "deepseek-v4-flash" {
		t.Fatalf("refresh snapped the pick back to %q (the founder's bug)", m.agent.model)
	}
	joined := stripANSI(strings.Join(m.agentLines, "\n"))
	if strings.Contains(joined, "now runs on Qwen3.6-27B") {
		t.Errorf("no snap-back note should print:\n%s", joined)
	}

	// Disconnecting does not lose the pick either.
	m.connected = nil
	m.refreshAgentModel()
	if m.agent.model != "deepseek-v4-flash" {
		t.Fatalf("pick lost on disconnect, model=%q", m.agent.model)
	}

	// Tuning a DIFFERENT channel afterwards IS a deliberate re-point.
	m.connected = &offer{NodeID: "grok-node", Model: "grok-4.5", Online: true}
	m.refreshAgentModel()
	if m.agent.model != "grok-4.5" || m.agentPicked {
		t.Fatalf("a fresh channel should re-point and clear the pin: model=%q picked=%v", m.agent.model, m.agentPicked)
	}
}
