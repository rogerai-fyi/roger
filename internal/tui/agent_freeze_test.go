package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// agentSeed builds an AGENT-mode model tuned to a model and pointed at broker, ready to
// run a turn. It mirrors the fixtures the other agent tests use.
func agentSeed(t *testing.T, broker string) model {
	t.Helper()
	base := browseSeed(120)
	base.broker = broker
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	if am.agent == nil || am.agent.model == "" {
		t.Fatalf("enterAgent should build a runtime on a tuned model, got %+v", am.agent)
	}
	return am
}

// TestAgentCostMsgRearmsDrain is the REGRESSION for the 835s turn-freeze: a per-turn cost
// tick (X-RogerAI-Cost) must NOT stop the event drain. It drives the REAL Cmd chain (each
// Update's returned Cmd feeds the next message, exactly as the Bubble Tea runtime does) -
// the older end-to-end test missed the bug by re-invoking waitAgentEvent manually every
// loop, papering over the missing re-arm. With the bug, the chain goes nil right after the
// cost message and the turn never reaches done (busy spins forever).
func TestAgentCostMsgRearmsDrain(t *testing.T) {
	srv := chatBroker(t, "the answer is two") // chatBroker sends an X-RogerAI-Cost header
	am := agentSeed(t, srv.URL)
	am.agentBusy = true

	// Launch the turn goroutine (no message yet).
	if msg := am.startAgentTurn("how many go files")(); msg != nil {
		t.Fatalf("startAgentTurn launch cmd should return nil, got %#v", msg)
	}

	// Follow the Cmd chain: start with the drain; after each Update use the Cmd it returned.
	cmd := am.waitAgentEvent()
	sawCost, done := false, false
	for i := 0; i < 80 && !done; i++ {
		if cmd == nil {
			t.Fatalf("the Cmd chain went nil before the turn finished — a cost tick stopped the drain (the freeze bug). sawCost=%v", sawCost)
		}
		msg := cmd()
		switch msg.(type) {
		case agentCostMsg:
			sawCost = true
		case agentDoneMsg:
			done = true
		}
		var tm tea.Model
		tm, cmd = am.Update(msg)
		am = asModel(tm)
	}
	if !sawCost {
		t.Fatal("expected a cost tick from the X-RogerAI-Cost header")
	}
	if !done {
		t.Fatal("the turn never reached done — the drain stalled (freeze)")
	}
	if am.agentBusy {
		t.Error("agentBusy must clear once the turn is done")
	}
	if !strings.Contains(stripANSI(am.View()), "the answer is two") {
		t.Errorf("the final answer should land in the transcript:\n%s", stripANSI(am.View()))
	}
}

// TestAgentEscForceStop covers the two-press esc: 1st esc is a graceful cancel (stays busy,
// in AGENT, marks canceling); 2nd esc FORCE-stops (frees the prompt now even if the
// goroutine lags), staying in AGENT with a usable prompt - never trapped on "cancelling…".
func TestAgentEscForceStop(t *testing.T) {
	am := agentSeed(t, "")
	am.agentBusy = true
	cancels := 0
	am.agent.cancel = func() { cancels++ }

	// 1st esc: graceful — cancel fired, turn still busy, canceling marked, still in AGENT.
	m1, _ := am.onAgentKey(tea.KeyMsg{Type: tea.KeyEsc})
	a1 := asModel(m1)
	if !a1.agentBusy {
		t.Error("1st esc should keep the turn busy (graceful cancel, not a force-stop)")
	}
	if !a1.agentCanceling {
		t.Error("1st esc should mark the turn canceling")
	}
	if a1.mode != modeAgent {
		t.Errorf("1st esc should stay in AGENT, got %v", a1.mode)
	}

	// 2nd esc: force-stop — busy cleared, canceling cleared, still in AGENT, "turn stopped".
	m2, _ := a1.onAgentKey(tea.KeyMsg{Type: tea.KeyEsc})
	a2 := asModel(m2)
	if a2.agentBusy {
		t.Error("2nd esc should force the turn to stop (agentBusy cleared) — no 'cancelling…' trap")
	}
	if a2.agentCanceling {
		t.Error("2nd esc should clear canceling")
	}
	if a2.mode != modeAgent {
		t.Errorf("2nd esc force-stop should stay in AGENT (usable prompt), got %v", a2.mode)
	}
	if cancels < 2 {
		t.Errorf("esc should drive cancel on both presses, got %d", cancels)
	}
	if !strings.Contains(stripANSI(strings.Join(a2.agentLines, "\n")), "turn stopped") {
		t.Error("force-stop should note 'turn stopped' in the transcript")
	}
}

// TestAgentQueueWhileBusy: input stays typable while a turn runs, and enter QUEUES the
// next ask (Claude-style) instead of being dropped or starting a racing turn.
func TestAgentQueueWhileBusy(t *testing.T) {
	am := agentSeed(t, "")
	am.agentBusy = true
	am.agent.running.Store(true) // a turn goroutine is live

	// Typing while busy edits the input (it is NOT blocked).
	tm, _ := am.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello there")})
	am = asModel(tm)
	if am.agentIn.Value() != "hello there" {
		t.Fatalf("typing should work while a turn runs, got input %q", am.agentIn.Value())
	}

	// Enter while busy queues the prompt and clears the input.
	qm, _ := am.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	am = asModel(qm)
	if len(am.agentQueued) != 1 || am.agentQueued[0] != "hello there" {
		t.Fatalf("enter-while-busy should queue the prompt, got %v", am.agentQueued)
	}
	if am.agentIn.Value() != "" {
		t.Errorf("queuing should clear the input, got %q", am.agentIn.Value())
	}
	if !am.agentBusy {
		t.Error("queuing must not end the current turn")
	}
	// The queued ask is visible in the transcript.
	if !strings.Contains(stripANSI(strings.Join(am.agentLines, "\n")), "queued") {
		t.Error("a queued ask should be shown (⏳ queued …) in the transcript")
	}
}

// TestAgentQueueAutoSendOnDone: when the running turn finishes, the next queued prompt is
// auto-sent (the drained item starts a new turn). We discard the returned Cmd so no real
// goroutine launches - startAgentTurn already flips the state synchronously.
func TestAgentQueueAutoSendOnDone(t *testing.T) {
	am := agentSeed(t, "")
	am.agentBusy = true
	am.agent.running.Store(true)

	// Queue a prompt mid-turn.
	am.agentIn.SetValue("queued question")
	qm, _ := am.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	am = asModel(qm)
	if len(am.agentQueued) != 1 {
		t.Fatalf("expected one queued prompt, got %v", am.agentQueued)
	}

	// The turn's goroutine has exited (running cleared); the done arrives.
	am.agent.running.Store(false)
	dm, _ := am.Update(agentDoneMsg{}) // discard cmd: do not launch the real goroutine
	am = asModel(dm)

	if len(am.agentQueued) != 0 {
		t.Errorf("done should drain the queue, still have %v", am.agentQueued)
	}
	if !am.agentBusy {
		t.Error("done should auto-start the queued turn (agentBusy back on)")
	}
	if am.agentCanceling {
		t.Error("auto-started turn should not be in the canceling state")
	}
	if !strings.Contains(stripANSI(strings.Join(am.agentLines, "\n")), "queued question") {
		t.Errorf("the queued prompt should be echoed as a new turn:\n%s", stripANSI(strings.Join(am.agentLines, "\n")))
	}
}

// TestAgentSubmitWaitsForLingeringGoroutine: after a force-stop the previous turn's
// goroutine may still own the shared loop (rt.running). Submitting a new prompt then must
// QUEUE it (not start a racing turn) until that goroutine exits.
func TestAgentSubmitWaitsForLingeringGoroutine(t *testing.T) {
	am := agentSeed(t, "")
	am.agent.running.Store(true) // a force-stopped turn's goroutine is still unwinding

	nm, _ := am.submitAgentPrompt("do a thing")
	if nm.agentBusy {
		t.Error("submitting while the prior goroutine is alive must NOT start a racing turn")
	}
	if len(nm.agentQueued) != 1 || nm.agentQueued[0] != "do a thing" {
		t.Errorf("the prompt should be re-queued, got %v", nm.agentQueued)
	}
}

// TestStartQueuedPromptRoutesCommands: a dequeued slash-command runs inline (no turn); a
// dequeued chat prompt starts a turn. Covers both branches of startQueuedPrompt.
func TestStartQueuedPromptRoutesCommands(t *testing.T) {
	am := agentSeed(t, "")
	am.agentLines = []string{"old line"}

	// A slash command runs inline and starts NO turn (/clear wipes the transcript).
	cm, _ := am.startQueuedPrompt("/clear")
	if cm.agentBusy {
		t.Error("a queued slash command must not start a turn")
	}
	if strings.Contains(stripANSI(strings.Join(cm.agentLines, "\n")), "old line") {
		t.Errorf("queued /clear should reset the transcript, got %v", cm.agentLines)
	}

	// A plain prompt starts a turn.
	pm, _ := am.startQueuedPrompt("do a thing")
	if !pm.agentBusy {
		t.Error("a queued chat prompt should start a turn")
	}
}

// TestAgentWorkingLineReceivingVsStalled covers the hung-detection readout: a recent event
// reads as receiving/working with the per-call cap surfaced; a long silence reads as "may
// be stuck" with the esc out + cap.
func TestAgentWorkingLineReceivingVsStalled(t *testing.T) {
	m := browseSeed(120)

	// Streaming + recent event -> "receiving…", with the cap surfaced once slow.
	m.agentTurnState = poseStreaming
	recv := stripANSI(m.agentWorkingLine(5, 1))
	if !strings.Contains(recv, "receiving") {
		t.Errorf("recent streaming should read 'receiving…', got %q", recv)
	}
	if !strings.Contains(recv, "cap 300s") {
		t.Errorf("the working line should surface the per-call cap, got %q", recv)
	}

	// Thinking + recent event -> "working…" (not "receiving").
	m.agentTurnState = poseThinking
	work := stripANSI(m.agentWorkingLine(5, 1))
	if !strings.Contains(work, "working") {
		t.Errorf("recent thinking should read 'working…', got %q", work)
	}

	// Long silence (>= agentStallSec) -> honest "may be stuck", esc out, cap.
	stalled := stripANSI(m.agentWorkingLine(agentStallSec+10, agentStallSec+5))
	if !strings.Contains(stalled, "may be stuck") {
		t.Errorf("a long silence should warn it may be stuck, got %q", stalled)
	}
	if !strings.Contains(stalled, "esc to cancel") {
		t.Errorf("the stall line should offer esc as the out, got %q", stalled)
	}
	if !strings.Contains(stalled, "cap 300s") {
		t.Errorf("the stall line should surface the cap, got %q", stalled)
	}

	// A TOOL running is exempt from the stall warning even after a long silence (the tool is
	// local + self-bounded; flagging it was a false alarm). It says what's happening instead.
	m.agentTurnState = poseTool
	tool := stripANSI(m.agentWorkingLine(70, agentStallSec+5))
	if strings.Contains(tool, "may be stuck") {
		t.Errorf("a running tool must not be flagged 'may be stuck', got %q", tool)
	}
	if !strings.Contains(tool, "running the tool") {
		t.Errorf("the tool-running line should say so, got %q", tool)
	}
}

// TestCornerWaitingIdleNoBlinkFreeze (task 5): a frozen (idle, not-live) waiting corner
// must NEVER stick on the blink (closed eye '-') - the open eye is preferred. Live frames
// still blink (the desync liveness cue while animating).
func TestCornerWaitingIdleNoBlinkFreeze(t *testing.T) {
	withMotion(func() { // un-freeze quiet so the blink logic is exercised
		// Find a LIVE blink frame to prove the blink exists at all.
		blinkFrame := -1
		for f := 0; f < 300; f++ {
			if _, eye := cornerFrameFor(poseWaiting, f, true); eye == "-" {
				blinkFrame = f
				break
			}
		}
		if blinkFrame < 0 {
			t.Fatal("expected the live waiting corner to blink within 300 frames")
		}
		// That same frame, FROZEN (live=false), must not blink.
		if _, eye := cornerFrameFor(poseWaiting, blinkFrame, false); eye == "-" {
			t.Errorf("idle/frozen waiting corner stuck on the blink at frame %d; should show the open eye", blinkFrame)
		}
		// Across the whole range no frozen waiting frame may show the closed eye.
		for f := 0; f < 300; f++ {
			if _, eye := cornerFrameFor(poseWaiting, f, false); eye == "-" {
				t.Fatalf("frozen waiting corner blinked at frame %d (closed eye)", f)
			}
		}
	})
}
