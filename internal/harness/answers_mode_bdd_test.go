package harness

// answers_mode_bdd_test.go makes features/answers/answers_mode.feature EXECUTABLE. Answers
// mode is the existing agent loop doing retrieval, so most of what this pins is that the
// EXISTING guarantees still hold under retrieval pressure: every model call is a normal
// metered relay through a real httptest broker (BrokerCompleter, the same path plain chat
// takes), the spend cap still refuses, and cancellation still stops the spend. What is new
// is the per-turn retrieval budget and the degrade-without-sources path.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type modeState struct {
	t *testing.T

	broker  *httptest.Server
	content *httptest.Server
	pageURL string

	loop       *Loop
	final      string
	err        error
	events     []Event
	modelCalls int
	receipts   int
	cost       float64
	tokIn      int
	tokOut     int
	tps        float64

	searchHits int
	budgetTurn []Event

	origVet func(net.IP) error
}

func (s *modeState) reset() {
	s.loop, s.final, s.err, s.events = nil, "", nil, nil
	s.modelCalls, s.receipts = 0, 0
	s.cost, s.tokIn, s.tokOut, s.tps = 0, 0, 0, 0
	s.searchHits, s.budgetTurn = 0, nil
	s.pageURL = ""
	s.origVet = fetchVetIP
	fetchVetIP = allowLoopbackVet
	s.content = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>page text</p></body></html>"))
	}))
	s.pageURL = s.content.URL + "/"
}

func (s *modeState) restore() {
	fetchVetIP = s.origVet
	for _, srv := range []**httptest.Server{&s.broker, &s.content} {
		if *srv != nil {
			(*srv).Close()
			*srv = nil
		}
	}
}

// meteredBroker stands up a broker that answers with receipts, replying with the scripted
// bodies in order (each is a raw OpenAI-shaped choices payload).
func (s *modeState) meteredBroker(bodies []string) string {
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := s.modelCalls
		s.modelCalls++
		w.Header().Set("X-RogerAI-Cost", "0.0021")
		w.Header().Set("X-RogerAI-Tokens-In", "120")
		w.Header().Set("X-RogerAI-Tokens-Out", "48")
		w.Header().Set("X-RogerAI-TPS", "61.5")
		w.Header().Set("Content-Type", "application/json")
		if i < len(bodies) {
			_, _ = w.Write([]byte(bodies[i]))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answered"}}]}`))
	}))
	return s.broker.URL
}

// fetchCallBody renders an OpenAI response asking for one web_fetch.
func fetchCallBody(id, url string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":"web_fetch","arguments":%q}}]}}]}`,
		id, fmt.Sprintf(`{"url":%q}`, url))
}

func (s *modeState) onCost(credits float64, in, out int, tps float64) {
	s.receipts++
	s.cost, s.tokIn, s.tokOut, s.tps = credits, in, out, tps
}

// --- metering ------------------------------------------------------------------

func (s *modeState) turnWithTwoModelCalls() error {
	url := s.meteredBroker([]string{fetchCallBody("c1", s.pageURL)})
	s.loop = NewLoop(s.t.TempDir(), "sys", BrokerCompleter(url, "tester", "gpt-oss-20b", false, 0, s.onCost), nil)
	s.final, s.err = s.loop.Send(context.Background(), "look that up", func(e Event) { s.events = append(s.events, e) })
	return s.err
}

func (s *modeState) standardReceipts() error {
	if s.modelCalls != 2 {
		return fmt.Errorf("the turn made %d model calls, want 2 (one to ask for the fetch, one to answer)", s.modelCalls)
	}
	if s.receipts != s.modelCalls {
		return fmt.Errorf("%d receipts for %d model calls - every retrieval turn meters like chat", s.receipts, s.modelCalls)
	}
	if s.cost <= 0 || s.tokIn <= 0 || s.tokOut <= 0 || s.tps <= 0 {
		return fmt.Errorf("receipt = cost %v in %d out %d tps %v, want all > 0", s.cost, s.tokIn, s.tokOut, s.tps)
	}
	return nil
}

func (s *modeState) capBoundsThem() error {
	// The cap is enforced BROKER-side, on the same relay every chat turn takes. What is
	// checkable here is that a retrieval turn really rides that path: it read back the
	// per-turn receipt headers the cap is accounted from, for every model call, rather
	// than short-circuiting retrieval work around the relay.
	if s.broker == nil {
		return fmt.Errorf("the turn did not go through the broker relay")
	}
	if s.receipts != s.modelCalls || s.receipts == 0 {
		return fmt.Errorf("%d receipts for %d model calls: retrieval turns must be accounted like any relay", s.receipts, s.modelCalls)
	}
	return nil
}

func (s *modeState) capCrossedMidTurn() error {
	// First call asks for a fetch; the second (post-retrieval) call is refused by the cap.
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := s.modelCalls
		s.modelCalls++
		w.Header().Set("Content-Type", "application/json")
		if i == 0 {
			_, _ = w.Write([]byte(fetchCallBody("c1", s.pageURL)))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"monthly spend cap reached - raise your limit or wait for the next cycle"}}`))
	}))
	s.loop = NewLoop(s.t.TempDir(), "sys", BrokerCompleter(s.broker.URL, "tester", "gpt-oss-20b", false, 0, s.onCost), nil)
	return nil
}

func (s *modeState) loopAttemptsNextCall() error {
	s.final, s.err = s.loop.Send(context.Background(), "look that up", func(e Event) { s.events = append(s.events, e) })
	return nil
}

func (s *modeState) refusalIsTheOutcome() error {
	if s.err == nil {
		return fmt.Errorf("an over-cap turn must surface the refusal, got final %q", s.final)
	}
	if !strings.Contains(strings.ToLower(s.err.Error()), "cap") {
		return fmt.Errorf("the refusal should name the spend cap, got %v", s.err)
	}
	return nil
}

func (s *modeState) noFurtherWork() error {
	if s.modelCalls != 2 {
		return fmt.Errorf("the loop made %d model calls, want 2 (it stopped at the refusal)", s.modelCalls)
	}
	fetches := 0
	for _, e := range s.events {
		if e.Kind == EventToolResult && e.Tool == "web_fetch" {
			fetches++
		}
	}
	if fetches != 1 {
		return fmt.Errorf("%d retrievals ran, want the 1 from before the refusal", fetches)
	}
	return nil
}

// --- the per-turn retrieval budget ----------------------------------------------

func (s *modeState) budgetIs(searches, fetches int) error {
	if maxSearchesPerTurn != searches || maxFetchesPerTurn != fetches {
		return fmt.Errorf("budget is %d searches / %d fetches, want %d / %d",
			maxSearchesPerTurn, maxFetchesPerTurn, searches, fetches)
	}
	return nil
}

// searchLoop builds a loop whose model calls web_search n times then answers, with a stub
// search tool that records how many times it actually reached the provider.
func (s *modeState) searchLoop(n int) {
	calls := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls <= n {
			return toolCall(fmt.Sprintf("s%d", calls), "web_search", `{"query":"q"}`), nil
		}
		return Message{Role: "assistant", Content: "answered with what I have"}, nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	// GUARDS OFF (2026-08-20). These scenarios drive web_fetch directly on fixture
	// URLs no user ever typed, because what they exercise is the layer BELOW the
	// guards: retrieval, citation derivation and injection wrapping. With the default
	// chain on, GuardFetchProvenance would refuse every one of those fetches - which
	// is correct behaviour and would make these tests assert nothing. The guards have
	// their own suite (guards_test.go).
	s.loop.Guards = []Guard{}
	s.loop.MaxSteps = n + 2
	stub := Tool{Name: "web_search", Run: func(context.Context, string, map[string]any) (string, error) {
		s.searchHits++
		return "[1] Result\n    https://a.example/x\n    snip", nil
	}}
	s.loop.toolByName["web_search"] = stub
	s.loop.tools = append(s.loop.tools, stub)
}

func (s *modeState) fourthSearchAttempted() error {
	s.searchLoop(maxSearchesPerTurn + 1)
	s.final, s.err = s.loop.Send(context.Background(), "search a lot", func(e Event) { s.events = append(s.events, e) })
	return s.err
}

func (s *modeState) budgetResultNoNetwork() error {
	if s.searchHits != maxSearchesPerTurn {
		return fmt.Errorf("the provider was reached %d times, want at most the budget of %d", s.searchHits, maxSearchesPerTurn)
	}
	var results []Event
	for _, e := range s.events {
		if e.Kind == EventToolResult && e.Tool == "web_search" {
			results = append(results, e)
		}
	}
	if len(results) != maxSearchesPerTurn+1 {
		return fmt.Errorf("%d search results recorded, want %d (the over-budget call still answers)", len(results), maxSearchesPerTurn+1)
	}
	if !strings.Contains(strings.ToLower(results[maxSearchesPerTurn].Result), "budget") {
		return fmt.Errorf("the over-budget call returned %q, want a budget-exhausted result", results[maxSearchesPerTurn].Result)
	}
	return nil
}

func (s *modeState) toldToAnswerWithWhatItHas() error {
	for _, e := range s.events {
		if e.Kind == EventToolResult && e.Tool == "web_search" && strings.Contains(strings.ToLower(e.Result), "budget") {
			if !strings.Contains(strings.ToLower(e.Result), "answer") {
				return fmt.Errorf("the budget result does not tell the model what to do next: %q", e.Result)
			}
		}
	}
	if last := s.events[len(s.events)-1]; last.Kind != EventFinal {
		return fmt.Errorf("the turn did not end with an answer")
	}
	return nil
}

func (s *modeState) previousTurnExhausted() error {
	s.searchLoop(maxSearchesPerTurn + 1)
	if _, err := s.loop.Send(context.Background(), "search a lot", nil); err != nil {
		return err
	}
	s.searchHits = 0
	return nil
}

func (s *modeState) userSendsNewMessage() error {
	// Re-script the SAME loop for a second turn: one search, then an answer.
	calls := 0
	s.loop.complete = func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("n1", "web_search", `{"query":"fresh"}`), nil
		}
		return Message{Role: "assistant", Content: "answered"}, nil
	}
	s.final, s.err = s.loop.Send(context.Background(), "a new question", func(e Event) { s.budgetTurn = append(s.budgetTurn, e) })
	return s.err
}

func (s *modeState) newTurnHasFullBudget() error {
	if s.searchHits != 1 {
		return fmt.Errorf("the new turn reached the provider %d times, want 1 (the budget reset)", s.searchHits)
	}
	for _, e := range s.budgetTurn {
		if e.Kind == EventToolResult && strings.Contains(strings.ToLower(e.Result), "budget") {
			return fmt.Errorf("the new turn hit the previous turn's exhausted budget: %q", e.Result)
		}
	}
	return nil
}

func (s *modeState) budgetExhaustedMidTurn() error {
	return s.fourthSearchAttempted()
}

func (s *modeState) stillProducesAnswer() error {
	if s.err != nil {
		return fmt.Errorf("the turn errored: %v", s.err)
	}
	if strings.TrimSpace(s.final) == "" {
		return fmt.Errorf("the turn produced no answer")
	}
	return nil
}

func (s *modeState) notPresentedAsFailure() error {
	// The production property (the final text is the test's own script, so asserting on it
	// would prove nothing): the budget refusal is surfaced as ordinary information - not an
	// error event, and not something that ends the turn.
	var found bool
	for _, e := range s.events {
		if e.Kind != EventToolResult || !strings.Contains(strings.ToLower(e.Result), "budget") {
			continue
		}
		found = true
		if e.IsError {
			return fmt.Errorf("the budget refusal was flagged as an error: %q", e.Result)
		}
		if e.Denied {
			return fmt.Errorf("the budget refusal was flagged as a denial: %q", e.Result)
		}
	}
	if !found {
		return fmt.Errorf("no budget refusal was recorded")
	}
	if s.err != nil {
		return fmt.Errorf("the turn ended in error: %v", s.err)
	}
	return nil
}

// --- degradation ------------------------------------------------------------------

func (s *modeState) providerDownAllTurn() error {
	calls := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("s1", "web_search", `{"query":"q"}`), nil
		}
		return Message{Role: "assistant", Content: "answered from what I know"}, nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	// GUARDS OFF (2026-08-20). These scenarios drive web_fetch directly on fixture
	// URLs no user ever typed, because what they exercise is the layer BELOW the
	// guards: retrieval, citation derivation and injection wrapping. With the default
	// chain on, GuardFetchProvenance would refuse every one of those fetches - which
	// is correct behaviour and would make these tests assert nothing. The guards have
	// their own suite (guards_test.go).
	s.loop.Guards = []Guard{}
	down := Tool{Name: "web_search", Run: func(context.Context, string, map[string]any) (string, error) {
		return "search failed: search provider returned HTTP 503", nil
	}}
	s.loop.toolByName["web_search"] = down
	s.loop.tools = append(s.loop.tools, down)
	return nil
}

func (s *modeState) userAsksQuestion() error {
	s.final, s.err = s.loop.Send(context.Background(), "what is the news?", func(e Event) { s.events = append(s.events, e) })
	return s.err
}

func (s *modeState) modelReceivesFailure() error {
	for _, e := range s.events {
		if e.Kind == EventToolResult && strings.Contains(e.Result, "search failed") {
			return nil
		}
	}
	return fmt.Errorf("the model was never told the search failed")
}

func (s *modeState) turnStillAnswers() error { return s.stillProducesAnswer() }

func (s *modeState) noSourcesBlockRendered() error {
	if len(s.loop.sources()) != 0 {
		return fmt.Errorf("sources were cited with nothing retrieved: %+v", s.loop.sources())
	}
	if strings.Contains(s.final, "Sources:") {
		return fmt.Errorf("a sources block was rendered with nothing retrieved: %q", s.final)
	}
	return nil
}

func (s *modeState) fetchInFlight() error {
	s.content.Close()
	s.content = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	s.pageURL = s.content.URL + "/"
	return nil
}

func (s *modeState) escPressed() error {
	ctx, cancel := context.WithCancel(context.Background())
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		s.modelCalls++
		return toolCall("c1", "web_fetch", fetchArgs(s.pageURL)), nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	// GUARDS OFF (2026-08-20). These scenarios drive web_fetch directly on fixture
	// URLs no user ever typed, because what they exercise is the layer BELOW the
	// guards: retrieval, citation derivation and injection wrapping. With the default
	// chain on, GuardFetchProvenance would refuse every one of those fetches - which
	// is correct behaviour and would make these tests assert nothing. The guards have
	// their own suite (guards_test.go).
	s.loop.Guards = []Guard{}
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	s.final, s.err = s.loop.Send(ctx, "read it", func(e Event) { s.events = append(s.events, e) })
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		return fmt.Errorf("the turn took %s to stop, want the in-flight fetch abandoned promptly", elapsed)
	}
	return nil
}

func (s *modeState) fetchAbandoned() error {
	for _, e := range s.events {
		if e.Kind == EventToolResult && e.Tool == "web_fetch" && !e.IsError {
			return fmt.Errorf("the cancelled fetch still returned content: %q", e.Result)
		}
	}
	return nil
}

func (s *modeState) turnStopsNoFurtherBilling() error {
	if s.err == nil {
		return fmt.Errorf("a cancelled turn must return an error")
	}
	if s.modelCalls != 1 {
		return fmt.Errorf("%d model calls after cancel, want 1", s.modelCalls)
	}
	return nil
}

func TestAnswersModeBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &modeState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.restore()
				return ctx, err
			})

			sc.Step(`^an answers turn that made 2 model calls around its retrievals$`, st.turnWithTwoModelCalls)
			sc.Step(`^each model call produced the standard receipt \(cost, tokens in/out, tps\)$`, st.standardReceipts)
			sc.Step(`^the wallet's spend cap bounded them like any relay$`, st.capBoundsThem)
			sc.Step(`^the spend cap is crossed between retrieval steps$`, st.capCrossedMidTurn)
			sc.Step(`^the loop attempts the next model call$`, st.loopAttemptsNextCall)
			sc.Step(`^the refusal surfaces as the turn's outcome$`, st.refusalIsTheOutcome)
			sc.Step(`^no further retrievals or model calls are made$`, st.noFurtherWork)

			sc.Step(`^the per-turn budget of (\d+) searches and (\d+) fetches$`, st.budgetIs)
			sc.Step(`^the model attempts a 4th web_search in one turn$`, st.fourthSearchAttempted)
			sc.Step(`^the call returns a budget-exhausted tool result without touching the network$`, st.budgetResultNoNetwork)
			sc.Step(`^the model is told to answer with what it has$`, st.toldToAnswerWithWhatItHas)
			sc.Step(`^the previous turn exhausted its retrieval budget$`, st.previousTurnExhausted)
			sc.Step(`^the user sends a new message$`, st.userSendsNewMessage)
			sc.Step(`^the new turn starts with a full budget$`, st.newTurnHasFullBudget)
			sc.Step(`^the budget is exhausted mid-turn$`, st.budgetExhaustedMidTurn)
			sc.Step(`^the turn still produces an answer \(with the sources gathered so far\)$`, st.stillProducesAnswer)
			sc.Step(`^the answer is not presented as a failure$`, st.notPresentedAsFailure)

			sc.Step(`^the search provider is down for the whole turn$`, st.providerDownAllTurn)
			sc.Step(`^the user asks a question$`, st.userAsksQuestion)
			sc.Step(`^the model receives the failure as tool results$`, st.modelReceivesFailure)
			sc.Step(`^the turn still produces an answer$`, st.turnStillAnswers)
			sc.Step(`^the answer carries no sources block \(nothing was retrieved\)$`, st.noSourcesBlockRendered)

			sc.Step(`^a web_fetch is in flight against a slow host$`, st.fetchInFlight)
			sc.Step(`^the user presses esc$`, st.escPressed)
			sc.Step(`^the fetch is abandoned promptly$`, st.fetchAbandoned)
			sc.Step(`^the turn stops with no further billed model call$`, st.turnStopsNoFurtherBilling)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/answers/answers_mode.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("answers mode behavior scenarios failed (see godog output above)")
	}
}
