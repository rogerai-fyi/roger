package harness

// agent_bdd_test.go makes features/agent/agent.feature EXECUTABLE, driving the REAL
// in-channel tool-using agent (this package, internal/harness - NOT internal/agent, which is
// the provider/sharing node). No business logic is mocked:
//
//   - Loop.Send drives the real tool-use loop; a deterministic stub Completer (the seam the
//     committed harness_test.go uses) scripts the model turns, and the real BuiltinTools run.
//   - BrokerCompleter relays through a real httptest broker so the model carried on the wire,
//     the consumer cap, the receipt-header readback, the cancel path, and the cap-refusal path
//     are all exercised against actual HTTP, exactly like internal/tui/agent.go wires it.
//
// Scenario 5 ("leaving returns to the channel") is TUI navigation; here we pin the harness-level
// invariant it depends on - the session Loop retains its transcript across a leave (only Reset
// clears it), so re-entering resumes with prior context. The TUI return-to-channel/still-tuned
// half is covered by the TUI agent tests (see the feature's GROUND TRUTH note).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type agentState struct {
	t    *testing.T
	root string

	model    string
	loop     *Loop
	calls    int // completer invocations == billed model calls (the spend proxy)
	final    string
	err      error
	kinds    []EventKind
	cancel   context.CancelFunc
	ctx      context.Context
	resumeOK bool // a re-entered turn saw the prior transcript

	// BrokerCompleter scenarios
	srv      *httptest.Server
	gotModel string
	cost     float64
	tokIn    int
	tokOut   int
	tps      float64
	costHits int
}

func (s *agentState) reset() {
	s.root = s.t.TempDir()
	s.model, s.loop, s.calls = "", nil, 0
	s.final, s.err, s.kinds = "", nil, nil
	s.cancel, s.ctx, s.resumeOK = nil, nil, false
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	s.gotModel, s.cost, s.tokIn, s.tokOut, s.tps, s.costHits = "", 0, 0, 0, 0, 0
}

// --- scenario 1: runs on the channel's model --------------------------------

func (s *agentState) channelTunedTo(model string) error {
	s.model = model
	return nil
}

func (s *agentState) userRunsAgent() error {
	// A real agent turn relayed through a real broker that records the model on the wire.
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = jsonDecode(r, &body)
		s.gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ready"}}]}`))
	}))
	s.loop = NewLoop(s.root, "sys", BrokerCompleter(s.srv.URL, "tester", s.model, false, 0, nil), nil)
	s.final, s.err = s.loop.Send(context.Background(), "hello agent", nil)
	return s.err
}

func (s *agentState) agentStartsOnChannelModel(model string) error {
	if s.gotModel != model {
		return fmt.Errorf("agent relayed on model %q, want the channel's model %q (not some default)", s.gotModel, model)
	}
	if s.gotModel == "" {
		return fmt.Errorf("agent relayed with no model")
	}
	return nil
}

// --- scenario 2: loops over tool calls until done ---------------------------

func (s *agentState) taskNeedsSeveralTools() error {
	if err := os.WriteFile(filepath.Join(s.root, "f.txt"), []byte("carrier locked"), 0644); err != nil {
		return err
	}
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		s.calls++
		switch s.calls {
		case 1:
			return toolCall("c1", "read_file", `{"path":"f.txt"}`), nil
		case 2:
			return toolCall("c2", "list_dir", `{"path":"."}`), nil
		default:
			return Message{Role: "assistant", Content: "done: used read_file then list_dir"}, nil
		}
	}
	s.loop = NewLoop(s.root, "sys", complete, func(string, map[string]any) bool { return true })
	return nil
}

func (s *agentState) itRuns() error {
	s.final, s.err = s.loop.Send(context.Background(), "inspect the repo", func(e Event) { s.kinds = append(s.kinds, e.Kind) })
	return s.err
}

func (s *agentState) iteratesUntilDone() error {
	if s.calls != 3 {
		return fmt.Errorf("model calls = %d, want 3 (two tool rounds, then a final answer)", s.calls)
	}
	var toolCalls, toolResults, finals int
	for _, k := range s.kinds {
		switch k {
		case EventToolCall:
			toolCalls++
		case EventToolResult:
			toolResults++
		case EventFinal:
			finals++
		}
	}
	if toolCalls < 2 || toolResults < 2 {
		return fmt.Errorf("loop ran %d tool calls / %d results, want >=2 each (request->call->result->next)", toolCalls, toolResults)
	}
	if finals != 1 {
		return fmt.Errorf("loop emitted %d final answers, want exactly 1 (it finishes)", finals)
	}
	return nil
}

// --- scenario 3: esc cancels + stops the spend ------------------------------

func (s *agentState) turnInFlight() error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	// The completer cancels mid-call and returns the ctx error, exactly as BrokerCompleter
	// does when esc cancels the in-flight HTTP request (errors.Is(err, context.Canceled)).
	complete := func(c context.Context, _ []Message, _ []map[string]any) (Message, error) {
		s.calls++
		s.cancel()
		return Message{}, c.Err()
	}
	s.loop = NewLoop(s.root, "sys", complete, nil)
	return nil
}

func (s *agentState) userPressesEsc() error {
	s.final, s.err = s.loop.Send(s.ctx, "do a long thing", func(e Event) { s.kinds = append(s.kinds, e.Kind) })
	return nil
}

func (s *agentState) turnCancelled() error {
	if s.err == nil {
		return fmt.Errorf("a cancelled turn must return an error")
	}
	sawCancel := false
	for _, k := range s.kinds {
		if k == EventError {
			sawCancel = true
		}
	}
	if !sawCancel {
		return fmt.Errorf("a cancelled turn should emit a clean cancellation event")
	}
	return nil
}

func (s *agentState) noFurtherTokensBilled() error {
	if s.calls != 1 {
		return fmt.Errorf("model calls after cancel = %d, want 1 (no further billed call - the spend stops)", s.calls)
	}
	return nil
}

// --- scenario 4: the wallet spend cap bounds the agent ----------------------

func (s *agentState) spendCapConfigured() error {
	// The cap is enforced broker-side (shared with every relay); model the broker REFUSING an
	// over-cap turn with the same error shape it returns to any consume path.
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"monthly spend cap reached - raise your limit or wait for the next cycle"}}`))
	}))
	s.loop = NewLoop(s.root, "sys", BrokerCompleter(s.srv.URL, "tester", "gpt-oss-20b", false, 0, nil), nil)
	return nil
}

func (s *agentState) usageWouldExceedCap() error {
	s.final, s.err = s.loop.Send(context.Background(), "an expensive task", func(e Event) { s.kinds = append(s.kinds, e.Kind) })
	return nil
}

func (s *agentState) nextTurnRefused() error {
	if s.err == nil {
		return fmt.Errorf("an over-cap turn must be refused (the loop must surface the broker refusal)")
	}
	if s.final != "" {
		return fmt.Errorf("a refused turn must not produce a final answer, got %q", s.final)
	}
	if !strings.Contains(strings.ToLower(s.err.Error()), "cap") {
		return fmt.Errorf("refusal should name the spend cap, got %q", s.err.Error())
	}
	return nil
}

// --- scenario 5: leaving keeps the session (transcript intact) --------------

func (s *agentState) userIsInTheAgent() error {
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		s.calls++
		return Message{Role: "assistant", Content: "first answer"}, nil
	}
	s.loop = NewLoop(s.root, "carrier persona", complete, nil)
	if _, err := s.loop.Send(context.Background(), "first question", nil); err != nil {
		return err
	}
	return nil
}

func (s *agentState) pressEscToLeave() error {
	// Leaving the agent does NOT Reset the loop (the TUI retains the runtime), so the session
	// transcript survives. Re-enter and ask again: the completer must see the prior turn.
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		for _, m := range msgs {
			if m.Role == "user" && m.Content == "first question" {
				s.resumeOK = true
			}
		}
		return Message{Role: "assistant", Content: "second answer"}, nil
	}
	s.loop.complete = complete
	_, err := s.loop.Send(context.Background(), "second question", nil)
	return err
}

func (s *agentState) returnToChannelTranscriptIntact() error {
	if !s.resumeOK {
		return fmt.Errorf("re-entering the agent did not see the prior transcript - it was not retained")
	}
	// system + (user1, assistant1) + (user2, assistant2) == 5 messages retained (no Reset).
	if got := len(s.loop.messages); got != 5 {
		return fmt.Errorf("retained transcript = %d messages, want 5 (persona + two full turns)", got)
	}
	return nil
}

// --- scenario 6: meters each turn like a normal relay -----------------------

func (s *agentState) agentCompletesTurn() error {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The relay settles cost + BILLED token counts + throughput and rides them back as the
		// same sibling headers plain chat reads.
		w.Header().Set("X-RogerAI-Cost", "0.0123")
		w.Header().Set("X-RogerAI-Tokens-In", "42")
		w.Header().Set("X-RogerAI-Tokens-Out", "108")
		w.Header().Set("X-RogerAI-TPS", "73.5")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"metered"}}]}`))
	}))
	onCost := func(credits float64, in, out int, tps float64) {
		s.costHits++
		s.cost, s.tokIn, s.tokOut, s.tps = credits, in, out, tps
	}
	s.loop = NewLoop(s.root, "sys", BrokerCompleter(s.srv.URL, "tester", "gpt-oss-20b", false, 0, onCost), nil)
	s.final, s.err = s.loop.Send(context.Background(), "one turn", nil)
	return s.err
}

func (s *agentState) turnReceiptsRecorded() error {
	if s.costHits != 1 {
		return fmt.Errorf("the turn's receipt callback fired %d times, want 1 (each turn is metered)", s.costHits)
	}
	if s.cost <= 0 || s.tokIn <= 0 || s.tokOut <= 0 || s.tps <= 0 {
		return fmt.Errorf("recorded receipt = cost %v in %d out %d tps %v, want all > 0 (tokens/throughput/cost)", s.cost, s.tokIn, s.tokOut, s.tps)
	}
	return nil
}

func TestAgentBDD(t *testing.T) {
	// Keep client.SignRequest's key-on-first-use side effect inside the test sandbox.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &agentState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				return ctx, err
			})
			// scenario 1
			sc.Step(`^a channel tuned to model "([^"]*)"$`, st.channelTunedTo)
			sc.Step(`^the user runs /agent$`, st.userRunsAgent)
			sc.Step(`^the agent starts on "([^"]*)" \(the band you're on\), not some default$`, st.agentStartsOnChannelModel)
			// scenario 2
			sc.Step(`^the agent is given a task that needs several tools$`, st.taskNeedsSeveralTools)
			sc.Step(`^it runs$`, st.itRuns)
			sc.Step(`^it iterates request -> tool call -> result -> next request until it finishes or is stopped$`, st.iteratesUntilDone)
			// scenario 3
			sc.Step(`^an agent turn is in flight \(a slow or stuck model\)$`, st.turnInFlight)
			sc.Step(`^the user presses esc$`, st.userPressesEsc)
			sc.Step(`^the turn is cancelled$`, st.turnCancelled)
			sc.Step(`^no further tokens are billed for that turn \(the spend stops\)$`, st.noFurtherTokensBilled)
			// scenario 4
			sc.Step(`^a monthly spend cap is configured$`, st.spendCapConfigured)
			sc.Step(`^the agent's usage would exceed the cap$`, st.usageWouldExceedCap)
			sc.Step(`^the next turn is refused \(the cap bounds the agent like any relay\)$`, st.nextTurnRefused)
			// scenario 5
			sc.Step(`^the user is in the agent$`, st.userIsInTheAgent)
			sc.Step(`^they press esc to leave$`, st.pressEscToLeave)
			sc.Step(`^they return to the channel, still tuned, transcript intact$`, st.returnToChannelTranscriptIntact)
			// scenario 6
			sc.Step(`^the agent completes a turn$`, st.agentCompletesTurn)
			sc.Step(`^that turn's tokens in/out, throughput, latency, and cost are recorded \(same receipts as chat\)$`, st.turnReceiptsRecorded)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/agent/agent.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("agent behavior scenarios failed (see godog output above)")
	}
}

// jsonDecode decodes an HTTP request body into v (small local helper).
func jsonDecode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
