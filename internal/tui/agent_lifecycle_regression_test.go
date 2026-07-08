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
// AGENT redesign residuals (2026-07-08):
//   - the handoff pre-bind gate read the BAND ctx (max across stations) but bound a SPECIFIC
//     free station: a free 8k station beside a paid 32k sibling was bound then refused
//     post-bind (the mixed-band twin of #6). Pinned as a godog scenario in auto_tune.feature.
//   - a re-scan to ZERO guests while THE DESK was focused left an invisible focused desk that
//     swallowed arrows/enter (TestDeskFocusClearsWhenGuestsVanish).
//   - re-entry clobbered refreshAgentModel's "no model tuned in" status with "AGENT ready"
//     (TestReentryPreservesNoModelStatus).
//   - the "finding a free band…" beat prefix differed between the two sites that emit it
//     (TestFindingBandBeatPrefixConsistent).
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

// TestParkedTurnRequeueEchoesOnce: audit finding (2026-07-07). startParkedTurn requeued
// the FIRST parked prompt into agentQueued WITHOUT echoed=true when a turn was still running
// at drain time; the busy-queue drain then re-echoed it (the same double-echo class fixed for
// the rest[] entries). The requeued prompt must carry echoed=true, and drain exactly once.
func TestParkedTurnRequeueEchoesOnce(t *testing.T) {
	m := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})

	// One ask parks while no band is tuned (echoed "▸ …" once at park time).
	tm, _ := m.submitAgentPrompt(queuedPrompt{text: "only ask"})
	m = asModel(tm)
	if len(m.agentPending) != 1 {
		t.Fatalf("the ask should park while no band is tuned, got %d pending", len(m.agentPending))
	}

	// A turn is running at auto-tune time, so drainPendingPrompts -> startParkedTurn requeues
	// the parked prompt into agentQueued instead of starting it now.
	m.agent.running.Store(true)
	_ = m.runAutoTune()
	if len(m.agentQueued) != 1 {
		t.Fatalf("the parked ask should requeue while a turn runs, got %d queued", len(m.agentQueued))
	}
	if !m.agentQueued[0].echoed {
		t.Fatalf("the requeued parked prompt must carry echoed=true so the busy-queue drain does not re-echo it")
	}

	// The running turn finishes and the busy queue drains: the ask must NOT be re-echoed.
	m.agent.running.Store(false)
	dm, _ := m.Update(agentDoneMsg{}) // discard cmd: no real relay
	m = asModel(dm)
	body := stripANSI(strings.Join(m.agentLines, "\n"))
	if n := strings.Count(body, "only ask"); n != 1 {
		t.Fatalf("\"only ask\" echoed %d times, want exactly 1 (the requeue double-echo bug):\n%s", n, body)
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

// TestDeskFocusClearsWhenGuestsVanish: finding (2026-07-08). A re-scan that shrinks
// deskGuests to ZERO while THE DESK is focused left deskFocused=true over an EMPTY roster
// (deskRosterBlock renders nothing at zero guests) - an INVISIBLE focused desk that
// swallowed arrows/enter. onOperatorDetected must clear deskFocused (and reset the cursor)
// when the new guest set is empty, so keys reach the ask box again.
func TestDeskFocusClearsWhenGuestsVanish(t *testing.T) {
	oc, err := registryGuest("opencode")
	if err != nil {
		t.Fatal(err)
	}
	m := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	m.operatorDetections = []operator.Detection{{Guest: oc, Path: "/fake/opencode", Version: oc.KnownGood}}
	m.deskFocused = true
	m.deskCursor = 1 // parked on the guest row

	// A re-scan lands ZERO guests (the guest quit / dropped off PATH).
	nm, _ := m.onOperatorDetected(operatorDetectedMsg{ds: nil})
	got := asModel(nm)
	if got.deskFocused {
		t.Fatalf("deskFocused must clear when the guest set empties - an invisible focused desk swallows arrows/enter")
	}
	if got.deskCursor != 0 {
		t.Fatalf("deskCursor should reset to 0 when the desk defocuses, got %d", got.deskCursor)
	}
}

// TestReentryPreservesNoModelStatus: finding (2026-07-08, cosmetic). On RE-ENTRY with no
// model resolving, refreshAgentModel sets the specific "no model tuned in" status; enterAgent
// then unconditionally overwrote it with the generic "AGENT ready", making the status wrong.
// The specific no-model status must survive re-entry.
func TestReentryPreservesNoModelStatus(t *testing.T) {
	m := freshDeskAgent(t, nil)
	// A session that HAD a model but no longer resolves one (the band dropped, nothing
	// sticky): agent.model set, but connected/lastConnected nil so resolveAgentModel()=="".
	m.agent.model = "gpt-oss-20b"
	m.connected = nil
	m.lastConnected = nil
	m.autoTuning = false

	nm, _ := m.enterAgent()
	status := stripANSI(asModel(nm).status)
	if !strings.Contains(status, "no model tuned in") {
		t.Fatalf("re-entry must preserve the specific no-model status refreshAgentModel set, got %q", status)
	}
	if strings.Contains(status, "AGENT ready") {
		t.Fatalf("re-entry clobbered the no-model status with the generic AGENT ready: %q", status)
	}
}

// TestFindingBandBeatPrefixConsistent: finding (2026-07-08, cosmetic). The "finding a free
// band…" beat used a "· " prefix in enterAgent but glyphOnAir in submitAgentPrompt. Pin ONE
// prefix at both sites so the transcript reads consistently.
func TestFindingBandBeatPrefixConsistent(t *testing.T) {
	beatLine := func(lines []string) string {
		for _, ln := range lines {
			if strings.Contains(stripANSI(ln), "finding a free band") {
				return ln
			}
		}
		return ""
	}
	// enterAgent's fresh-landing beat.
	m1 := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	enterBeat := beatLine(m1.agentLines)
	// submitAgentPrompt's park-path beat (no model tuned -> park + auto-tune beat). Clear the
	// transcript + re-arm so the ONLY beat present is the one submitAgentPrompt emits (else
	// beatLine would find the enter beat freshDeskAgent already appended).
	m2 := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	m2.agentLines = nil
	m2.autoTuning = false
	tm, _ := m2.submitAgentPrompt(queuedPrompt{text: "hello"})
	submitBeat := beatLine(asModel(tm).agentLines)

	if enterBeat == "" || submitBeat == "" {
		t.Fatalf("both sites must emit the finding-a-band beat: enter=%q submit=%q", enterBeat, submitBeat)
	}
	if enterBeat != submitBeat {
		t.Fatalf("the finding-a-band beat prefix differs between enterAgent and submitAgentPrompt:\n enter=%q\nsubmit=%q",
			stripANSI(enterBeat), stripANSI(submitBeat))
	}
}
