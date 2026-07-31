package harness

// citations_bdd_test.go makes features/answers/citations.feature EXECUTABLE against the
// REAL loop. The load-bearing invariant under test: the sources list is DERIVED from the
// executed tool log (SourcesFrom over the turn's messages), never from URLs the model
// writes in its prose. Every scenario therefore drives a real Loop with a scripted model,
// real tools, and real local content servers - a scripted model is the ONLY way to make
// the model hallucinate a citation on demand, which is the whole point of scenario 2.
//
// The fetch address guard is real; loopback is permitted through the fetchVetIP seam so
// httptest servers can stand in for public pages (see fetch_hardening_bdd_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v5/internal/capsule"
)

type citeState struct {
	t *testing.T

	srv     *httptest.Server
	pages   map[string]string // path -> body
	fetched []string          // URLs the model was scripted to fetch

	loop          *Loop
	eventsSeen    []Event
	redirectTo    string
	searchResults string
	final         string
	err           error
	sources       []source

	turn1, turn2 []source

	origVet func(net.IP) error
}

func (s *citeState) reset() {
	s.pages = map[string]string{}
	s.fetched = nil
	s.loop, s.final, s.err, s.sources = nil, "", nil, nil
	s.searchResults, s.redirectTo, s.eventsSeen = "", "", nil
	s.turn1, s.turn2 = nil, nil
	s.origVet = fetchVetIP
	fetchVetIP = allowLoopbackVet
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" && s.redirectTo != "" {
			http.Redirect(w, r, s.redirectTo, http.StatusFound)
			return
		}
		body, ok := s.pages[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html><body>no such page</body></html>"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
}

func (s *citeState) restore() {
	fetchVetIP = s.origVet
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

// page registers a served page and returns its absolute URL.
func (s *citeState) page(path, title, body string) string {
	s.pages[path] = fmt.Sprintf("<html><head><title>%s</title></head><body><p>%s</p></body></html>", title, body)
	return s.srv.URL + path
}

// runScript drives a real turn: each entry is one scripted model reply, the last being the
// final answer text.
func (s *citeState) runScript(finalText string, calls [][2]string) error {
	step := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		if step < len(calls) {
			c := calls[step]
			step++
			return toolCall(fmt.Sprintf("c%d", step), c[0], c[1]), nil
		}
		return Message{Role: "assistant", Content: finalText}, nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	s.final, s.err = s.loop.Send(context.Background(), "what is the backoff?", func(e Event) {
		if e.Kind == EventToolResult {
			s.eventsSeen = append(s.eventsSeen, e)
		}
	})
	s.sources = s.loop.sources()
	return s.err
}

// fetchArgs renders a web_fetch tool-call argument JSON for url.
func fetchArgs(url string) string { return fmt.Sprintf(`{"url":%q}`, url) }

// capsuleRoundTrip carries a turn through the REAL roger.context.v1 wire shape and reads
// it back. This is the load-bearing check behind the spec's "no new capsule field" claim:
// a capsule stores a tool call FLAT (name + arguments + result) inside the assistant turn,
// so what must survive is the pairing of web_fetch with its wrapped result - which is
// exactly the citation record. If the capsule ever stopped carrying tool results, this
// scenario is what would notice.
func capsuleRoundTrip(t *testing.T, in []Message) []Message {
	t.Helper()
	// harness messages -> capsule messages (results folded into their originating call).
	results := map[string]string{}
	for _, m := range in {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	var cmsgs []capsule.Message
	for _, m := range in {
		if m.Role == "tool" {
			continue // carried inside its call
		}
		cm := capsule.Message{Role: m.Role, Content: m.Content}
		if len(m.ToolCalls) > 0 {
			flat := make([]capsule.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				c := capsule.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
				if res, ok := results[tc.ID]; ok {
					c.Result = &res
				}
				flat = append(flat, c)
			}
			cm.ToolCalls = capsule.ToolCallsRaw(flat)
		}
		cmsgs = append(cmsgs, cm)
	}

	b, err := json.Marshal(cmsgs)
	if err != nil {
		t.Fatalf("marshal capsule messages: %v", err)
	}
	var back []capsule.Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal capsule messages: %v", err)
	}

	// capsule messages -> harness messages, the way an importer would rebuild the log.
	var out []Message
	for _, cm := range back {
		out = append(out, Message{Role: cm.Role, Content: cm.Content})
		if len(cm.ToolCalls) == 0 {
			continue
		}
		var flat []capsule.ToolCall
		if err := json.Unmarshal(cm.ToolCalls, &flat); err != nil {
			t.Fatalf("unmarshal tool_calls: %v", err)
		}
		for _, tc := range flat {
			if tc.Result == nil {
				continue
			}
			out = append(out, Message{Role: "tool", Name: tc.Name, ToolCallID: tc.ID, Content: *tc.Result})
		}
	}
	return out
}

// --- scenario 1: an answer built on retrievals -------------------------------

func (s *citeState) turnSearchedThenFetchedTwo() error {
	a := s.page("/a", "Backoff A", "retry with jitter")
	b := s.page("/b", "Backoff B", "resubscribe after a drop")
	s.fetched = []string{a, b}
	// The search result is the loop's own record of which URL had which title.
	results := renderResults([]searchResult{
		{Title: "Backoff A", URL: a, Snippet: "retry"},
		{Title: "Backoff B", URL: b, Snippet: "resubscribe"},
	})
	s.pages["/__search"] = results
	return s.runScriptWithSearch("here is the answer", results, [][2]string{
		{"web_fetch", fetchArgs(a)},
		{"web_fetch", fetchArgs(b)},
	})
}

// runScriptWithSearch scripts a web_search call whose result is served by a stub provider,
// then the given follow-up calls.
func (s *citeState) runScriptWithSearch(finalText, searchResults string, calls [][2]string) error {
	step := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		if step == 0 {
			step++
			return toolCall("s1", "web_search", `{"query":"backoff"}`), nil
		}
		if step-1 < len(calls) {
			c := calls[step-1]
			step++
			return toolCall(fmt.Sprintf("c%d", step), c[0], c[1]), nil
		}
		return Message{Role: "assistant", Content: finalText}, nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	s.loop.MaxSteps = 12
	// The search tool is only advertised when configured; inject the rendered results
	// directly as the tool's output so the citation derivation is what is under test here
	// (the adapter itself is covered by the web_search suite).
	s.loop.toolByName["web_search"] = Tool{
		Name: "web_search",
		Run:  func(context.Context, string, map[string]any) (string, error) { return searchResults, nil },
	}
	s.loop.tools = append(s.loop.tools, s.loop.toolByName["web_search"])
	s.final, s.err = s.loop.Send(context.Background(), "what is the backoff?", nil)
	s.sources = s.loop.sources()
	return s.err
}

func (s *citeState) modelReturnsFinal() error { return s.err }

func (s *citeState) answerCarriesSourcesBlock() error {
	if len(s.sources) != 2 {
		return fmt.Errorf("derived %d sources, want 2: %+v", len(s.sources), s.sources)
	}
	for i, want := range s.fetched {
		if s.sources[i].URL != want {
			return fmt.Errorf("source %d = %q, want %q", i+1, s.sources[i].URL, want)
		}
	}
	// The TITLES must come from the search results, not the host fallback: asserting only
	// "non-empty" would pass even with the title derivation deleted.
	for i, want := range []string{"Backoff A", "Backoff B"} {
		if s.sources[i].Title != want {
			return fmt.Errorf("source %d title = %q, want the search result's title %q", i+1, s.sources[i].Title, want)
		}
		if !strings.Contains(s.final, want) {
			return fmt.Errorf("the rendered block omits the title %q:\n%s", want, s.final)
		}
	}
	// The block must reach the user, not just the accessor.
	if !strings.Contains(s.final, "Sources:") {
		return fmt.Errorf("the final answer carries no sources block: %q", s.final)
	}
	for _, want := range s.fetched {
		if !strings.Contains(s.final, want) {
			return fmt.Errorf("the sources block omits %q: %q", want, s.final)
		}
	}
	return nil
}

// --- scenario 2: a hallucinated URL is not a source ---------------------------

func (s *citeState) turnFetchedTwo(a, b string) error {
	// The scenario names example.com URLs; serve them locally and remember the mapping so
	// the assertion can talk about the names the spec uses.
	ua := s.page("/x", "Page A", "alpha")
	ub := s.page("/y", "Page B", "beta")
	s.fetched = []string{ua, ub}
	return nil
}

func (s *citeState) modelCitesInvented(invented string) error {
	return s.runScript("see "+invented+" for details", [][2]string{
		{"web_fetch", fetchArgs(s.fetched[0])},
		{"web_fetch", fetchArgs(s.fetched[1])},
	})
}

func (s *citeState) sourcesAreExactlyTheFetched() error {
	if len(s.sources) != 2 {
		return fmt.Errorf("derived %d sources, want exactly the 2 fetched: %+v", len(s.sources), s.sources)
	}
	for i, want := range s.fetched {
		if s.sources[i].URL != want {
			return fmt.Errorf("source %d = %q, want %q", i+1, s.sources[i].URL, want)
		}
	}
	return nil
}

func (s *citeState) inventedNotInBlock(invented string) error {
	for _, src := range s.sources {
		if src.URL == invented {
			return fmt.Errorf("a URL the model invented became a source: %q", invented)
		}
	}
	if strings.Contains(sourcesBlock(s.sources), invented) {
		return fmt.Errorf("the rendered block carries the invented URL %q", invented)
	}
	return nil
}

// --- scenario 3: a failed retrieval is not a source ---------------------------

func (s *citeState) turnWithFailedFetches() error {
	ok := s.page("/good", "Good Page", "the answer")
	missing := s.srv.URL + "/gone"        // 404
	blocked := "http://169.254.169.254/x" // refused by the guard
	s.fetched = []string{ok}
	return s.runScript("answered", [][2]string{
		{"web_fetch", fetchArgs(missing)},
		{"web_fetch", fetchArgs(blocked)},
		{"web_fetch", fetchArgs(ok)},
	})
}

func (s *citeState) oneFetchSucceeded() error { return nil }

func (s *citeState) onlySuccessfulListed() error {
	if len(s.sources) != 1 {
		return fmt.Errorf("derived %d sources, want only the successful fetch: %+v", len(s.sources), s.sources)
	}
	if s.sources[0].URL != s.fetched[0] {
		return fmt.Errorf("source = %q, want the successful fetch %q", s.sources[0].URL, s.fetched[0])
	}
	return nil
}

// --- scenario 4: unfetched search results are not sources ---------------------

func (s *citeState) searchedFiveFetchedTwo() error {
	var rs []searchResult
	for i := 0; i < 5; i++ {
		u := s.page(fmt.Sprintf("/r%d", i), fmt.Sprintf("Result %d", i), "body")
		rs = append(rs, searchResult{Title: fmt.Sprintf("Result %d", i), URL: u, Snippet: "snip"})
	}
	s.fetched = []string{rs[1].URL, rs[3].URL}
	return s.runScriptWithSearch("answered", renderResults(rs), [][2]string{
		{"web_fetch", fetchArgs(rs[1].URL)},
		{"web_fetch", fetchArgs(rs[3].URL)},
	})
}

func (s *citeState) onlyFetchedListed() error {
	if len(s.sources) != 2 {
		return fmt.Errorf("derived %d sources, want the 2 fetched: %+v", len(s.sources), s.sources)
	}
	for i, want := range s.fetched {
		if s.sources[i].URL != want {
			return fmt.Errorf("source %d = %q, want %q", i+1, s.sources[i].URL, want)
		}
	}
	return nil
}

// --- scenario 5: no retrievals, no block --------------------------------------

func (s *citeState) turnWithoutRetrieval() error {
	return s.runScript("just answered from what I know", nil)
}

func (s *citeState) noSourcesBlock() error {
	if len(s.sources) != 0 {
		return fmt.Errorf("a turn with no retrievals derived %d sources", len(s.sources))
	}
	if strings.Contains(s.final, "Sources:") {
		return fmt.Errorf("a sources block was rendered with nothing retrieved: %q", s.final)
	}
	return nil
}

// --- ordering, dedup, per-turn reset ------------------------------------------

func (s *citeState) fetchesInOrderBThenA() error {
	b := s.page("/b", "Bee", "beta")
	a := s.page("/a", "Ay", "alpha")
	s.fetched = []string{b, a}
	return s.runScript("answered", [][2]string{
		{"web_fetch", fetchArgs(b)},
		{"web_fetch", fetchArgs(a)},
	})
}

func (s *citeState) listedInRetrievalOrder() error {
	if len(s.sources) != 2 {
		return fmt.Errorf("derived %d sources, want 2", len(s.sources))
	}
	block := sourcesBlock(s.sources)
	first, second := strings.Index(block, s.fetched[0]), strings.Index(block, s.fetched[1])
	if first < 0 || second < 0 || first > second {
		return fmt.Errorf("sources are not in first-retrieval order:\n%s", block)
	}
	if !strings.Contains(block, "[1]") || !strings.Contains(block, "[2]") {
		return fmt.Errorf("sources are not numbered:\n%s", block)
	}
	return nil
}

func (s *citeState) fetchedTwiceInOneTurn(_ string) error {
	u := s.page("/dup", "Dup", "same page")
	s.fetched = []string{u}
	return s.runScript("answered", [][2]string{
		{"web_fetch", fetchArgs(u)},
		{"web_fetch", fetchArgs(u)},
	})
}

func (s *citeState) listedExactlyOnce() error {
	if len(s.sources) != 1 {
		return fmt.Errorf("the same URL fetched twice produced %d sources: %+v", len(s.sources), s.sources)
	}
	if n := strings.Count(sourcesBlock(s.sources), s.fetched[0]); n != 1 {
		return fmt.Errorf("the URL appears %d times in the block, want 1", n)
	}
	return nil
}

func (s *citeState) twoTurnsFetchedDifferentPages() error {
	a := s.page("/a", "Ay", "alpha")
	b := s.page("/b", "Bee", "beta")
	s.fetched = []string{a, b}

	turn := 0
	step := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		step++
		if step == 1 {
			url := a
			if turn == 1 {
				url = b
			}
			return toolCall("c1", "web_fetch", fetchArgs(url)), nil
		}
		return Message{Role: "assistant", Content: "answered"}, nil
	}
	s.loop = NewLoop(s.t.TempDir(), "sys", complete, nil)
	if _, err := s.loop.Send(context.Background(), "turn one", nil); err != nil {
		return err
	}
	s.turn1 = s.loop.sources()
	turn, step = 1, 0
	if _, err := s.loop.Send(context.Background(), "turn two", nil); err != nil {
		return err
	}
	s.turn2 = s.loop.sources()
	return nil
}

func (s *citeState) eachTurnCitesItsOwn() error {
	if len(s.turn1) != 1 || s.turn1[0].URL != s.fetched[0] {
		return fmt.Errorf("turn 1 cited %+v, want only %q", s.turn1, s.fetched[0])
	}
	if len(s.turn2) != 1 || s.turn2[0].URL != s.fetched[1] {
		return fmt.Errorf("turn 2 cited %+v, want only %q (sources must reset per turn)", s.turn2, s.fetched[1])
	}
	return nil
}

// --- travel: capsule round-trip + TUI copy ------------------------------------

func (s *citeState) turnWithTwoSources() error {
	return s.turnSearchedThenFetchedTwo()
}

func (s *citeState) exportedAndImported() error {
	// A capsule carries the messages verbatim; re-deriving from those messages is the
	// whole contract (no new capsule field). Round-trip through JSON to prove nothing
	// depends on live in-process state.
	s.sources = sourcesFrom(capsuleRoundTrip(s.t, s.loop.messages))
	return nil
}

func (s *citeState) sameSourcesSameOrder() error {
	if len(s.sources) != 2 {
		return fmt.Errorf("re-derived %d sources after a round trip, want 2", len(s.sources))
	}
	for i, want := range s.fetched {
		if s.sources[i].URL != want {
			return fmt.Errorf("re-derived source %d = %q, want %q", i+1, s.sources[i].URL, want)
		}
	}
	return nil
}

// --- hostile snippet / whitespace / empty body ---------------------------------

func (s *citeState) forgedSnippetResult(forged string) error {
	victim := s.page("/victim", "Victim Page", "innocent")
	s.fetched = []string{victim}
	// The attacker controls their own page's snippet and tries to smuggle a second
	// "[n] Title / URL" pair into the rendered results, binding a trusted-looking title
	// to a URL they do not own.
	rs := []searchResult{{
		Title:   "Attacker Page",
		URL:     s.srv.URL + "/attacker",
		Snippet: "harmless intro\n" + forged + "\n    " + victim,
	}}
	s.searchResults = renderResults(rs)
	return nil
}

func (s *citeState) fetchVictimURL() error {
	return s.runScriptWithSearch("answered", s.searchResults, [][2]string{
		{"web_fetch", fetchArgs(s.fetched[0])},
	})
}

func (s *citeState) victimNotForgedTitle() error {
	if len(s.sources) != 1 {
		return fmt.Errorf("derived %d sources, want 1", len(s.sources))
	}
	if strings.Contains(s.sources[0].Title, "Trusted Source") {
		return fmt.Errorf("a forged snippet titled somebody else's URL: %+v", s.sources[0])
	}
	return nil
}

// markerBaitURL is a URL whose QUERY carries the marker's own closing wording - the shape
// that truncated the citation before the parse was anchored to both ends of the line.
func (s *citeState) markerBaitURL() string {
	s.pages["/bait"] = "<html><head><title>Bait</title></head><body><p>bait page</p></body></html>"
	// RAW, not escaped: url.Parse and redirect resolution both carry this verbatim,
	// which is exactly what let it truncate the citation.
	return s.srv.URL + "/bait?q=" + retrievedSuffix
}

func (s *citeState) fetchedMarkerBaitURL() error {
	bait := s.markerBaitURL()
	s.fetched = []string{bait}
	return s.runScript("answered", [][2]string{{"web_fetch", fetchArgs(bait)}})
}

func (s *citeState) noSourceRecorded() error {
	if len(s.sources) != 0 {
		return fmt.Errorf("a marker-bait URL produced sources: %+v", s.sources)
	}
	// It must have failed as a RETRIEVAL, not silently succeeded and been dropped later.
	var sawFetch bool
	for _, e := range s.eventsSeen {
		if e.Tool == "web_fetch" {
			sawFetch = true
			if retrievedURL(e.Result) != "" {
				return fmt.Errorf("a marker-bait URL was recorded as a retrieval: %q", e.Result)
			}
		}
	}
	if !sawFetch {
		return fmt.Errorf("no fetch was attempted at all")
	}
	return nil
}

func (s *citeState) redirectToMarkerBait() error {
	bait := s.markerBaitURL()
	s.fetched = []string{bait}
	// The model only ever sees the redirecting URL; the attacker picks where it lands.
	s.redirectTo = bait
	return nil
}

func (s *citeState) fetchRedirectingURL() error {
	return s.runScript("answered", [][2]string{{"web_fetch", fetchArgs(s.srv.URL + "/go")}})
}

func (s *citeState) fetchedWithStrayWhitespace(u string) error {
	page := s.page("/ws", "Whitespace", "same page")
	s.fetched = []string{page}
	return s.runScript("answered", [][2]string{
		{"web_fetch", fetchArgs(page)},
		{"web_fetch", fetchArgs(page + " ")},
	})
}

func (s *citeState) fetchReturnedEmpty() error {
	s.pages["/empty"] = ""
	return s.runScript("answered", [][2]string{
		{"web_fetch", fetchArgs(s.srv.URL + "/empty")},
	})
}

func TestCitationsBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &citeState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.restore()
				return ctx, err
			})

			sc.Step(`^a turn in which the model called web_search and then web_fetch on 2 result URLs$`, st.turnSearchedThenFetchedTwo)
			sc.Step(`^the model returns its final answer$`, st.modelReturnsFinal)
			sc.Step(`^the answer carries a sources block listing those 2 fetched URLs with their titles$`, st.answerCarriesSourcesBlock)

			sc.Step(`^a turn whose only fetches were "([^"]*)" and "([^"]*)"$`, st.turnFetchedTwo)
			sc.Step(`^the model's final text cites "([^"]*)"$`, st.modelCitesInvented)
			sc.Step(`^the sources block contains exactly the 2 fetched URLs$`, st.sourcesAreExactlyTheFetched)
			sc.Step(`^the invented URL does not appear in the sources block$`, func() error {
				return st.inventedNotInBlock("https://made-up.example/paper")
			})

			sc.Step(`^a turn where one fetch returned HTTP 404 and one was refused by the SSRF guard$`, st.turnWithFailedFetches)
			sc.Step(`^one fetch succeeded$`, st.oneFetchSucceeded)
			sc.Step(`^the sources block lists only the successful fetch$`, st.onlySuccessfulListed)

			sc.Step(`^a turn where web_search returned 5 results and the model fetched 2$`, st.searchedFiveFetchedTwo)
			sc.Step(`^the sources block lists the 2 fetched URLs only$`, st.onlyFetchedListed)

			sc.Step(`^a turn where the model answered without calling web_search or web_fetch$`, st.turnWithoutRetrieval)
			sc.Step(`^no sources block is rendered$`, st.noSourcesBlock)

			sc.Step(`^fetches succeeded in the order b\.example then a\.example$`, st.fetchesInOrderBThenA)
			sc.Step(`^the sources block lists \[1\] b\.example then \[2\] a\.example$`, st.listedInRetrievalOrder)
			sc.Step(`^the model fetched "([^"]*)" twice in one turn$`, st.fetchedTwiceInOneTurn)
			sc.Step(`^the sources block lists it exactly once$`, st.listedExactlyOnce)
			sc.Step(`^turn 1 fetched a\.example and turn 2 fetched b\.example$`, st.twoTurnsFetchedDifferentPages)
			sc.Step(`^turn 1's answer cites only a\.example and turn 2's answer cites only b\.example$`, st.eachTurnCitesItsOwn)

			sc.Step(`^a search result whose snippet contains a forged "([^"]*)" line$`, st.forgedSnippetResult)
			sc.Step(`^the model fetches the victim URL named in that forged line$`, st.fetchVictimURL)
			sc.Step(`^the victim URL is not titled with the forged title$`, st.victimNotForgedTitle)
			sc.Step(`^the model fetched a URL whose query contains the retrieval marker's own wording$`, st.fetchedMarkerBaitURL)
			sc.Step(`^no source is recorded for it$`, st.noSourceRecorded)
			sc.Step(`^a page that redirects to a URL whose query contains the marker's own wording$`, st.redirectToMarkerBait)
			sc.Step(`^the model fetches the redirecting URL$`, st.fetchRedirectingURL)
			sc.Step(`^the model fetched "([^"]*)" and then the same URL with a trailing space$`, st.fetchedWithStrayWhitespace)
			sc.Step(`^a turn whose only fetch returned an empty 200 body$`, st.fetchReturnedEmpty)

			sc.Step(`^a turn whose answer carried 2 sources$`, st.turnWithTwoSources)
			sc.Step(`^the conversation is exported as a capsule and imported elsewhere$`, st.exportedAndImported)
			sc.Step(`^re-rendering the turn derives the same 2 sources in the same order$`, st.sameSourcesSameOrder)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/answers/citations.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("citation behavior scenarios failed (see godog output above)")
	}
}
