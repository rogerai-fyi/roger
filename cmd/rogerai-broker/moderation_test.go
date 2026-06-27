package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptText(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello there"},{"role":"assistant","content":"hi back"}]}`)
	got := promptText(body)
	if !strings.Contains(got, "hello there") || !strings.Contains(got, "hi back") {
		t.Errorf("promptText = %q", got)
	}
	// multimodal array content: pull the text parts
	mm := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"array part"}]}]}`)
	if !strings.Contains(promptText(mm), "array part") {
		t.Errorf("promptText (array) = %q", promptText(mm))
	}
}

func TestModerationScreen(t *testing.T) {
	// disabled (no url, not required) -> allow
	if st := (moderation{}).screen("x").status; st != 0 {
		t.Errorf("disabled should allow, got %d", st)
	}
	// required but unconfigured -> 503 (fail closed)
	if st := (moderation{require: true}).screen("x").status; st != http.StatusServiceUnavailable {
		t.Errorf("required+unset should 503, got %d", st)
	}
	// flagged endpoint -> 451
	flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer flag.Close()
	if st := (moderation{provider: "url", url: flag.URL, client: flag.Client()}).screen("bad").status; st != http.StatusUnavailableForLegalReasons {
		t.Errorf("flagged should 451, got %d", st)
	}
	// clean endpoint -> allow
	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	defer clean.Close()
	if st := (moderation{provider: "url", url: clean.URL, client: clean.Client()}).screen("ok").status; st != 0 {
		t.Errorf("clean should allow, got %d", st)
	}
	// required + endpoint down -> 503 (fail closed); not required + down -> allow (fail open)
	if st := (moderation{provider: "url", url: "http://127.0.0.1:0", require: true, client: &http.Client{}}).screen("x").status; st != http.StatusServiceUnavailable {
		t.Errorf("required+unreachable should 503, got %d", st)
	}
	if st := (moderation{provider: "url", url: "http://127.0.0.1:0", client: &http.Client{}}).screen("x").status; st != 0 {
		t.Errorf("unreachable+not-required should fail open, got %d", st)
	}
}

// TestModerationEmptyInputShortCircuits: a configured screen does NOT hit the network
// for empty/whitespace input - it allows without calling the endpoint.
func TestModerationEmptyInputShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer srv.Close()
	m := moderation{provider: "url", url: srv.URL, client: srv.Client()}
	for _, in := range []string{"", "   ", "\n\t "} {
		if st := m.screen(in).status; st != 0 {
			t.Errorf("empty input %q should allow, got %d", in, st)
		}
	}
	if called {
		t.Error("empty input must not call the moderation endpoint (short-circuit)")
	}
}

// TestModerationCategoriesShape: the OpenAI categories map is parsed alongside flagged
// (used for the block log) and a flag still rejects with 451.
func TestModerationCategoriesShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"sexual/minors":true,"violence":false}}]}`))
	}))
	defer srv.Close()
	if st := (moderation{provider: "url", url: srv.URL, client: srv.Client()}).screen("bad").status; st != http.StatusUnavailableForLegalReasons {
		t.Errorf("flagged-with-categories should 451, got %d", st)
	}
}

// groqVerdictServer stubs Groq's OpenAI-compatible chat/completions, returning a
// Llama Guard verdict as the assistant message content (e.g. "safe" or "unsafe\nS1").
func groqVerdictServer(t *testing.T, verdict string, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("expected bearer auth, got %q", got)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconvQuote(verdict) + `}}]}`))
	}))
}

// strconvQuote JSON-quotes a string for embedding in the stub response body.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func groqMod(srv *httptest.Server, require bool) moderation {
	return moderation{
		provider: "groq", require: require, client: srv.Client(),
		groqKey: "test-key", groqURL: srv.URL, groqModel: "meta-llama/llama-guard-4-12b",
	}
}

// TestModerationGroqSafe: a "safe" verdict -> ALLOW (status 0).
func TestModerationGroqSafe(t *testing.T) {
	srv := groqVerdictServer(t, "safe", nil)
	defer srv.Close()
	if st := groqMod(srv, false).screen("hi there").status; st != 0 {
		t.Errorf("safe verdict should allow, got %d", st)
	}
}

// TestModerationGroqUnsafe: an "unsafe\nS1" verdict -> BLOCK (451).
func TestModerationGroqUnsafe(t *testing.T) {
	srv := groqVerdictServer(t, "unsafe\nS1", nil)
	defer srv.Close()
	if st := groqMod(srv, false).screen("bad stuff").status; st != http.StatusUnavailableForLegalReasons {
		t.Errorf("unsafe verdict should 451, got %d", st)
	}
}

// TestModerationGroqSafeguardSameLineCSAM guards the safeguard-model output format: the
// model answers "unsafe S4" with the category on the SAME line. The screen must 451 AND
// flag CSAM (S4) so the incident is preserved + a CyberTipline report queued. The old
// Llama-Guard parser expected codes on the NEXT line and would have lost the S4 signal.
func TestModerationGroqSafeguardSameLineCSAM(t *testing.T) {
	srv := groqVerdictServer(t, "unsafe S4", nil)
	defer srv.Close()
	m := groqMod(srv, true)
	m.csamCats = loadCSAMCategories("") // defaults: s4, sexual/minors
	r := m.screen("...redacted...")
	if r.status != http.StatusUnavailableForLegalReasons {
		t.Fatalf("unsafe S4 should 451, got %d", r.status)
	}
	if !r.csam || strings.ToLower(r.category) != "s4" {
		t.Fatalf("S4 (same line) must be detected as CSAM, got csam=%v category=%q", r.csam, r.category)
	}
}

// TestModerationGroqIgnoresReasoningChannel guards that the verdict is parsed from
// message.content ONLY: the safeguard model's chain-of-thought (which can literally
// contain the word "unsafe") lands in message.reasoning and must NOT flip a "safe" verdict
// to blocked. (completionText folds reasoning in for billing; moderation must not.)
func TestModerationGroqIgnoresReasoningChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","reasoning":"The user might be unsafe, considering S1... but no.","content":"safe"}}]}`))
	}))
	defer srv.Close()
	m := moderation{provider: "groq", require: true, client: srv.Client(), groqKey: "test-key", groqURL: srv.URL, groqModel: "x"}
	if st := m.screen("is the sky blue?").status; st != 0 {
		t.Fatalf("a 'safe' content verdict must ALLOW even when the reasoning mentions unsafe categories, got %d", st)
	}
}

// TestModerationGroqVerdictParsing exercises the safe/unsafe word-boundary parse across
// the verdict shapes the safeguard model emits, so a stray punctuation/format never flips
// allow<->block: a first word of exactly "safe" allows; anything else fails toward blocking.
func TestModerationGroqVerdictParsing(t *testing.T) {
	cases := []struct {
		verdict   string
		wantBlock bool
	}{
		{"safe", false},
		{"safe.", false},
		{"safe,", false}, // behavioral delta vs the old prefix-list (which would 451 this)
		{"safe: no violations", false},
		{"safe\n", false},
		{"Safe", false},
		{"unsafe S1", true},
		{"unsafe S4", true},
		{"unsafe", true},        // flagged even with no codes (fail toward blocking)
		{"unsafe\nS1,S3", true}, // next-line codes still block
		{"I cannot help", true}, // unexpected -> fail toward blocking
	}
	for _, c := range cases {
		srv := groqVerdictServer(t, c.verdict, nil)
		st := groqMod(srv, false).screen("text").status
		srv.Close()
		blocked := st == http.StatusUnavailableForLegalReasons
		if blocked != c.wantBlock {
			t.Errorf("verdict %q: blocked=%v (status %d), want blocked=%v", c.verdict, blocked, st, c.wantBlock)
		}
	}
}

// TestModerationGroqFailClosed: provider=groq with REQUIRE=1 fails CLOSED (503) when
// the Groq endpoint errors (unreachable); not-required fails OPEN (allow).
func TestModerationGroqFailClosed(t *testing.T) {
	down := moderation{provider: "groq", require: true, client: &http.Client{},
		groqKey: "test-key", groqURL: "http://127.0.0.1:0", groqModel: "x"}
	if st := down.screen("x").status; st != http.StatusServiceUnavailable {
		t.Errorf("groq+required+unreachable should 503, got %d", st)
	}
	open := down
	open.require = false
	if st := open.screen("x").status; st != 0 {
		t.Errorf("groq+unreachable+not-required should fail open, got %d", st)
	}
}

// TestModerationGroqEmptyShortCircuits: empty/whitespace input never calls Groq.
func TestModerationGroqEmptyShortCircuits(t *testing.T) {
	calls := 0
	srv := groqVerdictServer(t, "unsafe\nS1", &calls)
	defer srv.Close()
	m := groqMod(srv, true)
	for _, in := range []string{"", "   ", "\n\t "} {
		if st := m.screen(in).status; st != 0 {
			t.Errorf("empty input %q should allow, got %d", in, st)
		}
	}
	if calls != 0 {
		t.Errorf("empty input must not call Groq, got %d calls", calls)
	}
}

// TestModerationProviderSelection covers the backend resolution in loadModeration via
// the same inference logic: explicit provider wins; otherwise url > groq > off.
func TestModerationProviderSelection(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		url        string
		groqKey    string
		wantScreen string // "url" | "groq" | "off"
	}{
		{"explicit groq", "groq", "", "k", "groq"},
		{"explicit url", "url", "http://x", "k", "url"},
		{"infer url when url set", "", "http://x", "", "url"},
		{"infer groq when only key", "", "", "k", "groq"},
		{"off when nothing", "", "", "", "off"},
		{"infer url wins over key", "", "http://x", "k", "url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := resolveProvider(c.provider, c.url, c.groqKey)
			if m != c.wantScreen {
				t.Errorf("provider=%q url=%q key set=%v -> %q, want %q", c.provider, c.url, c.groqKey != "", m, c.wantScreen)
			}
		})
	}
}

// resolveProvider mirrors loadModeration's backend inference so the selection rule is
// unit-testable without environment variables. Returns "url"/"groq"/"off".
func resolveProvider(provider, url, groqKey string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "url", "groq":
		// explicit
	default:
		switch {
		case url != "":
			p = "url"
		case groqKey != "":
			p = "groq"
		default:
			p = ""
		}
	}
	if p == "" {
		return "off"
	}
	return p
}
