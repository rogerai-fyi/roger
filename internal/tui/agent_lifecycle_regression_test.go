package tui

// agent_lifecycle_regression_test.go - permanent regressions for the auto-tune / pending
// lifecycle leaks the AGENT [0] desk-entry audit caught (2026-07-07):
//
//   #2 (minor) runAutoTune had no mode guard: an auto-tune resolving AFTER the user left
//      AGENT still bound a channel / fired a parked turn outside AGENT.
//   #3 (minor) the 2nd+ parked prompt was echoed TWICE - once at park time, again when
//      drainPendingPrompts requeued it and submitAgentPrompt re-echoed at drain.
//   #5 (minor) deskCursor was never clamped when a re-scan SHRANK deskGuests while the
//      desk was focused, so the carat/marquee vanished until the user pressed up.
//   #6 (minor) the handoff auto-tune bound a KNOWN-small free band and only THEN refused
//      at the §6 gate, leaving the user silently tuned to a band too small for a guest.
//
// Candidates for promotion into the approved .feature set at the next founder review
// (findings #1, #2, #4 are pinned as godog scenarios in features/operator/*.feature).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/operator"
)

// freshDeskAgent enters [0] AGENT off a market of `offers`, logged in, with NOTHING tuned
// in (the genuinely-fresh landing: no proxy holder, agent.model == "").
func freshDeskAgent(t *testing.T, offers []offer) model {
	t.Helper()
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm, _ = tm.Update(offersMsg(offers))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 10})
	tm, _ = tm.Update(keyMsg("0"))
	m := asModel(tm)
	if m.mode != modeAgent {
		t.Fatalf("[0] should enter AGENT, got mode %d", m.mode)
	}
	if m.agent == nil || m.agent.model != "" {
		t.Fatalf("a fresh landing must have a runtime with no model tuned in, got %+v", m.agent)
	}
	return m
}

func freeOffer(model string, ctx int) offer {
	return offer{NodeID: "n-" + model, Model: model, Online: true, FreeNow: true, Signal: 50, Ctx: ctx}
}

// TestParkedPromptEchoOnceEach: finding #3. Two prompts parked before the auto-tune must
// each be echoed EXACTLY once - the first drains into a turn (echoed at park, not again),
// the second requeues (echoed at park, and must NOT be re-echoed when the queue drains).
func TestParkedPromptEchoOnceEach(t *testing.T) {
	m := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})

	// Two asks submitted while no band is tuned -> both park, each echoed "▸ …" once.
	var tm tea.Model
	tm, _ = m.submitAgentPrompt(queuedPrompt{text: "first ask"})
	tm, _ = asModel(tm).submitAgentPrompt(queuedPrompt{text: "second ask"})
	m = asModel(tm)
	if len(m.agentPending) != 2 {
		t.Fatalf("both asks should park while no band is tuned, got %d pending", len(m.agentPending))
	}

	// The auto-tune lands the free band: first drains into a turn (its goroutine cmd is
	// discarded so running stays false), second requeues into agentQueued.
	_ = m.runAutoTune()
	if len(m.agentQueued) != 1 {
		t.Fatalf("the second parked ask should requeue, got %d queued", len(m.agentQueued))
	}
	// The previous turn's goroutine never launched (cmd discarded); the done drains the
	// queue and submits the second ask.
	m.agent.running.Store(false)
	dm, _ := m.Update(agentDoneMsg{}) // discard cmd: no real relay
	m = asModel(dm)

	body := stripANSI(strings.Join(m.agentLines, "\n"))
	if n := strings.Count(body, "first ask"); n != 1 {
		t.Fatalf("\"first ask\" echoed %d times, want exactly 1:\n%s", n, body)
	}
	if n := strings.Count(body, "second ask"); n != 1 {
		t.Fatalf("\"second ask\" echoed %d times, want exactly 1 (the double-echo bug):\n%s", n, body)
	}
}

// TestRunAutoTuneBailsOutsideAgentMode: finding #2 (the mode guard). An auto-tune that
// resolves AFTER the user has left AGENT must be a no-op: never bind a channel, never fire
// a parked turn, never stomp the browse status.
func TestRunAutoTuneBailsOutsideAgentMode(t *testing.T) {
	m := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	// A prompt parks and arms the auto-tune.
	tm, _ := m.submitAgentPrompt(queuedPrompt{text: "leftover"})
	m = asModel(tm)
	if !m.autoTuning {
		t.Fatalf("submitting with no band should arm the auto-tune")
	}
	// The user left AGENT (esc to BROWSE) while the cold fetch was in flight.
	m.mode = modeBrowse
	cmd := m.runAutoTune()
	if cmd != nil {
		t.Fatalf("runAutoTune outside AGENT must be a no-op, got a cmd")
	}
	if m.connected != nil {
		t.Fatalf("runAutoTune outside AGENT must not bind a channel")
	}
	if m.autoTuning {
		t.Fatalf("runAutoTune outside AGENT must disarm the auto-tune")
	}
	if len(m.agentPending) != 0 {
		t.Fatalf("runAutoTune outside AGENT must drop the parked prompts, got %d", len(m.agentPending))
	}
}

// TestDeskCursorClampsOnGuestShrink: finding #5. A re-scan that SHRINKS the guest list
// while THE DESK is focused must clamp deskCursor into range - not strand it past the last
// row until the user presses up.
func TestDeskCursorClampsOnGuestShrink(t *testing.T) {
	oc, err := registryGuest("opencode")
	if err != nil {
		t.Fatal(err)
	}
	hz, err := registryGuest("hermes")
	if err != nil {
		t.Fatal(err)
	}
	m := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	m.operatorDetections = []operator.Detection{
		{Guest: oc, Path: "/fake/opencode", Version: oc.KnownGood},
		{Guest: hz, Path: "/fake/hermes", Version: hz.KnownGood},
	}
	m.deskFocused = true
	m.deskCursor = 2 // parked on the 2nd guest (DJ=0, opencode=1, hermes=2)

	// A re-scan drops back to a SINGLE guest: rows are now DJ=0, opencode=1 (max index 1).
	nm, _ := m.onOperatorDetected(operatorDetectedMsg{ds: []operator.Detection{
		{Guest: oc, Path: "/fake/opencode", Version: oc.KnownGood},
	}})
	got := asModel(nm)
	if max := got.deskRowCount() - 1; got.deskCursor > max || got.deskCursor < 0 {
		t.Fatalf("deskCursor %d out of range after a shrink (rows=%d, want 0..%d)", got.deskCursor, got.deskRowCount(), max)
	}
	if got.deskCursor != 1 {
		t.Fatalf("deskCursor should clamp to the last row (1), got %d", got.deskCursor)
	}
}

// TestHandoffRefusesKnownSmallFreeBandWithoutBinding: finding #6. When the ONLY free band
// is a KNOWN-small window, the handoff auto-tune must land on the honest refusal WITHOUT
// binding - never leave the user silently tuned to a band too small for a guest.
func TestHandoffRefusesKnownSmallFreeBandWithoutBinding(t *testing.T) {
	oc, err := registryGuest("opencode")
	if err != nil {
		t.Fatal(err)
	}
	// The only free band on air has an 8k window - known-small (under the 16k floor).
	m := freshDeskAgent(t, []offer{freeOffer("tiny-free", 8192)})
	m.operatorDetections = []operator.Detection{{Guest: oc, Path: "/fake/opencode", Version: oc.KnownGood}}
	d := operator.Detection{Guest: oc, Path: "/fake/opencode", Version: oc.KnownGood}

	nm, _ := m.startOperatorHandoff(d, false)
	got := asModel(nm)
	if got.connected != nil {
		t.Fatalf("the handoff must NOT bind a known-small free band, but connected to %q", got.connected.Model)
	}
	if got.proxyHolder != nil && got.proxyHolder.Connected() {
		t.Fatalf("the handoff must NOT open a channel on a known-small free band")
	}
	if got.operatorPlate != nil {
		t.Fatalf("no pre-launch plate should open over a refused band")
	}
	body := stripANSI(strings.Join(got.agentLines, "\n"))
	if !strings.Contains(body, "16k") && !strings.Contains(strings.ToLower(body), "too small") {
		t.Fatalf("the transcript must carry an honest 'no agent-ready band' refusal:\n%s", body)
	}
}
