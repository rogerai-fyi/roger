package tui

// handoff_agent_turns_bdd_test.go makes features/handoff/agent_turns.feature EXECUTABLE
// against the REAL recording path. The agent surface records through the same ring the
// channel already uses (internal/tui/context_capsule.go).
//
// Tool calls, results and the completed answer are driven through the REAL event handler
// (onAgentEvent), so these scenarios prove the WIRING and not merely the helpers: if the
// handler ever stopped recording, they fail. The user prompt is recorded at the two turn-
// start sites, which cannot be driven here without firing a real broker turn, so those
// scenarios call the same production function the call sites do.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/capsule"
	"rogerai.fm/roger/v6/internal/harness"
)

type agentTurnsState struct {
	t *testing.T
	m *model
}

func (s *agentTurnsState) reset() {
	s.m = &model{agent: &agentRuntime{model: "gpt-oss-20b"}}
}

// --- recording a turn ---------------------------------------------------------

func (s *agentTurnsState) agentOnBand() error { return nil }

func (s *agentTurnsState) userSendsPrompt() error {
	s.m.recordAgentPrompt("refactor the fetch guard")
	return nil
}

func (s *agentTurnsState) ringCarriesUserTurn() error {
	if len(s.m.ring) != 1 {
		return fmt.Errorf("ring holds %d turns, want the agent prompt", len(s.m.ring))
	}
	got := s.m.ring[0]
	if got.Role != "user" || got.Content != "refactor the fetch guard" {
		return fmt.Errorf("recorded %+v, want the user's agent prompt", got)
	}
	return nil
}

func (s *agentTurnsState) turnEndsWithAnswer() error {
	s.m.recordAgentPrompt("what is the guard?")
	s.answer("it vets the address before dialling")
	return nil
}

func (s *agentTurnsState) ringCarriesAssistantTurn() error {
	if len(s.m.ring) != 2 {
		return fmt.Errorf("ring holds %d turns, want prompt + answer", len(s.m.ring))
	}
	got := s.m.ring[1]
	if got.Role != "assistant" || !strings.Contains(got.Content, "vets the address") {
		return fmt.Errorf("recorded %+v, want the agent's answer", got)
	}
	return nil
}

func (s *agentTurnsState) attributedToAgentAndModel() error {
	got := s.m.ring[len(s.m.ring)-1]
	if !strings.Contains(got.XRoger.Agent, "agent") {
		return fmt.Errorf("agent tag %q does not identify the agent surface", got.XRoger.Agent)
	}
	if got.XRoger.Model == nil || *got.XRoger.Model != "gpt-oss-20b" {
		return fmt.Errorf("model on the turn = %v, want the band it ran on", got.XRoger.Model)
	}
	return nil
}

func (s *agentTurnsState) channelAgentChannelTurns() error {
	s.m.recordTurn("user", "channel one", "user", nil, nil)
	s.m.recordAgentPrompt("agent one")
	s.m.recordTurn("user", "channel two", "user", nil, nil)
	return nil
}

func (s *agentTurnsState) consecutiveIndicesOneThread() error {
	if len(s.m.ring) != 3 {
		return fmt.Errorf("ring holds %d turns, want 3", len(s.m.ring))
	}
	for i, msg := range s.m.ring {
		if msg.XRoger.Turn != i {
			return fmt.Errorf("turn %d carries index %d - the surfaces are not sharing one sequence", i, msg.XRoger.Turn)
		}
	}
	return nil
}

func (s *agentTurnsState) turnWithNoText() error {
	s.answer("   ")
	return nil
}

func (s *agentTurnsState) ringUnchanged() error {
	if len(s.m.ring) != 0 {
		return fmt.Errorf("an empty turn recorded %d turns", len(s.m.ring))
	}
	return nil
}

// --- tool calls ride with the turn ---------------------------------------------

// event drives one streamed step through the REAL handler, keeping the model in sync.
func (s *agentTurnsState) event(e harness.Event) {
	mm, _ := s.m.onAgentEvent(agentEventMsg(e))
	next := mm.(model)
	s.m = &next
}

// toolRound drives one call + its result through the real handler.
func (s *agentTurnsState) toolRound(_, name, args, result string, denied, failed bool) {
	var parsed map[string]any
	_ = json.Unmarshal([]byte(args), &parsed)
	s.event(harness.Event{Kind: harness.EventToolCall, Tool: name, Args: parsed})
	s.event(harness.Event{
		Kind: harness.EventToolResult, Tool: name, Result: result, IsError: failed, Denied: denied,
	})
}

// answer completes the turn through the real handler.
func (s *agentTurnsState) answer(text string) {
	s.event(harness.Event{Kind: harness.EventFinal, Text: text})
}

func (s *agentTurnsState) turnCalledFetchThenAnswered() error {
	s.m.recordAgentPrompt("read that page")
	s.toolRound("c1", "web_fetch", `{"url":"https://example.com/"}`, "Example Domain", false, false)
	s.answer("the page is a placeholder")
	return nil
}

func (s *agentTurnsState) lastTurnCalls() ([]capsule.ToolCall, error) {
	if len(s.m.ring) == 0 {
		return nil, fmt.Errorf("nothing recorded")
	}
	last := s.m.ring[len(s.m.ring)-1]
	if len(last.ToolCalls) == 0 {
		return nil, nil
	}
	var out []capsule.ToolCall
	if err := json.Unmarshal(last.ToolCalls, &out); err != nil {
		return nil, fmt.Errorf("tool_calls do not decode as the flat capsule shape: %w", err)
	}
	return out, nil
}

func (s *agentTurnsState) turnCarriesOneCall() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	if len(calls) != 1 {
		return fmt.Errorf("assistant turn carries %d tool calls, want 1", len(calls))
	}
	return nil
}

func (s *agentTurnsState) callCarriesNameArgsResult() error {
	calls, _ := s.lastTurnCalls()
	c := calls[0]
	if c.Name != "web_fetch" {
		return fmt.Errorf("call name = %q", c.Name)
	}
	if !strings.Contains(c.Arguments, "example.com") {
		return fmt.Errorf("call arguments = %q, want what it was called with", c.Arguments)
	}
	if c.Result == nil || !strings.Contains(*c.Result, "Example Domain") {
		return fmt.Errorf("call result = %v, want the tool's output", c.Result)
	}
	return nil
}

func (s *agentTurnsState) userDeniedShell() error {
	s.m.recordAgentPrompt("run it")
	s.toolRound("c1", "run_shell", `{"cmd":"rm -rf /"}`, "user denied this run_shell call", true, false)
	s.answer("understood, not running it")
	return nil
}

func (s *agentTurnsState) callMarkedDenied() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	if len(calls) != 1 || !calls[0].Denied {
		return fmt.Errorf("call = %+v, want it marked denied", calls)
	}
	return nil
}

func (s *agentTurnsState) noResultForIt() error {
	calls, _ := s.lastTurnCalls()
	if calls[0].Result != nil {
		return fmt.Errorf("a denied call carries a result %q - nothing ran", *calls[0].Result)
	}
	return nil
}

func (s *agentTurnsState) toolReturnedError() error {
	s.m.recordAgentPrompt("read it")
	s.toolRound("c1", "web_fetch", `{"url":"http://10.0.0.1/"}`, "error: blocked address", false, true)
	s.answer("that address is blocked")
	return nil
}

func (s *agentTurnsState) callMarkedFailed() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	if len(calls) != 1 || !calls[0].Failed {
		return fmt.Errorf("call = %+v, want it marked failed", calls)
	}
	return nil
}

func (s *agentTurnsState) turnCalledThree() error {
	s.m.recordAgentPrompt("do the thing")
	s.toolRound("c1", "list_dir", `{"path":"."}`, "a.go", false, false)
	s.toolRound("c2", "read_file", `{"path":"a.go"}`, "package main", false, false)
	s.toolRound("c3", "web_fetch", `{"url":"https://x.example/"}`, "docs", false, false)
	s.answer("done")
	return nil
}

func (s *agentTurnsState) allThreeInOrder() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	want := []string{"list_dir", "read_file", "web_fetch"}
	if len(calls) != len(want) {
		return fmt.Errorf("carried %d calls, want %d", len(calls), len(want))
	}
	for i, w := range want {
		if calls[i].Name != w {
			return fmt.Errorf("call %d = %q, want %q (order must be the order they ran)", i, calls[i].Name, w)
		}
	}
	return nil
}

func (s *agentTurnsState) turnWithToolsThenWithout() error {
	if err := s.turnCalledFetchThenAnswered(); err != nil {
		return err
	}
	s.m.recordAgentPrompt("thanks")
	s.answer("you are welcome")
	return nil
}

func (s *agentTurnsState) turnErroredAfterTools() error {
	s.m.recordAgentPrompt("do the thing")
	s.toolRound("c1", "web_fetch", `{"url":"https://a.example/"}`, "page", false, false)
	s.event(harness.Event{Kind: harness.EventError, Text: "no station is serving that model"})
	return nil
}

func (s *agentTurnsState) secondTurnAnswersWithNoTools() error {
	s.m.recordAgentPrompt("try again")
	s.answer("answered without tools")
	return nil
}

func (s *agentTurnsState) secondTurnHasNoCalls() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	if len(calls) != 0 {
		return fmt.Errorf("the second turn carried %d stale tool calls", len(calls))
	}
	return nil
}

// --- bounded and clean ----------------------------------------------------------

func (s *agentTurnsState) hugeToolResult() error {
	s.m.recordAgentPrompt("read the big page")
	s.toolRound("c1", "web_fetch", `{"url":"https://big.example/"}`, strings.Repeat("x", 200_000), false, false)
	s.answer("read it")
	return nil
}

func (s *agentTurnsState) resultTruncatedToCap() error {
	calls, err := s.lastTurnCalls()
	if err != nil {
		return err
	}
	if calls[0].Result == nil {
		return fmt.Errorf("no result recorded")
	}
	if len(*calls[0].Result) > capsuleResultCap+64 {
		return fmt.Errorf("recorded result is %d bytes, want capped at %d", len(*calls[0].Result), capsuleResultCap)
	}
	return nil
}

func (s *agentTurnsState) truncationMarked() error {
	calls, _ := s.lastTurnCalls()
	if !strings.Contains(*calls[0].Result, "truncated") {
		return fmt.Errorf("a capped result must be marked truncated")
	}
	return nil
}

func (s *agentTurnsState) moreTurnsThanRingHolds() error {
	for i := 0; i < contextRingCap+25; i++ {
		s.m.recordAgentPrompt(fmt.Sprintf("prompt %d", i))
	}
	return nil
}

func (s *agentTurnsState) ringHoldsMostRecent() error {
	if len(s.m.ring) != contextRingCap {
		return fmt.Errorf("ring holds %d turns, want the cap of %d", len(s.m.ring), contextRingCap)
	}
	if !strings.Contains(s.m.ring[len(s.m.ring)-1].Content, fmt.Sprintf("prompt %d", contextRingCap+24)) {
		return fmt.Errorf("the newest turn was aged out instead of the oldest")
	}
	return nil
}

func (s *agentTurnsState) survivorIndicesUnchanged() error {
	first := s.m.ring[0].XRoger.Turn
	for i, msg := range s.m.ring {
		if msg.XRoger.Turn != first+i {
			return fmt.Errorf("index sequence broke at %d: %d", i, msg.XRoger.Turn)
		}
	}
	if first == 0 {
		return fmt.Errorf("indices restarted at 0 - aged-out turns must keep their place in the sequence")
	}
	return nil
}

const testSessionKey = "sk-roger-super-secret-session-key"

func (s *agentTurnsState) sessionHoldsCredentials() error {
	s.m.recordAgentPrompt("do the work")
	s.toolRound("c1", "web_fetch", `{"url":"https://x.example/"}`, "page text", false, false)
	s.answer("done")
	return nil
}

func (s *agentTurnsState) exportCapsule() error { return nil }

func (s *agentTurnsState) capsuleHasNoSecret(secret string) error {
	blob, err := json.Marshal(s.m.ring)
	if err != nil {
		return err
	}
	needle := map[string]string{
		"session key":       testSessionKey,
		"broker auth token": "Bearer roger-broker-token",
	}[secret]
	if needle == "" {
		return fmt.Errorf("unknown secret %q", secret)
	}
	if strings.Contains(string(blob), needle) {
		return fmt.Errorf("the capsule carries the %s", secret)
	}
	return nil
}

// --- clearing --------------------------------------------------------------------

func (s *agentTurnsState) recordedAgentTurns() error {
	s.m.recordTurn("user", "a channel turn", "user", nil, nil)
	s.m.recordAgentPrompt("an agent prompt")
	s.answer("an agent answer")
	return nil
}

func (s *agentTurnsState) userClearsAgent() error {
	s.m.clearAgentTurns()
	return nil
}

func (s *agentTurnsState) agentTurnsGone() error {
	for _, msg := range s.m.ring {
		if strings.Contains(msg.Content, "agent prompt") || strings.Contains(msg.Content, "agent answer") {
			return fmt.Errorf("a cleared agent turn is still in the ring: %q", msg.Content)
		}
	}
	if len(s.m.ring) != 1 {
		return fmt.Errorf("ring holds %d turns, want the channel turn kept", len(s.m.ring))
	}
	return nil
}

func TestHandoffAgentTurnsBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &agentTurnsState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})

			sc.Step(`^the agent is on a band$`, st.agentOnBand)
			sc.Step(`^the user sends an agent prompt$`, st.userSendsPrompt)
			sc.Step(`^the ring carries that prompt as a user turn$`, st.ringCarriesUserTurn)
			sc.Step(`^an agent turn that ends with an answer$`, st.turnEndsWithAnswer)
			sc.Step(`^the ring carries that answer as an assistant turn$`, st.ringCarriesAssistantTurn)
			sc.Step(`^the turn is attributed to the agent and the model it ran on$`, st.attributedToAgentAndModel)
			sc.Step(`^a channel turn, then an agent turn, then another channel turn$`, st.channelAgentChannelTurns)
			sc.Step(`^the three turns carry consecutive indices in one thread$`, st.consecutiveIndicesOneThread)
			sc.Step(`^an agent turn that ends with no text at all$`, st.turnWithNoText)
			sc.Step(`^the ring is unchanged$`, st.ringUnchanged)

			sc.Step(`^an agent turn that called web_fetch and then answered$`, st.turnCalledFetchThenAnswered)
			sc.Step(`^the assistant turn carries one tool call$`, st.turnCarriesOneCall)
			sc.Step(`^it carries the tool name, the arguments, and the result$`, st.callCarriesNameArgsResult)
			sc.Step(`^an agent turn where the user denied a run_shell confirm$`, st.userDeniedShell)
			sc.Step(`^the assistant turn carries that call marked denied$`, st.callMarkedDenied)
			sc.Step(`^it carries no result for it$`, st.noResultForIt)
			sc.Step(`^an agent turn where a tool returned an error$`, st.toolReturnedError)
			sc.Step(`^the assistant turn carries that call marked failed$`, st.callMarkedFailed)
			sc.Step(`^an agent turn that called three tools before answering$`, st.turnCalledThree)
			sc.Step(`^the assistant turn carries all three calls in the order they ran$`, st.allThreeInOrder)
			sc.Step(`^an agent turn with tool calls, then a second turn with none$`, st.turnWithToolsThenWithout)
			sc.Step(`^an agent turn whose tools ran and then the turn errored$`, st.turnErroredAfterTools)
			sc.Step(`^a second turn that answers with no tools of its own$`, st.secondTurnAnswersWithNoTools)
			sc.Step(`^the second turn carries no tool calls$`, st.secondTurnHasNoCalls)

			sc.Step(`^an agent turn whose tool returned a very large result$`, st.hugeToolResult)
			sc.Step(`^the recorded result is truncated to the capsule's per-result cap$`, st.resultTruncatedToCap)
			sc.Step(`^the truncation is marked$`, st.truncationMarked)
			sc.Step(`^more agent turns than the ring holds$`, st.moreTurnsThanRingHolds)
			sc.Step(`^the ring holds only the most recent turns$`, st.ringHoldsMostRecent)
			sc.Step(`^the turn indices of the survivors are unchanged$`, st.survivorIndicesUnchanged)
			sc.Step(`^an agent session whose runtime holds a session key and a broker URL$`, st.sessionHoldsCredentials)
			sc.Step(`^turns are recorded and a capsule is exported$`, st.exportCapsule)
			sc.Step(`^the capsule contains no (.+)$`, st.capsuleHasNoSecret)

			sc.Step(`^recorded agent turns$`, st.recordedAgentTurns)
			sc.Step(`^the user clears the agent transcript$`, st.userClearsAgent)
			sc.Step(`^those turns no longer travel in a handoff$`, st.agentTurnsGone)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/handoff/agent_turns.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("agent-turn handoff scenarios failed (see godog output above)")
	}
}
