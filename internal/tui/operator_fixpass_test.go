package tui

// operator_fixpass_test.go - permanent unit regressions for the Guest Operators Phase 2
// iteration-1 validation findings (2026-07 fix pass), alongside the scenarios added to
// features/operator/{rc_interlock,handoff_lifecycle,operator_command}.feature:
//   #1 a remote-queued "/..." must NEVER slash-dispatch at drain (origin-tagged queue);
//   #2 bracketed paste must be re-armed in the return cmd set (the defensive reset seq
//      disables it AFTER bubbletea's RestoreTerminal re-enabled it);
//   #3 the channel (Connected) is re-checked at exec time - a band drop during the
//      450ms staging beat aborts instead of launching the guest into 502/503s;
//   #4 every exec-time abort emits the DJ-back status frame so a remote surface told
//      "guest has the mic" during staging is never stranded.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestStartQueuedPromptOriginDispatch is the finding-#1 table: only a LOCAL slash entry
// dispatches through runAgentCommand at drain; a REMOTE entry is always a chat turn -
// matching the idle path - so no remote text can exec a guest (or /clear the host)
// through the busy queue.
func TestStartQueuedPromptOriginDispatch(t *testing.T) {
	cases := []struct {
		name     string
		q        queuedPrompt
		wantTurn bool   // a chat turn starts (agentBusy flips on)
		wantMark string // a transcript marker proving which path ran
	}{
		{"local slash dispatches inline", queuedPrompt{text: "/clear"}, false, "session cleared"},
		{"remote /operator is a chat turn, never a handoff", queuedPrompt{text: "/operator opencode", remote: true}, true, "▸ /operator opencode"},
		{"remote /clear is a chat turn too (asymmetry removal intended)", queuedPrompt{text: "/clear", remote: true}, true, "▸ /clear"},
		{"local chat text starts a turn", queuedPrompt{text: "hello there"}, true, "▸ hello there"},
		{"remote chat text starts a turn", queuedPrompt{text: "hello there", remote: true}, true, "▸ hello there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := agentSeed(t, "")
			nm, _ := m.startQueuedPrompt(tc.q) // returned Cmd deliberately not run: no real turn goroutine
			if nm.agentBusy != tc.wantTurn {
				t.Fatalf("agentBusy = %v, want %v", nm.agentBusy, tc.wantTurn)
			}
			if got := stripANSI(strings.Join(nm.agentLines, "\n")); !strings.Contains(got, tc.wantMark) {
				t.Fatalf("transcript lacks %q:\n%s", tc.wantMark, got)
			}
			if nm.operatorHandoff != nil {
				t.Fatal("a drained queue entry staged a guest handoff")
			}
		})
	}
}

// TestOperatorReturnReenablesBracketedPaste pins finding #2: the onOperatorDone cmd set
// includes tea.EnableBracketedPaste UNCONDITIONALLY (the radio always runs with paste
// on), for both mouse settings.
func TestOperatorReturnReenablesBracketedPaste(t *testing.T) {
	// Sandbox HOME: executing the return cmd set runs fetchBalance, which signs with the
	// local user key (LoadOrCreateUserKey) - never touch (or slowly create under) the
	// developer's real config.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTerm := operatorTermOut
	operatorTermOut = &bytes.Buffer{}
	t.Cleanup(func() { operatorTermOut = saveTerm })
	// A live stub broker: executing the return cmd set runs the fetchBalance leaf for
	// real, and a dead address can hang for seconds in sandboxed environments.
	balSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(balSrv.Close)
	for _, mouseOff := range []bool{false, true} {
		t.Run(fmt.Sprintf("mouseOff=%v", mouseOff), func(t *testing.T) {
			m, _, _ := opRegressionSeed(t)
			m.mouseOff = mouseOff
			m.broker = balSrv.URL
			var tm tea.Model
			tm, _ = m.runAgentCommand("/operator opencode")
			tm, _ = tm.Update(keyMsg("y")) // accept the Phase 3 pre-launch plate -> staged
			tm, _ = tm.Update(operatorExecMsg{})
			_, cmd := tm.Update(operatorDoneMsg{})
			found := false
			for _, msg := range collectCmdMsgs(cmd) {
				if strings.Contains(fmt.Sprintf("%T", msg), "enableBracketedPaste") {
					found = true
				}
			}
			if !found {
				t.Fatal("the return cmd set does not re-enable bracketed paste")
			}
		})
	}
}

// TestExecRecheckAbortsOnBandDrop pins finding #3: a Disconnect() during the staging
// beat means NO exec at exec time - the abort is honest and the desk stays usable.
func TestExecRecheckAbortsOnBandDrop(t *testing.T) {
	m, holder, execs := opRegressionSeed(t)
	var tm tea.Model
	tm, _ = m.runAgentCommand("/operator opencode")
	tm, _ = tm.Update(keyMsg("y")) // accept the Phase 3 pre-launch plate -> staged
	holder.Disconnect()            // the band drops inside the 450ms staging window
	tm, _ = tm.Update(operatorExecMsg{})
	got := asModel(tm)
	if len(*execs) != 0 {
		t.Fatalf("the exec was issued into a dropped band: %v", (*execs)[0].Args)
	}
	if got.operatorHandoff != nil {
		t.Fatal("the aborted handoff is still staged")
	}
	if v := stripANSI(got.View()); !strings.Contains(v, "the channel dropped while patching") {
		t.Fatalf("no honest abort note:\n%s", v)
	}
	if got.mode != modeAgent || got.agentBusy {
		t.Fatal("the desk is not usable after the abort")
	}
}

// djBackEmitted reports whether the fake bridge saw the corrective DJ-back status frame.
func djBackEmitted(fb *fakeBridge) bool {
	for _, f := range fb.emitted {
		if f.Kind == protocol.RCKindStatus && strings.Contains(f.Text, "the DJ is back at the desk") {
			return true
		}
	}
	return false
}

// TestExecTimeAbortEmitsDJBackFrame pins finding #4 across both exec-time abort causes:
// the staging guard may already have told a remote surface "guest has the mic", so every
// abort must correct the record with the DJ-back status frame.
func TestExecTimeAbortEmitsDJBackFrame(t *testing.T) {
	t.Run("DJ picked up a turn during staging", func(t *testing.T) {
		m, _, execs := opRegressionSeed(t)
		fb := newFakeBridge()
		m.rcBridge = fb
		var tm tea.Model
		tm, _ = m.runAgentCommand("/operator opencode")
		tm, _ = tm.Update(keyMsg("y")) // accept the Phase 3 pre-launch plate -> staged
		// The staging guard answers a remote turn with the guest-has-the-mic frame.
		tm, _ = tm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "hi", Origin: "phone"}))
		mm := asModel(tm)
		mm.agentBusy = true // a DJ turn slips in during the staging beat
		tm, _ = mm.Update(operatorExecMsg{})
		if got := asModel(tm); got.operatorHandoff != nil || len(*execs) != 0 {
			t.Fatal("the exec-time re-check did not abort")
		}
		if !djBackEmitted(fb) {
			t.Fatalf("no corrective DJ-back frame after the abort (frames: %+v)", fb.emitted)
		}
	})
	t.Run("band dropped during staging", func(t *testing.T) {
		m, holder, execs := opRegressionSeed(t)
		fb := newFakeBridge()
		m.rcBridge = fb
		var tm tea.Model
		tm, _ = m.runAgentCommand("/operator opencode")
		tm, _ = tm.Update(keyMsg("y")) // accept the Phase 3 pre-launch plate -> staged
		holder.Disconnect()
		tm, _ = tm.Update(operatorExecMsg{})
		if len(*execs) != 0 {
			t.Fatal("the band-drop abort still execed")
		}
		if !djBackEmitted(fb) {
			t.Fatalf("the band-drop abort emitted no DJ-back frame (frames: %+v)", fb.emitted)
		}
	})
}
