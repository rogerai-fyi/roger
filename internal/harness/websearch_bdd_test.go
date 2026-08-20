package harness

// websearch_bdd_test.go makes features/answers/web_search.feature EXECUTABLE against the
// REAL builtin toolset and the REAL agent loop. Nothing is mocked except the far side of
// the wire: the Brave adapter talks to an httptest server speaking Brave's real response
// shape ({"web":{"results":[{title,url,description}]}}), so the ADAPTER CONTRACT is under
// test, not a stand-in for it. The model turns are scripted by the same deterministic stub
// Completer the committed harness_test.go / agent_bdd_test.go use.
//
// The search config is a real file at <UserConfigDir>/rogerai/search.json (the house
// persona.go/history.go layout); the suite points XDG_CONFIG_HOME at a temp dir, so
// "configured" and "not configured" are exercised as the real on-disk states.
//
// NOTE on the endpoint override: search.json's "endpoint" is OPERATOR-supplied config, not
// a model-supplied URL, so it is deliberately NOT subject to the web_fetch SSRF guard -
// that is what lets this suite (and a future self-hosted SearXNG) point it at loopback.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type searchState struct {
	t *testing.T

	root string
	srv  *httptest.Server

	// PROVIDER BEHAVIOUR KNOBS, ALL UNDER stubMu.
	//
	// Every field below is written by a step function on the test goroutine and read - or, for
	// hits/gotQuery/gotCount, written - by the stub provider's HTTP handler on a server
	// goroutine, and nothing used to order the two. "The step waits for the response" is not
	// the synchronisation it reads as: a socket round trip is not a happens-before edge, and
	// the connect-timeout scenario deliberately leaves one handler PARKED inside the closure
	// while the turn moves on, so two handlers overlap outright. The race detector caught that
	// pair on hits/gotQuery/gotCount (1 run in 12 of `-run TestWebSearchBDD -race`), and a
	// lost hits++ is not a detector curiosity here - three Thens assert an EXACT provider
	// request count, one of them the no-retry-on-429 invariant.
	stubMu   sync.Mutex
	results  []map[string]any // the "web.results" array the stub returns
	status   int              // provider HTTP status (0 = 200)
	rawBody  string           // when set, returned verbatim (malformed-JSON case)
	hang     bool             // simulate a connect timeout
	hits     int              // provider requests actually made
	gotQuery string
	gotCount string

	// tool execution
	result string
	err    error

	// loop execution
	loop      *Loop
	calls     int
	confirms  int
	final     string
	loopErr   error
	toolResIn string // the tool result the model saw on its next turn
}

func (s *searchState) reset() {
	// TEAR THE PREVIOUS PROVIDER DOWN FIRST, THEN CLEAR WHAT IT WAS WRITING. This used to be
	// the other way round, which is a race with the same shape as the one stubMu exists for
	// and one the mutex alone would not fix: Close is what WAITS for a handler still inside
	// the closure, so clearing hits and the captured query above it meant zeroing counters
	// that the last scenario's parked timeout handler could still be incrementing.
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	s.root = s.t.TempDir()
	s.stubMu.Lock()
	s.results = []map[string]any{
		{"title": "Valkey pubsub reconnect", "url": "https://valkey.io/topics/pubsub/", "description": "reconnect and backoff"},
		{"title": "Redis client backoff", "url": "https://redis.io/docs/backoff/", "description": "exponential backoff"},
		{"title": "Bus resubscribe notes", "url": "https://example.org/bus", "description": "resubscribe after a drop"},
	}
	s.status, s.rawBody, s.hang, s.hits = 0, "", false, 0
	s.gotQuery, s.gotCount = "", ""
	s.stubMu.Unlock()
	searchTimeout = 15 * time.Second // restore the seam any scenario shortened
	s.result, s.err = "", nil
	s.loop, s.calls, s.confirms = nil, 0, 0
	s.final, s.loopErr, s.toolResIn = "", nil, ""
	// Each scenario starts from a clean config dir: "configured" is a real file.
	cfg := t_configDir(s.t)
	_ = os.RemoveAll(filepath.Join(cfg, "rogerai", "search.json"))
}

// t_configDir returns the suite's isolated <UserConfigDir> (XDG_CONFIG_HOME is set by the
// test func), creating the rogerai dir.
func t_configDir(t *testing.T) string {
	d, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(d, "rogerai"), 0o755)
	return d
}

// startProvider stands up the Brave-shaped stub and returns its URL.
func (s *searchState) startProvider() string {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RECORD AND SNAPSHOT UNDER THE LOCK, THEN ANSWER WITHOUT IT. The release is not an
		// optimisation: the hang branch below parks this handler on the request context until
		// the ADAPTER's deadline fires, and holding stubMu across that would turn a scenario
		// about a hanging provider into a hanging suite - every later step and the reset that
		// closes this server would queue behind a handler waiting to be cancelled.
		s.stubMu.Lock()
		s.hits++
		s.gotQuery = r.URL.Query().Get("q")
		s.gotCount = r.URL.Query().Get("count")
		hang, status, rawBody, results := s.hang, s.status, s.rawBody, s.results
		s.stubMu.Unlock()
		if hang {
			// A response that never arrives: the adapter's own deadline must end this.
			<-r.Context().Done()
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if rawBody != "" {
			_, _ = w.Write([]byte(rawBody))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": results}})
	}))
	return s.srv.URL
}

// --- the step-side doors onto the knobs the handler shares --------------------------
//
// Every one of these is called from the test goroutine while a provider handler may be
// running - the connect-timeout scenario guarantees one is - so they take the same stubMu the
// handler takes. Reaching past them and touching a field directly is the defect this replaced.

func (s *searchState) setResults(r []map[string]any) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.results = r
}

func (s *searchState) prependResult(r map[string]any) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.results = append([]map[string]any{r}, s.results...)
}

func (s *searchState) setStatus(code int) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.status = code
}

func (s *searchState) setHang() {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.hang = true
}

func (s *searchState) setRawBody(b string) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.rawBody = b
}

func (s *searchState) hitCount() int {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return s.hits
}

// captured is the query and count the provider actually received on the wire.
func (s *searchState) captured() (query, count string) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return s.gotQuery, s.gotCount
}

// writeConfig writes the real search.json the adapter reads.
func (s *searchState) writeConfig(endpoint string) error {
	cfg := t_configDir(s.t)
	b, _ := json.Marshal(map[string]string{
		"provider": "brave",
		"key":      "BSA-test-key",
		"endpoint": endpoint,
	})
	return os.WriteFile(filepath.Join(cfg, "rogerai", "search.json"), b, 0o600)
}

// --- Background --------------------------------------------------------------

func (s *searchState) providerConfigured() error {
	return s.writeConfig(s.startProvider())
}

func (s *searchState) noProviderConfigured() error {
	cfg := t_configDir(s.t)
	return os.RemoveAll(filepath.Join(cfg, "rogerai", "search.json"))
}

// --- tool advertisement ------------------------------------------------------

func (s *searchState) loopStartsTurn() error {
	return nil // the assertions below read the real toolset
}

func findSearchTool() (Tool, bool) {
	for _, t := range BuiltinTools() {
		if t.Name == "web_search" {
			return t, true
		}
	}
	return Tool{}, false
}

func (s *searchState) toolsIncludeWebSearch() error {
	t, ok := findSearchTool()
	if !ok {
		return fmt.Errorf("web_search is not in the builtin toolset (retrieval is not implemented)")
	}
	if t.Mutating {
		return fmt.Errorf("web_search must be read-only (Mutating=false), it is marked mutating")
	}
	props, _ := t.Params["properties"].(map[string]any)
	q, _ := props["query"].(map[string]any)
	if q == nil || q["type"] != "string" {
		return fmt.Errorf("web_search needs a string 'query' parameter, got %v", props["query"])
	}
	req, _ := t.Params["required"].([]any)
	found := false
	for _, r := range req {
		if fmt.Sprint(r) == "query" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("'query' must be required, required=%v", req)
	}
	return nil
}

func (s *searchState) toolsIncludeCountParam() error {
	t, ok := findSearchTool()
	if !ok {
		return fmt.Errorf("web_search is not in the builtin toolset")
	}
	props, _ := t.Params["properties"].(map[string]any)
	c, _ := props["count"].(map[string]any)
	if c == nil || c["type"] != "integer" {
		return fmt.Errorf("web_search needs an integer 'count' parameter, got %v", props["count"])
	}
	req, _ := t.Params["required"].([]any)
	for _, r := range req {
		if fmt.Sprint(r) == "count" {
			return fmt.Errorf("'count' must be OPTIONAL, it is listed as required")
		}
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(c["description"])), fmt.Sprint(searchMaxCount)) {
		return fmt.Errorf("'count' description should state the %d cap, got %q", searchMaxCount, c["description"])
	}
	// The bound must be IN the schema, not only in prose the model may ignore.
	if c["maximum"] != searchMaxCount {
		return fmt.Errorf("'count' schema maximum = %v, want %d", c["maximum"], searchMaxCount)
	}
	if c["minimum"] != 1 {
		return fmt.Errorf("'count' schema minimum = %v, want 1", c["minimum"])
	}
	return nil
}

func (s *searchState) toolsExcludeWebSearch() error {
	if _, ok := findSearchTool(); ok {
		return fmt.Errorf("web_search is advertised with NO provider configured - the model would be offered a dead-end tool")
	}
	return nil
}

// --- auto-run through the real loop -------------------------------------------

func (s *searchState) modelEmitsSearchCall() error {
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		s.calls++
		if s.calls == 1 {
			return toolCall("s1", "web_search", `{"query":"valkey pubsub reconnect backoff"}`), nil
		}
		for _, m := range msgs {
			if m.Role == "tool" {
				s.toolResIn = m.Content
			}
		}
		return Message{Role: "assistant", Content: "answered from the results"}, nil
	}
	s.loop = NewLoop(s.root, "sys", complete, func(string, map[string]any) bool {
		s.confirms++
		return true
	})
	return nil
}

func (s *searchState) loopExecutesIt() error {
	s.final, s.loopErr = s.loop.Send(context.Background(), "what is the backoff?", nil)
	return s.loopErr
}

func (s *searchState) noConfirmShown() error {
	if s.confirms != 0 {
		return fmt.Errorf("web_search prompted for confirmation %d time(s) - read-only tools must auto-run", s.confirms)
	}
	return nil
}

func (s *searchState) resultsFedBack() error {
	if s.toolResIn == "" {
		return fmt.Errorf("the model never saw a tool result for web_search")
	}
	if !strings.Contains(s.toolResIn, "valkey.io") {
		return fmt.Errorf("the tool result fed back does not carry the provider's results: %q", s.toolResIn)
	}
	return nil
}

// --- direct tool calls ---------------------------------------------------------

func (s *searchState) callSearch(args map[string]any) error {
	t, ok := findSearchTool()
	if !ok {
		return fmt.Errorf("web_search is not in the builtin toolset (retrieval is not implemented)")
	}
	s.result, s.err = t.Run(context.Background(), s.root, args)
	return nil
}

func (s *searchState) callSearchQuery(q string) error {
	return s.callSearch(map[string]any{"query": q})
}

func (s *searchState) callSearchPlain() error {
	return s.callSearchQuery("a normal question")
}

// searchThroughLoop runs a full turn through the REAL loop so the query passes through
// every place a transcript could be persisted, not just the tool function.
func (s *searchState) searchThroughLoop() error {
	calls := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("s1", "web_search", `{"query":"a normal question"}`), nil
		}
		return Message{Role: "assistant", Content: "done"}, nil
	}
	s.loop = NewLoop(s.root, "sys", complete, nil)
	_, s.err = s.loop.Send(context.Background(), "please look that up", nil)
	return nil
}

func (s *searchState) callSearchCount(count string) error {
	// Seed more results than any cap so the BOUND is what limits the output, not supply.
	var seeded []map[string]any
	for i := 0; i < searchMaxCount*3; i++ {
		seeded = append(seeded, map[string]any{
			"title": fmt.Sprintf("result %d", i), "url": fmt.Sprintf("https://r%d.example/x", i),
			"description": "snippet",
		})
	}
	s.setResults(seeded)
	args := map[string]any{"query": "bounded count"}
	if strings.TrimSpace(count) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(count))
		if err != nil {
			return err
		}
		args["count"] = float64(n) // a real tool call arrives as JSON, so float64
	}
	return s.callSearch(args)
}

func (s *searchState) callSearchLongQuery() error {
	return s.callSearchQuery(strings.Repeat("x", searchMaxQuery+1))
}

func (s *searchState) resultsAreShaped() error { return s.callSearchQuery("shaping") }

// --- result-shape assertions ----------------------------------------------------

func (s *searchState) resultsInRankOrder() error {
	if s.err != nil {
		return fmt.Errorf("web_search errored: %v", s.err)
	}
	if gotQuery, _ := s.captured(); gotQuery != "valkey pubsub reconnect backoff" {
		return fmt.Errorf("the provider received query %q, want the model's query verbatim", gotQuery)
	}
	first := strings.Index(s.result, "valkey.io")
	second := strings.Index(s.result, "redis.io")
	third := strings.Index(s.result, "example.org")
	if first < 0 || second < 0 || third < 0 {
		return fmt.Errorf("not all provider results reached the model: %q", s.result)
	}
	if !(first < second && second < third) {
		return fmt.Errorf("results are not in provider rank order: %q", s.result)
	}
	return nil
}

func (s *searchState) eachResultHasTitleURLSnippet() error {
	for _, want := range []string{"Valkey pubsub reconnect", "https://valkey.io/topics/pubsub/", "reconnect and backoff"} {
		if !strings.Contains(s.result, want) {
			return fmt.Errorf("result text is missing %q (title / url / snippet): %q", want, s.result)
		}
	}
	return nil
}

func (s *searchState) atMostNResults(n int) error {
	if s.err != nil {
		return fmt.Errorf("web_search errored: %v", s.err)
	}
	// The bound is asked for on the wire too, not just trimmed after the fact.
	if _, gotCount := s.captured(); gotCount != fmt.Sprint(n) {
		return fmt.Errorf("the provider was asked for count=%q, want %d", gotCount, n)
	}
	got := strings.Count(s.result, "https://r")
	if got > n {
		return fmt.Errorf("returned %d results, want at most %d", got, n)
	}
	if got != n {
		return fmt.Errorf("returned %d results, want exactly %d (the bound)", got, n)
	}
	return nil
}

func (s *searchState) providerReturnsMarkedUpSnippet(tag string) error {
	s.setResults([]map[string]any{{
		"title": "Valkey", "url": "https://valkey.io/",
		"description": "Valkey is " + tag + "an open source" + strings.Replace(tag, "<", "</", 1) + " datastore",
	}})
	return s.callSearchQuery("valkey")
}

func (s *searchState) snippetIsPlainText() error {
	if s.err != nil {
		return fmt.Errorf("web_search errored: %v", s.err)
	}
	if !strings.Contains(s.result, "Valkey is an open source datastore") {
		return fmt.Errorf("the snippet text did not survive stripping: %q", s.result)
	}
	return nil
}

func (s *searchState) noMarkupSurvives() error {
	for _, bad := range []string{"<strong>", "</strong>", "<", ">"} {
		if strings.Contains(s.result, bad) {
			return fmt.Errorf("markup %q reached the model: %q", bad, s.result)
		}
	}
	return nil
}

func (s *searchState) providerReturnsEscapedMarkup(esc string) error {
	s.setResults([]map[string]any{{
		"title": "Valkey", "url": "https://valkey.io/",
		"description": "Valkey is " + esc + "fast" + strings.Replace(esc, "&lt;", "&lt;/", 1),
	}})
	return s.callSearchQuery("escaped")
}

func (s *searchState) providerReturnsEntities(a, b string) error {
	s.setResults([]map[string]any{{
		"title": "Ben " + a + " Jerry" + b + "s", "url": "https://x.example/", "description": "snip",
	}})
	return s.callSearchQuery("entities")
}

func (s *searchState) entitiesDecoded() error {
	if !strings.Contains(s.result, "Ben & Jerry's") {
		return fmt.Errorf("entities were not decoded: %q", s.result)
	}
	return nil
}

func (s *searchState) providerReturnsURL(u string) error {
	s.prependResult(map[string]any{"title": "bad scheme", "url": u, "description": "should be dropped"})
	return nil
}

func (s *searchState) badResultDropped() error {
	if strings.Contains(s.result, "ftp://") {
		return fmt.Errorf("a non-http(s) result reached the model: %q", s.result)
	}
	return nil
}

func (s *searchState) onlyHTTPResults() error {
	for _, line := range strings.Split(s.result, "\n") {
		i := strings.Index(line, "://")
		if i < 0 {
			continue
		}
		scheme := line[:i]
		if j := strings.LastIndexAny(scheme, " \t"); j >= 0 {
			scheme = scheme[j+1:]
		}
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("result line carries a %q URL, only http(s) may reach the model: %q", scheme, line)
		}
	}
	return nil
}

func (s *searchState) resultsExceedCap() error {
	var huge []map[string]any
	for i := 0; i < searchMaxCount; i++ {
		huge = append(huge, map[string]any{
			"title":       fmt.Sprintf("huge %d", i),
			"url":         fmt.Sprintf("https://r%d.example/x", i),
			"description": strings.Repeat("long snippet ", 400),
		})
	}
	s.setResults(huge)
	return s.callSearchQuery("huge")
}

func (s *searchState) clippedAndMarked() error {
	if len(s.result) > maxToolOutput+64 {
		return fmt.Errorf("result is %d bytes, want clipped to ~%d", len(s.result), maxToolOutput)
	}
	if !strings.Contains(s.result, "truncated") {
		return fmt.Errorf("a clipped result must be marked truncated: %q", tail(s.result))
	}
	return nil
}

// --- failure handling ------------------------------------------------------------

func (s *searchState) providerRespondsWith(failure string) error {
	switch {
	case strings.Contains(failure, "500"):
		s.setStatus(http.StatusInternalServerError)
	case strings.Contains(failure, "429"):
		s.setStatus(http.StatusTooManyRequests)
	case strings.Contains(failure, "timeout"):
		s.setHang()
		searchTimeout = 300 * time.Millisecond // the seam, restored in reset()
	case strings.Contains(failure, "malformed"):
		s.setRawBody(`{"web":{"results":[{"title":`)
	default:
		return fmt.Errorf("unknown provider failure %q", failure)
	}
	return nil
}

func (s *searchState) providerResponds429() error { return s.providerRespondsWith("HTTP 429") }

func (s *searchState) loopDoesNotAbort() error {
	if s.err != nil {
		return fmt.Errorf("a provider failure must be returned to the MODEL as a tool result, not as a loop error: %v", s.err)
	}
	return nil
}

func (s *searchState) resultStatesFailure() error {
	low := strings.ToLower(s.result)
	if !strings.Contains(low, "search failed") {
		return fmt.Errorf("the tool result must say the search failed and why, got %q", s.result)
	}
	return nil
}

func (s *searchState) modelCanContinue() error {
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		s.calls++
		if s.calls == 1 {
			return toolCall("s1", "web_search", `{"query":"anything"}`), nil
		}
		return Message{Role: "assistant", Content: "answered without sources"}, nil
	}
	s.loop = NewLoop(s.root, "sys", complete, nil)
	final, err := s.loop.Send(context.Background(), "q", nil)
	if err != nil {
		return fmt.Errorf("the turn aborted on a search failure: %v", err)
	}
	if final == "" {
		return fmt.Errorf("the turn produced no answer after a search failure")
	}
	return nil
}

func (s *searchState) exactlyOneProviderRequest() error {
	if n := s.hitCount(); n != 1 {
		return fmt.Errorf("made %d provider requests for one call, want exactly 1 (no retry against a rate-limited provider)", n)
	}
	return nil
}

func (s *searchState) providerReturnsZero() error {
	s.setResults([]map[string]any{})
	return s.callSearchQuery("nothing matches this")
}

func (s *searchState) resultSaysNoResults() error {
	if !strings.Contains(strings.ToLower(s.result), "no results") {
		return fmt.Errorf("an empty result set must say so plainly, got %q", s.result)
	}
	return nil
}

func (s *searchState) notPresentedAsFailure() error {
	if s.err != nil {
		return fmt.Errorf("zero results is not an error, got %v", s.err)
	}
	if strings.Contains(strings.ToLower(s.result), "failed") {
		return fmt.Errorf("zero results must not read as a failure: %q", s.result)
	}
	return nil
}

func (s *searchState) errorNamesEmptyQuery() error {
	if s.err == nil && !strings.Contains(strings.ToLower(s.result), "empty query") {
		return fmt.Errorf("an empty query must be a clear tool error, got result %q err %v", s.result, s.err)
	}
	if s.err != nil && !strings.Contains(strings.ToLower(s.err.Error()), "empty query") {
		return fmt.Errorf("the error should name the empty query, got %v", s.err)
	}
	if n := s.hitCount(); n != 0 {
		return fmt.Errorf("an empty query reached the provider (%d requests)", n)
	}
	return nil
}

// loopContinues proves the recoverability claim against the REAL loop: a rejected
// web_search call comes back as a tool result the model answers around, not a dead turn.
func (s *searchState) loopContinues() error {
	calls := 0
	sawToolResult := false
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("s1", "web_search", `{"query":""}`), nil
		}
		for _, m := range msgs {
			if m.Role == "tool" && strings.TrimSpace(m.Content) != "" {
				sawToolResult = true
			}
		}
		return Message{Role: "assistant", Content: "answered without searching"}, nil
	}
	loop := NewLoop(s.root, "sys", complete, nil)
	final, err := loop.Send(context.Background(), "q", nil)
	if err != nil {
		return fmt.Errorf("a rejected web_search aborted the turn: %v", err)
	}
	if final == "" {
		return fmt.Errorf("the turn produced no answer after a rejected web_search")
	}
	if !sawToolResult {
		return fmt.Errorf("the model was never told why the search was rejected")
	}
	return nil
}

func (s *searchState) errorNamesCap() error {
	msg := s.result
	if s.err != nil {
		msg = s.err.Error()
	}
	if !strings.Contains(msg, fmt.Sprint(searchMaxQuery)) {
		return fmt.Errorf("the over-long-query error should name the %d cap, got %q", searchMaxQuery, msg)
	}
	return nil
}

func (s *searchState) noProviderRequest() error {
	if n := s.hitCount(); n != 0 {
		return fmt.Errorf("%d provider requests were made, want 0 (rejected before the wire)", n)
	}
	return nil
}

// --- transience -------------------------------------------------------------------

func (s *searchState) queryNotOnDisk() error {
	cfg := t_configDir(s.t)
	var found []string
	for _, dir := range []string{cfg, s.root, os.Getenv("HOME")} {
		if dir == "" {
			continue
		}
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && strings.Contains(string(b), "a normal question") {
				found = append(found, p)
			}
			return nil
		})
	}
	if len(found) > 0 {
		return fmt.Errorf("the query was written to disk at %v", found)
	}
	return nil
}

// queryInTranscriptOnly confirms the query IS in the live transcript (so the assertion
// above is about persistence, not about the query having silently vanished).
func (s *searchState) queryInTranscriptOnly() error {
	if s.err != nil {
		return fmt.Errorf("the turn errored: %v", s.err)
	}
	if s.loop == nil {
		return fmt.Errorf("no loop ran")
	}
	for _, m := range s.loop.messages {
		if strings.Contains(m.Content, "a normal question") {
			return nil
		}
		for _, tc := range m.ToolCalls {
			if strings.Contains(tc.Function.Arguments, "a normal question") {
				return nil
			}
		}
	}
	return fmt.Errorf("the query is not in the in-memory transcript at all")
}

// tail returns the last 200 bytes of s for error messages.
func tail(s string) string {
	if len(s) <= 200 {
		return s
	}
	return "..." + s[len(s)-200:]
}

func TestWebSearchBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &searchState{t: t}
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

			sc.Step(`^a search provider is configured with a valid key$`, st.providerConfigured)
			sc.Step(`^no search provider is configured$`, st.noProviderConfigured)
			sc.Step(`^the agent loop starts a turn$`, st.loopStartsTurn)
			sc.Step(`^the tools array includes "web_search" with a required "query" string parameter$`, st.toolsIncludeWebSearch)
			sc.Step(`^an optional bounded "count" integer parameter$`, st.toolsIncludeCountParam)
			sc.Step(`^"web_search" is absent from the tools array$`, st.toolsExcludeWebSearch)

			sc.Step(`^the model emits a web_search tool call$`, st.modelEmitsSearchCall)
			sc.Step(`^the loop executes it$`, st.loopExecutesIt)
			sc.Step(`^no confirm prompt is shown \(read-only tools auto-run\)$`, st.noConfirmShown)
			sc.Step(`^the results are fed back to the model as the tool result$`, st.resultsFedBack)

			sc.Step(`^the model calls web_search with query "([^"]*)"$`, st.callSearchQuery)
			sc.Step(`^the tool result lists results in provider rank order$`, st.resultsInRankOrder)
			sc.Step(`^each result carries a title, an http\(s\) url, and a snippet$`, st.eachResultHasTitleURLSnippet)
			sc.Step(`^the model calls web_search with count ?(\d*)$`, st.callSearchCount)
			sc.Step(`^at most (\d+) results are returned$`, st.atMostNResults)

			sc.Step(`^the provider returns a snippet marked up with "([^"]*)" around the match$`, st.providerReturnsMarkedUpSnippet)
			sc.Step(`^the snippet reaches the model as plain text$`, st.snippetIsPlainText)
			sc.Step(`^no markup tags survive$`, st.noMarkupSurvives)
			sc.Step(`^the provider returns a snippet containing "([^"]*)"$`, st.providerReturnsEscapedMarkup)
			sc.Step(`^the provider returns a title containing "([^"]*)" and "([^"]*)"$`, st.providerReturnsEntities)
			sc.Step(`^the title reaches the model with those characters decoded$`, st.entitiesDecoded)
			sc.Step(`^the provider returns a result with url "([^"]*)"$`, st.providerReturnsURL)
			sc.Step(`^the results are shaped$`, st.resultsAreShaped)
			sc.Step(`^that result is dropped$`, st.badResultDropped)
			sc.Step(`^only http:// and https:// results reach the model$`, st.onlyHTTPResults)

			sc.Step(`^the shaped results exceed the tool-output cap$`, st.resultsExceedCap)
			sc.Step(`^the result is clipped to maxToolOutput and marked truncated$`, st.clippedAndMarked)

			sc.Step(`^the search provider responds with (.+)$`, st.providerRespondsWith)
			sc.Step(`^the model calls web_search$`, st.callSearchPlain)
			sc.Step(`^the loop does not abort$`, st.loopDoesNotAbort)
			sc.Step(`^the tool result states the search failed and why$`, st.resultStatesFailure)
			sc.Step(`^the model can continue the turn and answer without sources$`, st.modelCanContinue)
			sc.Step(`^the search provider responds 429$`, st.providerResponds429)
			sc.Step(`^exactly one provider request is made for that call$`, st.exactlyOneProviderRequest)

			sc.Step(`^the provider returns zero results$`, st.providerReturnsZero)
			sc.Step(`^the tool result says no results were found for the query$`, st.resultSaysNoResults)
			sc.Step(`^it is not presented as a failure$`, st.notPresentedAsFailure)

			sc.Step(`^the tool result is an error naming the empty query$`, st.errorNamesEmptyQuery)
			sc.Step(`^the loop continues$`, st.loopContinues)
			sc.Step(`^the model calls web_search with a query longer than the query cap$`, st.callSearchLongQuery)
			sc.Step(`^the tool result is an error naming the cap$`, st.errorNamesCap)
			sc.Step(`^no provider request is made$`, st.noProviderRequest)

			sc.Step(`^the model calls web_search with a query$`, st.searchThroughLoop)
			sc.Step(`^the query is not written to any file on disk$`, st.queryNotOnDisk)
			sc.Step(`^it lives only in the in-memory transcript$`, st.queryInTranscriptOnly)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/answers/web_search.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("web_search behavior scenarios failed (see godog output above)")
	}
}
