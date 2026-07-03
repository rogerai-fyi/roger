package tui

// rc_confirm_bdd_test.go makes features/remote/rc_confirm.feature EXECUTABLE: a mutating tool's
// y/N confirm is fanned out to every surface (local TUI + attached remotes), the FIRST answer
// from any surface wins, and a confirm_done names who answered. Drives the REAL bubbletea model
// (rcSeedHost + a fakeBridge that records emitted frames), no mocks.

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type rcConfirmState struct {
	t    *testing.T
	m    model
	fb   *fakeBridge
	resp chan bool
	got  []bool // answers observed on the confirm resp channel (the loop goroutine's verdict)
}

func (s *rcConfirmState) reset(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	*s = rcConfirmState{t: t, m: m, fb: fb}
}

func (s *rcConfirmState) update(msg tea.Msg) {
	var tm tea.Model = s.m
	tm, _ = tm.Update(msg)
	s.m = asModel(tm)
	s.drain()
}
func (s *rcConfirmState) drain() {
	for {
		select {
		case v := <-s.resp:
			s.got = append(s.got, v)
		default:
			return
		}
	}
}
func (s *rcConfirmState) confirmDoneFrames() []protocol.RCFrame {
	var out []protocol.RCFrame
	for _, f := range s.fb.emitted {
		if f.Kind == protocol.RCKindConfirmDone {
			out = append(out, f)
		}
	}
	return out
}

// ── the propose + answer drivers ─────────────────────────────────────────────────────────

func (s *rcConfirmState) proposeMutatingTool() error {
	s.resp = make(chan bool, 1)
	c := agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "rm -rf tmp"}, resp: s.resp}
	s.update(agentConfirmMsg(c))
	return nil
}
func (s *rcConfirmState) remoteAnswers(approve bool) {
	s.update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: approve, Origin: "web (lramos85)"}))
}

// ── F1 ───────────────────────────────────────────────────────────────────────────────────

func (s *rcConfirmState) confirmReqOnAllSurfaces() error {
	if s.m.agentPendingConfirm == nil {
		return fmt.Errorf("no pending confirm on the local TUI")
	}
	sawReq := false
	for _, f := range s.fb.emitted {
		if f.Kind == protocol.RCKindConfirmReq {
			sawReq = true
		}
	}
	if !sawReq {
		return fmt.Errorf("no confirm_req frame was teed to the remote surface")
	}
	return nil
}
func (s *rcConfirmState) toolDoesNotRunYet() error {
	if len(s.got) != 0 {
		return fmt.Errorf("the tool resolved before the confirm was answered (%v)", s.got)
	}
	return nil
}

// ── F2 ───────────────────────────────────────────────────────────────────────────────────

func (s *rcConfirmState) remoteApprove() error { s.remoteAnswers(true); return nil }
func (s *rcConfirmState) remoteDeny() error    { s.remoteAnswers(false); return nil }
func (s *rcConfirmState) toolRuns() error {
	if len(s.got) != 1 || !s.got[0] {
		return fmt.Errorf("the tool did not run on a remote approve (resp=%v)", s.got)
	}
	return nil
}
func (s *rcConfirmState) toolSkippedDenied() error {
	if len(s.got) != 1 || s.got[0] {
		return fmt.Errorf("a remote deny should skip the tool with a false verdict (resp=%v)", s.got)
	}
	return nil
}
func (s *rcConfirmState) confirmDoneNamesRemote() error {
	dones := s.confirmDoneFrames()
	if len(dones) == 0 {
		return fmt.Errorf("no confirm_done frame was shown to the surfaces")
	}
	if dones[len(dones)-1].Origin != "web (lramos85)" {
		return fmt.Errorf("confirm_done origin = %q, want the remote surface", dones[len(dones)-1].Origin)
	}
	return nil
}

// ── F3 ───────────────────────────────────────────────────────────────────────────────────

func (s *rcConfirmState) localApproveFirst() error {
	s.update(kmRunes("y")) // the local host answers before any remote
	return nil
}
func (s *rcConfirmState) toolRunsOnce() error {
	if len(s.got) != 1 || !s.got[0] {
		return fmt.Errorf("expected exactly one run from the local approve, got %v", s.got)
	}
	return nil
}
func (s *rcConfirmState) laterRemoteIsNoOp() error {
	before := len(s.got)
	s.remoteAnswers(true) // a late remote answer for the already-resolved confirm
	if len(s.got) != before {
		return fmt.Errorf("a late remote answer re-resolved the confirm (resp grew to %v)", s.got)
	}
	if s.m.agentPendingConfirm != nil {
		return fmt.Errorf("the confirm should stay resolved after a late remote answer")
	}
	return nil
}

func TestRCConfirmBDD(t *testing.T) {
	st := &rcConfirmState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.Step(`^a running broker$`, func() error { return nil })
			sc.Step(`^a host with remote control enabled and one attached remote surface$`, func() error { return nil })
			// F1
			sc.Step(`^the agent proposes a mutating tool call$`, st.proposeMutatingTool)
			sc.Step(`^a confirm_req is delivered to the local TUI and the remote surface$`, st.confirmReqOnAllSurfaces)
			sc.Step(`^the tool does not run until the confirm is answered$`, st.toolDoesNotRunYet)
			// F2
			sc.Step(`^the agent is awaiting a tool confirm$`, st.proposeMutatingTool)
			sc.Step(`^the remote surface approves it$`, st.remoteApprove)
			sc.Step(`^the remote surface denies it$`, st.remoteDeny)
			sc.Step(`^the tool runs on the host$`, st.toolRuns)
			sc.Step(`^the tool is skipped with a denied result$`, st.toolSkippedDenied)
			sc.Step(`^a confirm_done naming the remote surface is shown everywhere$`, st.confirmDoneNamesRemote)
			// F3
			sc.Step(`^the local TUI approves it before the remote answers$`, st.localApproveFirst)
			sc.Step(`^the tool runs once$`, st.toolRunsOnce)
			sc.Step(`^a later remote answer for the same confirm is a no-op$`, st.laterRemoteIsNoOp)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/remote/rc_confirm.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("rc_confirm scenarios failed (see godog output above)")
	}
}
