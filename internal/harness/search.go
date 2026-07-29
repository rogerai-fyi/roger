package harness

// search.go is the web_search builtin: the retrieval half of answers mode. It queries a
// configured search provider (Brave is the MVP adapter) and hands the model ranked
// title / url / snippet results to read with web_fetch. Spec:
// features/answers/web_search.feature.
//
// The provider endpoint is OPERATOR-supplied config, not a model-supplied URL, so it is
// deliberately not subject to the web_fetch address guard - that is what lets an operator
// point it at a self-hosted search service.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// searchDefaultCount is the result count when the model does not ask for one.
	searchDefaultCount = 5
	// searchMaxCount is the hard ceiling on results, whatever the model asks for: it
	// bounds both the tokens fed back and the fetch fan-out it invites.
	searchMaxCount = 10
	// searchMaxQuery bounds a query before it reaches the wire.
	searchMaxQuery = 400
	// braveDefaultEndpoint is the Brave Search API web endpoint.
	braveDefaultEndpoint = "https://api.search.brave.com/res/v1/web/search"
)

// searchTimeout bounds one provider request. A var (the shellTimeout precedent) so a test
// can shorten it; production is unchanged.
var searchTimeout = 15 * time.Second

// searchConfig is <UserConfigDir>/rogerai/search.json - the presence of this file is what
// turns answers mode on.
type searchConfig struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Endpoint string `json:"endpoint"` // optional override (self-hosted / test)
}

// searchConfigPath mirrors PersonaPath's layout: <UserConfigDir>/rogerai/search.json.
func searchConfigPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			return ""
		}
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "rogerai", "search.json")
}

// loadSearchConfig reads the search config; ok is false when search is not configured (no
// file, unreadable, or no key), which is simply "answers mode is off".
func loadSearchConfig() (searchConfig, bool) {
	p := searchConfigPath()
	if p == "" {
		return searchConfig{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return searchConfig{}, false
	}
	var cfg searchConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return searchConfig{}, false
	}
	if strings.TrimSpace(cfg.Key) == "" {
		return searchConfig{}, false
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = braveDefaultEndpoint
	}
	return cfg, true
}

// searchResult is one ranked web result.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// searchTool builds the web_search tool for a configured provider. Read-only: it
// auto-runs, like every other retrieval tool.
func searchTool(cfg searchConfig) Tool {
	return Tool{
		Name: "web_search",
		Description: "Search the web and return ranked results (title, URL, snippet) to read with web_fetch. " +
			"Read-only. Use it when the answer depends on current or external information.",
		Mutating: false,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type": "string", "description": "The search query.",
				},
				"count": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": searchMaxCount,
					"description": fmt.Sprintf("How many results to return (default %d, maximum %d).",
						searchDefaultCount, searchMaxCount),
				},
			},
			"required": []any{"query"},
		},
		Run: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			return runWebSearch(ctx, cfg, str(args["query"]), intArg(args["count"]))
		},
	}
}

// intArg coerces a JSON-decoded arg to an int (models send numbers as float64, and
// sometimes as a string). 0 means "unset".
func intArg(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// runWebSearch validates, queries, shapes, and renders. Provider failures come back as a
// TOOL RESULT (nil error) so the model can react and still answer without sources; only
// caller mistakes (empty / over-long query) are tool errors.
func runWebSearch(ctx context.Context, cfg searchConfig, query string, count int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("empty query: web_search needs a query string")
	}
	if len(query) > searchMaxQuery {
		return "", fmt.Errorf("query is %d characters, over the %d character cap", len(query), searchMaxQuery)
	}
	switch {
	case count <= 0:
		count = searchDefaultCount
	case count > searchMaxCount:
		count = searchMaxCount
	}

	results, err := braveSearch(ctx, cfg, query, count)
	if err != nil {
		return fmt.Sprintf("search failed: %v", err), nil
	}
	results = shapeResults(results, count)
	if len(results) == 0 {
		return fmt.Sprintf("no results found for %q", query), nil
	}
	return clip(stripControls(renderResults(results))), nil
}

// shapeResults drops anything web_fetch could not safely follow (non-http(s) URLs) and
// enforces the count bound, preserving provider rank order.
func shapeResults(in []searchResult, count int) []searchResult {
	out := make([]searchResult, 0, len(in))
	for _, r := range in {
		u, err := url.Parse(strings.TrimSpace(r.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		out = append(out, r)
		if len(out) == count {
			break
		}
	}
	return out
}

// renderResults formats results for the model: numbered, rank-ordered, one URL per line.
// Titles and snippets are FLATTENED to a single line each: they are attacker-influenced
// text (anyone can title their own page), and a newline inside one would forge an extra
// "[n] Title / URL" pair that the citation reader would then bind to somebody else's URL.
func renderResults(rs []searchResult) string {
	var b strings.Builder
	for i, r := range rs {
		fmt.Fprintf(&b, "[%d] %s\n    %s\n", i+1, flatten(r.Title), flatten(r.URL))
		if s := flatten(r.Snippet); s != "" {
			fmt.Fprintf(&b, "    %s\n", s)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// flatten cleans one field of provider text: markup out, entities decoded, all whitespace
// (including newlines) collapsed to single spaces.
//
// Brave wraps query-term matches in <strong>; a live run showed that reaching the model
// verbatim. Markup spends context on nothing, and tag-shaped text handed to an agent is
// worse than noise. The newline collapse is load-bearing separately: a newline inside a
// title or snippet would forge an extra "[n] Title / URL" pair that the citation reader
// would then bind to somebody else's URL.
func flatten(s string) string {
	// Strip, then decode, then NEUTRALIZE what decoding may have re-formed: a snippet
	// carrying &lt;strong&gt; would otherwise decode into literal tag-shaped text after the
	// stripper had already run. Decoding first instead would eat legitimate prose ("a < b"),
	// so the angle brackets are blanked rather than the order reversed.
	return strings.Join(strings.Fields(angleBrackets.Replace(html.UnescapeString(stripTags(s)))), " ")
}

// angleBrackets blanks any angle bracket surviving the strip+decode pass.
var angleBrackets = strings.NewReplacer("<", " ", ">", " ")

// stripTags removes anything between angle brackets. Provider snippets are HTML fragments,
// not documents, so this is deliberately blunt: no parser, no partial-tag ambiguity, and an
// unclosed "<" simply truncates there rather than leaking the rest as markup.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return b.String()
		}
		s = s[i+j+1:]
	}
}

// braveSearch calls the Brave Search API once. NO retry: a 429 must not be answered by
// hammering a rate-limited provider (the /discover incident's lesson).
func braveSearch(ctx context.Context, cfg searchConfig, query string, count int) ([]searchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("bad search endpoint %q: %w", cfg.Endpoint, err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", cfg.Key)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("search provider rate limited this key (HTTP 429)")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("search provider returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	// Bound the provider's response like any other remote body (web_fetch caps at the same
	// size); an operator-configured endpoint is still a remote service.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxFetchBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("unreadable search response: %w", err)
	}
	out := make([]searchResult, 0, len(payload.Web.Results))
	for _, r := range payload.Web.Results {
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}
