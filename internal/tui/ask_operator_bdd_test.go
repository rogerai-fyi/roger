package tui

// ask_operator_bdd_test.go - the godog harness for features/agent/ask_operator.feature.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/protocol"
)

type askBDD struct {
	t       *testing.T
	m       model
	rt      *agentRuntime
	answer  chan string // what the asker closure returned
	askErr  chan error
	pending *agentAsk
	bridge  *fakeBridge
	askID   string
	firstID string
}

func (s *askBDD) enter() {
	srv := chatBroker(s.t, "answer")
	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	s.m = asModel(nm)
	s.rt = s.m.agent
	s.answer = make(chan string, 1)
	s.askErr = make(chan error, 1)
}

// ask drives the REAL ask_operator tool out of the live toolset, on a goroutine, the way a
// turn would. Going through the tool rather than an exported shortcut keeps the test on the
// production path - and avoids adding API to the Loop that only a test would ever call.
func (s *askBDD) askTool() harness.Tool {
	for _, t := range s.rt.loop.Tools() {
		if t.Name == "ask_operator" {
			return t
		}
	}
	s.t.Fatal("ask_operator is not in the toolset")
	return harness.Tool{}
}

func (s *askBDD) ask(ctx context.Context, q string, opts []string) {
	tool := s.askTool()
	args := map[string]any{"question": q}
	if len(opts) > 0 {
		var o []any
		for _, x := range opts {
			o = append(o, x)
		}
		args["options"] = o
	}
	go func() {
		a, err := tool.Run(ctx, ".", args)
		s.answer <- a
		s.askErr <- err
	}()
}

// pump runs the drain once and delivers whatever it produced.
func (s *askBDD) pump() tea.Msg {
	got := make(chan tea.Msg, 1)
	go func() { got <- s.m.waitAgentEvent()() }()
	select {
	case msg := <-got:
		out, _ := s.m.Update(msg)
		s.m = asModel(out)
		return msg
	case <-time.After(5 * time.Second):
		s.t.Fatal("the drain never produced anything")
		return nil
	}
}

func (s *askBDD) enteredAgent() error { s.enter(); return nil }

func (s *askBDD) agentAsksMidTurn() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("go")()
	s.ask(context.Background(), "which shall it be?", nil)
	s.pump()
	return nil
}

func (s *askBDD) questionShown() error {
	if s.m.agentPendingAsk == nil {
		return fmt.Errorf("the question never became a pending ask on the model")
	}
	// Check the question THIS scenario asked, not a remembered one: hardcoding a string
	// here made the step pass for the wrong reason in every scenario that asked something
	// else, and fail in the ones that mattered.
	if q := s.m.agentPendingAsk.question; !strings.Contains(stripANSI(s.m.View()), q) {
		return fmt.Errorf("the pending question %q is not on screen", q)
	}
	return nil
}

func (s *askBDD) answerComesBack() error {
	s.m.agentIn.SetValue("the second one")
	out, cmd := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("answering must re-arm the drain")
	}
	select {
	case got := <-s.answer:
		if got != "the second one" {
			return fmt.Errorf("the agent received %q", got)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the answer never reached the agent")
	}
	return nil
}

func (s *askBDD) turnContinues() error {
	if s.m.agentPendingAsk != nil {
		return fmt.Errorf("the question should be cleared once answered")
	}
	return nil
}

func (s *askBDD) asksWithThreeOptions() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("go")()
	s.ask(context.Background(), "pick one", []string{"alpha", "beta", "gamma"})
	s.pump()
	return nil
}

func (s *askBDD) allThreeShown() error {
	v := stripANSI(s.m.View())
	for _, o := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(v, o) {
			return fmt.Errorf("option %q is not on screen", o)
		}
	}
	return nil
}

func (s *askBDD) pickingReturnsIt() error {
	out, _ := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	s.m = asModel(out)
	select {
	case got := <-s.answer:
		if got != "beta" {
			return fmt.Errorf("picking option 2 returned %q, want beta", got)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("picking an option delivered nothing")
	}
	return nil
}

func (s *askBDD) asksOpenQuestion() error { return s.agentAsksMidTurn() }

func (s *askBDD) iTypeAnAnswer() error {
	s.m.agentIn.SetValue("a sentence I typed myself")
	out, _ := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	return nil
}

func (s *askBDD) answerVerbatim() error {
	select {
	case got := <-s.answer:
		if got != "a sentence I typed myself" {
			return fmt.Errorf("the answer arrived as %q", got)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("no answer arrived")
	}
	return nil
}

func (s *askBDD) iAnswer() error {
	if err := s.agentAsksMidTurn(); err != nil {
		return err
	}
	s.m.agentIn.SetValue("yes")
	out, cmd := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("answering must re-arm the drain")
	}
	<-s.answer
	return nil
}

func (s *askBDD) drainStillArmed() error { return nil } // asserted above, at the moment of answering

func (s *askBDD) turnReachesEnd() error {
	for i := 0; i < 50; i++ {
		if _, ok := s.pump().(agentDoneMsg); ok {
			return nil
		}
	}
	return fmt.Errorf("the turn never reached agentDoneMsg after the question was answered")
}

func (s *askBDD) notMutating() error {
	for _, t := range s.rt.loop.Tools() {
		if t.Name == "ask_operator" {
			if t.Mutating {
				return fmt.Errorf("ask_operator must not be Mutating: a question is not a side effect")
			}
			return nil
		}
	}
	return fmt.Errorf("ask_operator is not in the toolset")
}

func (s *askBDD) notThroughApproval() error {
	// The approval gate only ever sees a tool the loop decided to gate. A tool that is not
	// mutating and not named by NeedsConfirm is never offered to it, so no permission mode
	// can reach this one.
	if s.rt.loop.NeedsConfirm != nil {
		for _, t := range s.rt.loop.Tools() {
			if t.Name == "ask_operator" && s.rt.loop.NeedsConfirm(t) {
				return fmt.Errorf("ask_operator must not be widened into the confirm gate")
			}
		}
	}
	return nil
}

func (s *askBDD) approvalModeIs(mode string) error {
	s.enter()
	modes := map[string]agentPermMode{"confirm": permConfirm, "edits": permEdits, "all": permAll}
	pm, ok := modes[mode]
	if !ok {
		return fmt.Errorf("unknown mode %q", mode)
	}
	s.rt.perms.Store(int32(pm))
	return nil
}

func (s *askBDD) agentAsksAQuestion() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("go")()
	s.ask(context.Background(), "still my call?", nil)
	s.pump()
	return nil
}

func (s *askBDD) stillShownToMe() error { return s.questionShown() }

func (s *askBDD) nothingAnswersAutomatically() error {
	select {
	case got := <-s.answer:
		return fmt.Errorf("a permission mode answered the question for the operator: %q", got)
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func (s *askBDD) turnForceStopped() error {
	s.enter()
	s.m.agentBusy = true
	s.m.startAgentTurn("go")()
	if s.rt.cancel != nil {
		s.rt.cancel()
	}
	return nil
}

func (s *askBDD) goroutineReachesAsk() error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ask(ctx, "should I?", nil)
	return nil
}

func (s *askBDD) noQuestionShown() error {
	time.Sleep(200 * time.Millisecond)
	if s.m.agentPendingAsk != nil {
		return fmt.Errorf("a stopped turn put a question on screen")
	}
	return nil
}

func (s *askBDD) agentToldTurnOver() error {
	select {
	case err := <-s.askErr:
		if err == nil {
			return fmt.Errorf("the agent should be told the turn is over, not given an answer")
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the ask never returned")
	}
	return nil
}

func (s *askBDD) questionOnScreen() error { return s.agentAsksMidTurn() }

func (s *askBDD) turnBehindCancelled() error {
	if s.rt.cancel != nil {
		s.rt.cancel()
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

func (s *askBDD) questionStays() error {
	if s.m.agentPendingAsk == nil {
		return fmt.Errorf("a question already on screen was withdrawn when its turn was cancelled")
	}
	return nil
}

func (s *askBDD) answerStillDelivered() error { return s.answerComesBack() }

func (s *askBDD) questionForEndedTurn() error {
	if err := s.agentAsksMidTurn(); err != nil {
		return err
	}
	done := make(chan struct{})
	s.rt.turnDone = done
	close(done)
	nm, _ := s.m.Update(agentDoneMsg{turn: done})
	s.m = asModel(nm)
	return nil
}

func (s *askBDD) notSwallowingKeys() error {
	if s.m.agentPendingAsk != nil {
		return fmt.Errorf("the question outlived its turn and still owns the keys")
	}
	return nil
}

func (s *askBDD) questionOutstanding() error { return s.agentAsksMidTurn() }

func (s *askBDD) goroutineExits() error {
	done := make(chan struct{})
	s.rt.turnDone = done
	close(done)
	return nil
}

func (s *askBDD) drainReturnsPromptly() error {
	got := make(chan tea.Msg, 1)
	go func() { got <- s.m.waitAgentEvent()() }()
	select {
	case <-got:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the drain parked with a question outstanding")
	}
}

func (s *askBDD) emptyQuestion() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("go")()
	_, err := s.askTool().Run(context.Background(), ".", map[string]any{"question": "   "})
	s.askErr <- err
	return nil
}

func (s *askBDD) callFails() error { return s.agentToldTurnOver() }

func (s *askBDD) nothingShown() error { return s.noQuestionShown() }

func (s *askBDD) asksTwice() error {
	if err := s.agentAsksMidTurn(); err != nil {
		return err
	}
	s.m.agentIn.SetValue("first")
	out, _ := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	<-s.answer
	s.ask(context.Background(), "which shall it be?", nil)
	s.pump()
	return nil
}

func (s *askBDD) repeatGuardApplies() error {
	if s.m.agentPendingAsk == nil {
		return fmt.Errorf("a repeated question should still reach the operator")
	}
	return nil
}

// --- BASE STATION ----------------------------------------------------------

func (s *askBDD) remoteAttached() error {
	s.enter()
	s.bridge = newFakeBridge()
	s.m.rcBridge = s.bridge
	return nil
}

func (s *askBDD) mirroredToIt() error {
	for _, f := range s.bridge.emitted {
		if f.Kind == protocol.RCKindAskReq {
			if f.Text != s.m.agentPendingAsk.question {
				return fmt.Errorf("the mirrored question is %q, not the one on screen", f.Text)
			}
			if f.AskID == "" {
				return fmt.Errorf("a mirrored question needs an id, or a late answer cannot be told from a current one")
			}
			s.askID = f.AskID
			return nil
		}
	}
	return fmt.Errorf("no ask_req frame reached the remote surface")
}

func (s *askBDD) eitherResolvesOnce() error {
	// Answer from the REMOTE side, and require it to reach the agent exactly once.
	nm, _ := s.m.onRemoteInbound(protocol.RCInbound{Kind: protocol.RCInAsk, AskID: s.askID, Answer: "from the phone", Origin: "phone"})
	s.m = asModel(nm)
	select {
	case got := <-s.answer:
		if got != "from the phone" {
			return fmt.Errorf("the agent received %q", got)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("a remote answer never reached the agent")
	}
	if s.m.agentPendingAsk != nil {
		return fmt.Errorf("the question should be cleared once answered from either surface")
	}
	var dones int
	for _, f := range s.bridge.emitted {
		if f.Kind == protocol.RCKindAskDone {
			dones++
		}
	}
	if dones != 1 {
		return fmt.Errorf("a question must close exactly once on the wire, got %d ask_done frames", dones)
	}
	return nil
}

func (s *askBDD) answeredAndSecondOpen() error {
	if err := s.remoteAttached(); err != nil {
		return err
	}
	// First question, answered.
	if err := s.agentAsksMidTurn(); err != nil {
		return err
	}
	if err := s.mirroredToIt(); err != nil {
		return err
	}
	s.firstID = s.askID
	s.m.agentIn.SetValue("first answer")
	out, _ := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	<-s.answer
	// Second question, still open.
	s.ask(context.Background(), "and now this one?", nil)
	s.pump()
	if s.m.agentPendingAsk == nil {
		return fmt.Errorf("the second question is not open")
	}
	return nil
}

func (s *askBDD) lateAnswerArrives() error {
	nm, _ := s.m.onRemoteInbound(protocol.RCInbound{Kind: protocol.RCInAsk, AskID: s.firstID, Answer: "stale", Origin: "phone"})
	s.m = asModel(nm)
	return nil
}

func (s *askBDD) itIsDropped() error {
	select {
	case got := <-s.answer:
		return fmt.Errorf("a late answer for an ALREADY-RESOLVED question resolved the current "+
			"one instead: the agent received %q", got)
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func (s *askBDD) secondStillWaiting() error {
	if s.m.agentPendingAsk == nil {
		return fmt.Errorf("the second question was cleared by an answer that was not for it")
	}
	return nil
}

func TestAskOperatorFeature(t *testing.T) {
	st := &askBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = askBDD{t: t}
				return c, nil
			})
			sc.Step(`^I have entered AGENT mode with a band tuned in$`, st.enteredAgent)
			sc.Step(`^the agent asks a question mid-turn$`, st.agentAsksMidTurn)
			sc.Step(`^the question is shown$`, st.questionShown)
			sc.Step(`^my answer comes back as the tool's result$`, st.answerComesBack)
			sc.Step(`^the turn continues from there$`, st.turnContinues)
			sc.Step(`^the agent asks a question with three options$`, st.asksWithThreeOptions)
			sc.Step(`^all three are shown$`, st.allThreeShown)
			sc.Step(`^picking one returns that option as the result$`, st.pickingReturnsIt)
			sc.Step(`^the agent asks an open question$`, st.asksOpenQuestion)
			sc.Step(`^I type an answer$`, st.iTypeAnAnswer)
			sc.Step(`^the answer reaches the agent exactly as I typed it$`, st.answerVerbatim)
			sc.Step(`^I answer a question$`, st.iAnswer)
			sc.Step(`^the drain is still armed$`, st.drainStillArmed)
			sc.Step(`^the turn reaches its end normally$`, st.turnReachesEnd)
			sc.Step(`^ask_operator is not mutating$`, st.notMutating)
			sc.Step(`^it does not pass through the tool-approval gate$`, st.notThroughApproval)
			sc.Step(`^the approval mode is (\w+)$`, st.approvalModeIs)
			sc.Step(`^the agent asks a question$`, st.agentAsksAQuestion)
			sc.Step(`^it is still shown to me$`, st.stillShownToMe)
			sc.Step(`^nothing answers it automatically$`, st.nothingAnswersAutomatically)
			sc.Step(`^a turn has been force-stopped$`, st.turnForceStopped)
			sc.Step(`^its goroutine reaches an ask$`, st.goroutineReachesAsk)
			sc.Step(`^no question is shown$`, st.noQuestionShown)
			sc.Step(`^the agent is told the turn is over$`, st.agentToldTurnOver)
			sc.Step(`^a question is on screen$`, st.questionOnScreen)
			sc.Step(`^the turn behind it is cancelled$`, st.turnBehindCancelled)
			sc.Step(`^the question stays$`, st.questionStays)
			sc.Step(`^my answer is still delivered to whoever asked$`, st.answerStillDelivered)
			sc.Step(`^a question was shown for a turn that has since ended$`, st.questionForEndedTurn)
			sc.Step(`^the prompt does not sit there swallowing keys$`, st.notSwallowingKeys)
			sc.Step(`^a question is outstanding$`, st.questionOutstanding)
			sc.Step(`^the turn's goroutine exits$`, st.goroutineExits)
			sc.Step(`^the drain returns promptly rather than parking$`, st.drainReturnsPromptly)
			sc.Step(`^the agent asks with no question text$`, st.emptyQuestion)
			sc.Step(`^the tool call fails$`, st.callFails)
			sc.Step(`^nothing is shown to the operator$`, st.nothingShown)
			sc.Step(`^the agent asks the same question twice in one turn$`, st.asksTwice)
			sc.Step(`^the repeat guard treats it like any other repeated call$`, st.repeatGuardApplies)

			sc.Step(`^a remote surface is attached$`, st.remoteAttached)
			sc.Step(`^the question is mirrored to it$`, st.mirroredToIt)
			sc.Step(`^an answer from either surface resolves it once$`, st.eitherResolvesOnce)
			sc.Step(`^a question was answered and a second one is now open$`, st.answeredAndSecondOpen)
			sc.Step(`^a late answer for the FIRST arrives from a remote surface$`, st.lateAnswerArrives)
			sc.Step(`^it is dropped$`, st.itIsDropped)
			sc.Step(`^the second question is still waiting$`, st.secondStillWaiting)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/agent/ask_operator.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("ask_operator scenarios failed")
	}
}
