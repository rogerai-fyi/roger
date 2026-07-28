package harness

// answers_live_e2e_test.go is the LIVE end-to-end check for answers mode, run against the
// REAL broker, a REAL band on the market, and the REAL public web. It is gated behind
// ANSWERS_E2E=1 so it NEVER runs in CI or a normal `go test` (no external dependency, and
// no spend, in the default suite):
//
//	ANSWERS_E2E=1 go test ./internal/harness/ -run TestAnswersLiveE2E -v
//
// It exists because the house rule is that a live run catches what the suites cannot: the
// BDD suites script the model, so they prove the HARNESS holds - they cannot prove a real
// model actually drives the retrieval tools, that a real page survives extraction into
// something answerable, or that the citation block reflects the page that was really read.
// That is what this proves, once, against production. (It already earned its keep: the
// first run caught the TUI band warning firing on a band that drives tools perfectly well.)
//
// WHAT EACH SUBTEST PROVES, and what it does not:
//
//	fetch_and_cite      real broker + real model + real page + real guard. Complete.
//	brave_no_key        the REAL api.search.brave.com, reached for real, refusing us for
//	                    lack of a key: proves the degradation path (the turn still answers,
//	                    with no sources) against the actual provider.
//	search_fetch_cite   the full chain a real answer takes: the model searches, picks a
//	                    result, fetches it, and cites it. The SEARCH RESULTS ARE REAL (live
//	                    Wikipedia search over a real corpus) and the fetched page is real;
//	                    only the response SHAPE is translated to Brave's by a local shim,
//	                    because no Brave key exists on this machine. So this does NOT prove
//	                    Brave's success-response parsing - that is covered by the unit and
//	                    BDD suites against Brave's documented shape, and needs one live run
//	                    with a real key to be closed out.
//
// Env knobs: ROGER_E2E_BROKER (default https://broker.rogerai.fyi), ROGER_E2E_MODEL (the
// band to tune), ROGER_E2E_URL (the page fetch_and_cite reads).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const liveResearchPersona = "You are a research assistant. Use the tools available to you: " +
	"web_search finds pages, web_fetch reads one. Answer only from what you actually read."

func TestAnswersLiveE2E(t *testing.T) {
	if os.Getenv("ANSWERS_E2E") != "1" {
		t.Skip("live answers E2E: set ANSWERS_E2E=1 (talks to the real broker and the real web)")
	}
	broker := envOr("ROGER_E2E_BROKER", "https://broker.rogerai.fyi")
	model := envOr("ROGER_E2E_MODEL", "deepseek-v4-flash")

	t.Run("fetch_and_cite", func(t *testing.T) {
		page := envOr("ROGER_E2E_URL", "https://example.com/")
		res := liveTurn(t, broker, model, liveResearchPersona,
			"Read "+page+" and tell me in one sentence what that page says.")

		if !res.usedTool("web_fetch") {
			t.Fatalf("the band never completed a web_fetch - it is not driving tools (answer: %q)", res.final)
		}
		if len(res.sources) == 0 {
			t.Fatal("a successful fetch produced no sources")
		}
		if !strings.Contains(res.final, "Sources:") || !strings.Contains(res.final, res.sources[0].URL) {
			t.Errorf("the answer carries no usable sources block:\n%s", res.final)
		}
		if res.calls == 0 {
			t.Error("no receipts were recorded for a live turn")
		}
	})

	// The REAL Brave endpoint, with no key: proves the documented degradation (the turn
	// still answers, with no sources) against the actual provider rather than a stub.
	t.Run("brave_no_key", func(t *testing.T) {
		restore := liveSearchConfig(t, braveDefaultEndpoint, "no-key-on-this-machine")
		defer restore()

		res := liveTurn(t, broker, model, liveResearchPersona,
			"Search the web for what the Valkey project is, then answer in one sentence.")

		var sawFailure bool
		for _, e := range res.events {
			if e.Kind == EventToolResult && e.Tool == "web_search" &&
				strings.Contains(strings.ToLower(e.Result), "search failed") {
				sawFailure = true
				t.Logf("real Brave refusal surfaced to the model as: %.120q", e.Result)
			}
		}
		if !sawFailure {
			t.Skip("the band did not attempt a search, so the degradation path was not exercised")
		}
		if strings.TrimSpace(res.final) == "" {
			t.Error("a failed search killed the turn - it must still answer")
		}
		// NOT "no sources": a failed search does not stop a model reaching for a URL it
		// already knows, and citing a page it really fetched is CORRECT. The invariant that
		// must hold is the strict one - every citation is a page that was actually read.
		res.assertSourcesWereFetched(t)
		if !res.usedTool("web_fetch") && strings.Contains(res.final, "Sources:") {
			t.Errorf("a sources block was rendered with nothing retrieved at all:\n%s", res.final)
		}
	})

	// The full chain, with REAL search results over a real corpus and a REAL fetched page.
	t.Run("search_fetch_cite", func(t *testing.T) {
		shim := realSearchShim(t)
		defer shim.Close()
		restore := liveSearchConfig(t, shim.URL, "shim")
		defer restore()

		res := liveTurn(t, broker, model, liveResearchPersona,
			"Search the web for the Valkey project, then read the most relevant result and "+
				"tell me in one sentence what Valkey is.")

		if !res.usedTool("web_search") {
			t.Fatalf("the band never called web_search (answer: %q)", res.final)
		}
		if !res.usedTool("web_fetch") {
			t.Fatalf("the band searched but never read a result (answer: %q)", res.final)
		}
		if len(res.sources) == 0 {
			t.Fatal("the chain produced no sources")
		}
		cited := res.sources[0]
		if !strings.HasPrefix(cited.URL, "http") {
			t.Errorf("cited a non-URL: %+v", cited)
		}
		// Every citation is a page that was really read...
		res.assertSourcesWereFetched(t)
		// ...and at least one of them came out of the SEARCH, which is what makes this the
		// full chain rather than the fetch-only path already proven above.
		var fromSearch int
		for _, s := range res.sources {
			if strings.Contains(res.searchResultText, s.URL) {
				fromSearch++
			}
		}
		if fromSearch == 0 {
			t.Errorf("no cited source came out of the search results:\nsources: %+v\nresults:\n%s",
				res.sources, res.searchResultText)
		}
		if !strings.Contains(res.final, "Sources:") || !strings.Contains(res.final, cited.URL) {
			t.Errorf("the answer carries no usable sources block:\n%s", res.final)
		}
	})
}

// liveResult is one live turn's outcome.
type liveResult struct {
	final            string
	events           []Event
	sources          []source
	calls            int
	spent            float64
	searchResultText string
}

func (r liveResult) usedTool(name string) bool {
	for _, e := range r.events {
		if e.Kind == EventToolResult && e.Tool == name && !e.IsError {
			return true
		}
	}
	return false
}

// assertSourcesWereFetched pins the load-bearing invariant on a LIVE turn: every cited URL
// corresponds to a web_fetch that actually succeeded. This is the one that would catch the
// product citing something the model merely mentioned.
func (r liveResult) assertSourcesWereFetched(t *testing.T) {
	t.Helper()
	fetched := map[string]bool{}
	for _, e := range r.events {
		if e.Kind == EventToolResult && e.Tool == "web_fetch" && !e.IsError {
			if u := retrievedURL(e.Result); u != "" {
				fetched[u] = true
			}
		}
	}
	for _, s := range r.sources {
		if !fetched[s.URL] {
			t.Errorf("cited %q, which was never successfully fetched in this turn", s.URL)
		}
	}
}

// liveTurn runs ONE real turn through the real broker on the given band.
func liveTurn(t *testing.T, broker, model, persona, ask string) liveResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var res liveResult
	onCost := func(credits float64, _, _ int, _ float64) {
		res.calls++
		res.spent += credits
	}
	loop := NewLoop(t.TempDir(), persona, BrokerCompleter(broker, "", model, false, 0, onCost), nil)

	final, err := loop.Send(ctx, ask, func(e Event) {
		res.events = append(res.events, e)
		switch e.Kind {
		case EventToolCall:
			t.Logf("tool call: %s %v", e.Tool, e.Args)
		case EventToolResult:
			if e.Tool == "web_search" && !e.IsError {
				res.searchResultText = e.Result
			}
			t.Logf("tool result (%s, err=%v): %.200q", e.Tool, e.IsError, e.Result)
		}
	})
	if err != nil {
		t.Fatalf("live turn failed: %v", err)
	}
	res.final = final
	res.sources = loop.sources()
	t.Logf("model calls: %d · spend: %.6f credits", res.calls, res.spent)
	t.Logf("final answer:\n%s", final)
	return res
}

// liveSearchConfig points the search adapter at endpoint for the duration of a subtest. It
// copies the REAL config dir (so the broker still sees the real signing identity) into a
// temp XDG_CONFIG_HOME and adds search.json there, rather than writing into the user's live
// config. The returned func restores the environment.
func liveSearchConfig(t *testing.T, endpoint, key string) func() {
	t.Helper()
	realDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "rogerai")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	// Carry the identity + account files across so this is the SAME user to the broker.
	entries, err := os.ReadDir(filepath.Join(realDir, "rogerai"))
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(realDir, "rogerai", e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := json.Marshal(map[string]string{"provider": "brave", "key": key, "endpoint": endpoint})
	if err := os.WriteFile(filepath.Join(dst, "search.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	prevXDG, hadXDG := os.LookupEnv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSearchConfig(); !ok {
		t.Fatal("search did not read as configured after writing search.json")
	}
	return func() {
		if hadXDG {
			_ = os.Setenv("XDG_CONFIG_HOME", prevXDG)
			return
		}
		_ = os.Unsetenv("XDG_CONFIG_HOME")
	}
}

// realSearchShim is a local endpoint that performs a REAL search (live Wikipedia search
// over a real corpus) and re-shapes the answer into Brave's response format. It exists only
// because no Brave key is present on this machine: the results, the URLs, and the pages the
// model then fetches are all real - the SHAPE is what is being stood in for. See the header
// note on exactly what this subtest does and does not prove.
func realSearchShim(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		api := "https://en.wikipedia.org/w/api.php?action=query&list=search&format=json&srlimit=5&srsearch=" +
			url.QueryEscape(q)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, api, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Wikipedia 403s the default Go user-agent; a live run found this the hard way.
		req.Header.Set("User-Agent", "RogerAI-answers-e2e/1.0 (https://rogerai.fyi)")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("upstream search returned %d: %.200s", resp.StatusCode, body), http.StatusBadGateway)
			return
		}

		var wiki struct {
			Query struct {
				Search []struct {
					Title   string `json:"title"`
					Snippet string `json:"snippet"`
				} `json:"search"`
			} `json:"query"`
		}
		if err := json.Unmarshal(body, &wiki); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		type braveResult struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		}
		out := struct {
			Web struct {
				Results []braveResult `json:"results"`
			} `json:"web"`
		}{}
		for _, s := range wiki.Query.Search {
			out.Web.Results = append(out.Web.Results, braveResult{
				Title: s.Title,
				URL:   "https://en.wikipedia.org/wiki/" + strings.ReplaceAll(s.Title, " ", "_"),
				// Wikipedia snippets carry markup; strip it the crude way (the adapter
				// flattens whitespace itself, which is the behavior under test).
				Description: stripTags(s.Snippet),
			})
		}
		t.Logf("shim: real search for %q returned %d results", q, len(out.Web.Results))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// stripTags removes the <span> markup Wikipedia wraps match highlights in.
func stripTags(s string) string {
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

// envOr returns the env var or a default.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
