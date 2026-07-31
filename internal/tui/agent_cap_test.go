package tui

import (
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v5/internal/harness"
)

// TestAgentWorkingLineCapPrompt covers the soft-cap choice (the founder's "ask the
// user if they want to continue to wait or skip"): a model call past PerCallCap flips
// the working line to the tab-waits / esc-stops / auto-stop readout, and grantMoreTime
// pushes the soft mark back so the prompt clears. It builds its OWN runtime and never
// touches the shared seed's, so it cannot leak call state into other tests.
func TestAgentWorkingLineCapPrompt(t *testing.T) {
	m := browseSeed(120)
	m.agentTurnState = poseThinking
	rt := &agentRuntime{}
	rt.callLimit = harness.PerCallCap
	m.agent = rt

	// A call within the cap: the normal working readout, no cap prompt.
	granted := 0
	rt.callMu.Lock()
	rt.callStart = time.Now().Add(-10 * time.Second)
	rt.callSoft = rt.callStart.Add(harness.PerCallCap)
	rt.callExtend = func(time.Duration) { granted++ }
	rt.callMu.Unlock()
	line := stripANSI(m.agentWorkingLine(10, 10))
	if strings.Contains(line, "slow call") {
		t.Errorf("a call within the cap must not show the cap prompt, got %q", line)
	}

	// Past the cap: the choice line, with tab / esc / the auto-stop countdown.
	rt.callMu.Lock()
	rt.callStart = time.Now().Add(-harness.PerCallCap - 5*time.Second)
	rt.callSoft = rt.callStart.Add(harness.PerCallCap)
	rt.callMu.Unlock()
	line = stripANSI(m.agentWorkingLine(310, 310))
	for _, want := range []string{"slow call", "tab", "esc", "auto-stop"} {
		if !strings.Contains(line, want) {
			t.Errorf("past-cap line should contain %q, got %q", want, line)
		}
	}

	// grantMoreTime: extends the underlying deadline once and clears the prompt.
	if got := rt.grantMoreTime(); got != harness.PerCallCap {
		t.Fatalf("grantMoreTime = %s, want %s", got, harness.PerCallCap)
	}
	if granted != 1 {
		t.Fatalf("grantMoreTime should call the extend func once, got %d", granted)
	}
	line = stripANSI(m.agentWorkingLine(310, 310))
	if strings.Contains(line, "slow call") {
		t.Errorf("after a grant the prompt should clear until the next soft mark, got %q", line)
	}
	// Not past the (new) cap: a second grant is refused.
	if got := rt.grantMoreTime(); got != 0 {
		t.Error("grantMoreTime must refuse when the call is not past the soft cap")
	}

	// Between calls: zero-value state never prompts.
	rt.callMu.Lock()
	rt.callStart, rt.callSoft, rt.callExtend = time.Time{}, time.Time{}, nil
	rt.callMu.Unlock()
	if in, _, past, _ := rt.callState(); in || past {
		t.Error("cleared call state must read as no live call")
	}
}

func TestAgentGrantToastUsesConfiguredTimeout(t *testing.T) {
	m := browseSeed(120)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()
	m.agentBusy = true
	m.agent.callLimit = 10 * time.Minute
	m.agent.callStart = time.Now().Add(-11 * time.Minute)
	m.agent.callSoft = m.agent.callStart.Add(m.agent.callLimit)
	m.agent.callExtend = func(time.Duration) {}

	out, _ := m.onAgentKey(keyMsg("tab"))
	m = asModel(out)
	if got := stripANSI(strings.Join(m.agentLines, "\n")); !strings.Contains(got, "600s more") {
		t.Fatalf("grant toast must name configured 600s extension, got:\n%s", got)
	}
}
