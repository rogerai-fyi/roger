package tui

import "testing"

// A TURN'S DONE IS ACTED ON ONCE. The drain re-arms after a turn ends, and a done channel that
// is already closed reports again at once - so the same agentDoneMsg can reach Update twice.
// The second must change nothing: not the busy state, not the transcript (a second delegation
// receipt), not the status line.
//
// Until this test the guard ran only when a re-armed drain happened to observe an already
// closed done before the UI had processed the first report - one coverage run of ten, and
// none in the ten before it. Here the duplicate is simply sent, and the model's state after it
// is compared field for field with the state before it.
func TestASecondDoneForTheSameTurnChangesNothing(t *testing.T) {
	base := browseSeed(120)
	base.broker = brokerReplying(t, "the answer").URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	m := asModel(nm)
	if m.agent == nil {
		t.Fatal("entering AGENT on a tuned band should build a runtime")
	}

	done := make(chan struct{})
	m.agent.turnDone = done
	m.agentBusy = true
	nm, _ = m.Update(agentDoneMsg{turn: done})
	first := asModel(nm)
	if first.agentBusy {
		t.Fatal("the first done must end the turn")
	}
	if first.agentDoneHandled != done {
		t.Fatal("the first done must be recorded as handled")
	}

	// The duplicate arrives after the operator has started typing again: a handler that ran
	// twice would clear this, append a second receipt and rewrite the status.
	first.agentBusy = true
	lines, status := len(first.agentLines), first.status
	nm, cmd := first.Update(agentDoneMsg{turn: done})
	second := asModel(nm)
	if cmd != nil {
		t.Fatal("a duplicate done must schedule nothing")
	}
	if !second.agentBusy {
		t.Fatal("a duplicate done cleared the busy state of a turn it does not belong to")
	}
	if len(second.agentLines) != lines {
		t.Fatalf("a duplicate done wrote to the transcript: %d lines, want %d", len(second.agentLines), lines)
	}
	if second.status != status {
		t.Fatalf("a duplicate done rewrote the status line: %q", second.status)
	}
}
