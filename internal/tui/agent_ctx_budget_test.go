package tui

import (
	"testing"

	"rogerai.fm/roger/v6/internal/harness"
)

// The agent's tool-output budget must track the model it is RUNNING ON, and must follow a
// /model switch. The founder's turn died on Apple's 8K `foundation` band after a ~10KB
// web_fetch; switching from a 128K band to an 8K one and keeping the 128K budget would
// reproduce it exactly. See internal/harness/ctxbudget_test.go for the incident.

// agentOnBands returns an agent-mode model carrying a roomy band and a tiny one.
func agentOnBands(t *testing.T) model {
	t.Helper()
	m := browseSeed(100)
	m.bands = []band{
		{model: "big-model", online: true, cheapest: &offer{Model: "big-model", Ctx: 128000}},
		{model: "foundation", online: true, cheapest: &offer{Model: "foundation", Ctx: 8192}},
	}
	m.agent = m.newAgentRuntime()
	return m
}

func TestAgentToolBudgetFollowsTheTunedModel(t *testing.T) {
	m := agentOnBands(t)

	m.pickAgentModel("big-model")
	big := m.agent.loop.MaxToolOutput
	if big != harness.ToolOutputBudget(128000) {
		t.Fatalf("budget on a 128K band = %d, want %d", big, harness.ToolOutputBudget(128000))
	}

	// Switching to the 8K band must SHRINK the budget - keeping the roomy one is the bug.
	m.pickAgentModel("foundation")
	small := m.agent.loop.MaxToolOutput
	if small != harness.ToolOutputBudget(8192) {
		t.Fatalf("budget after switching to an 8K band = %d, want %d", small, harness.ToolOutputBudget(8192))
	}
	if small >= big {
		t.Errorf("switching to a smaller band did not shrink the budget (%d -> %d)", big, small)
	}
	// The concrete regression: the founder's 10103-byte fetch must no longer fit.
	if small >= 10103 {
		t.Errorf("an 8K band would still accept the 10103-byte result that overflowed it (budget %d)", small)
	}
}

// A model that is not on the current dial reports no context window, and an unknown window
// must fall back to the historical flat cap rather than a guess.
func TestAgentToolBudgetUnknownModelFallsBack(t *testing.T) {
	m := agentOnBands(t)
	m.pickAgentModel("not-on-the-dial")
	if got := m.agent.loop.MaxToolOutput; got != harness.ToolOutputBudget(0) {
		t.Errorf("budget for an unknown model = %d, want the flat fallback %d", got, harness.ToolOutputBudget(0))
	}
}
