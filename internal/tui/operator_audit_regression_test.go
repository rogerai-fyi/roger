package tui

// operator_audit_regression_test.go - permanent regressions for the two money-path bugs
// the claude-audit pre-push gate caught on the first Phase 2 push (2026-07-06):
//   (a) the spawn-failure early return skipped the SetBudget(0) restore, leaving the
//       interactive DJ session capped at the guest's $2 - surprise 402s later;
//   (b) the 450ms staging window (handoff staged, bridge not yet parked) let a remote
//       turn start a billed DJ turn that would then run under the suspended TUI and
//       corrupt the guest's freshly reset accumulator.
// Candidates for promotion into the approved .feature set at the next founder review.

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/operator"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// opRegressionSeed stages a handoff-capable AGENT model with a live holder and one
// detected guest, with the exec/scratch seams captured for the test.
func opRegressionSeed(t *testing.T) (model, *client.ProxyOptionsHolder, *[]*exec.Cmd) {
	t.Helper()
	saveExec, saveRoot, saveDelay := operatorExec, operatorScratchRoot, operatorStageDelay
	var execs []*exec.Cmd
	operatorExec = func(c *exec.Cmd, _ func(error) tea.Msg) tea.Cmd {
		execs = append(execs, c)
		return nil
	}
	operatorScratchRoot = t.TempDir()
	operatorStageDelay = time.Millisecond
	t.Cleanup(func() { operatorExec, operatorScratchRoot, operatorStageDelay = saveExec, saveRoot, saveDelay })

	m := asModel(agentReady(t))
	m.proxyHolder = client.NewProxyOptionsHolder(client.ProxyOptions{
		Broker: "http://127.0.0.1:1", User: "tester", Model: "qwen3-32b-fp8", SessionKey: client.NewSessionKey(),
	})
	m.endpoint = "http://127.0.0.1:1/v1"
	g, _ := func() (operator.Guest, bool) {
		for _, g := range operator.Registry() {
			if g.Name == "opencode" {
				return g, true
			}
		}
		return operator.Guest{}, false
	}()
	m.operatorDetections = []operator.Detection{{Guest: g, Path: "/fake/opencode", Version: g.KnownGood}}
	return m, m.proxyHolder, &execs
}

// TestSpawnFailureRestoresUncappedBudget: audit finding (a). A spawn failure must leave
// the interactive session UNCAPPED (Budget 0) like every other return path - never
// parked at the guest's $2 with the spend zeroed.
func TestSpawnFailureRestoresUncappedBudget(t *testing.T) {
	m, holder, _ := opRegressionSeed(t)
	var tm tea.Model
	tm, _ = m.runAgentCommand("/operator opencode")
	tm, _ = tm.Update(operatorExecMsg{})
	if got := holder.Get().Budget; got != client.DefaultSessionBudget {
		t.Fatalf("handoff must arm the $%v budget, got %v", client.DefaultSessionBudget, got)
	}
	// The exec never started: bubbletea delivers a non-ExitError to the callback.
	tm, _ = tm.Update(operatorDoneMsg{err: &exec.Error{Name: "opencode", Err: exec.ErrNotFound}})
	if got := asModel(tm).proxyHolder.Get().Budget; got != 0 {
		t.Fatalf("spawn failure left the DJ session capped at $%v (want uncapped 0) - later turns would 402", got)
	}
}

// TestStagingWindowRemoteTurnDropped: audit finding (b), host side. A remote turn landing
// AFTER the handoff is staged but BEFORE the bridge parks must be dropped with the
// "guest has the mic" status frame - never queued, never a DJ turn.
func TestStagingWindowRemoteTurnDropped(t *testing.T) {
	m, _, _ := opRegressionSeed(t)
	fb := newFakeBridge()
	m.rcBridge = fb
	var tm tea.Model
	tm, _ = m.runAgentCommand("/operator opencode") // staged; NOT yet execed/parked
	tm, _ = tm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "sneaky staging turn", Origin: "phone"}))
	got := asModel(tm)
	if got.agentBusy || len(got.agentQueued) != 0 {
		t.Fatalf("a staging-window remote turn started/queued a DJ turn (busy=%v queue=%v)", got.agentBusy, got.agentQueued)
	}
	if v := stripANSI(got.View()); strings.Contains(v, "sneaky staging turn") {
		t.Fatalf("the dropped turn reached the transcript")
	}
	sawStatus := false
	for _, f := range fb.emitted {
		if f.Kind == protocol.RCKindStatus && f.Operator == "opencode" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatalf("the sender must be told the guest has the mic (frames: %+v)", fb.emitted)
	}
}

// TestExecRecheckAbortsWhenDJPickedUp: audit finding (b), belt half. Even if a turn DID
// slip in during staging, onOperatorExec must re-check the DJ-idle preconditions and
// abort rather than exec over a live turn (billing it into the guest's accumulator).
func TestExecRecheckAbortsWhenDJPickedUp(t *testing.T) {
	m, holder, execs := opRegressionSeed(t)
	var tm tea.Model
	tm, _ = m.runAgentCommand("/operator opencode")
	mm := asModel(tm)
	mm.agentBusy = true // a turn slipped in during the staging beat
	tm, _ = mm.Update(operatorExecMsg{})
	got := asModel(tm)
	if len(*execs) != 0 {
		t.Fatalf("the exec was issued over a live DJ turn")
	}
	if got.operatorHandoff != nil {
		t.Fatalf("the aborted handoff must clear its staging state")
	}
	if b := holder.Get().Budget; b != 0 {
		t.Fatalf("an aborted handoff must not leave the $%v cap armed (budget=%v)", client.DefaultSessionBudget, b)
	}
}

// TestExecAbortsOutsideAgentMode: audit minor (2nd pass). A global key during the
// staging beat (ctrl+c quit-confirm, alt+m, a preset) can move the TUI off modeAgent;
// the staged operatorExecMsg must then ABORT, never exec the guest under another modal.
func TestExecAbortsOutsideAgentMode(t *testing.T) {
	m, holder, execs := opRegressionSeed(t)
	var tm tea.Model
	tm, _ = m.runAgentCommand("/operator opencode")
	mm := asModel(tm)
	mm.mode = modeBrowse // a global key pulled the TUI away mid-staging
	tm, _ = mm.Update(operatorExecMsg{})
	got := asModel(tm)
	if len(*execs) != 0 {
		t.Fatalf("the exec was issued outside AGENT mode")
	}
	if got.operatorHandoff != nil {
		t.Fatalf("the aborted handoff must clear its staging state")
	}
	if b := holder.Get().Budget; b != 0 {
		t.Fatalf("an aborted handoff must not leave the cap armed (budget=%v)", b)
	}
}
