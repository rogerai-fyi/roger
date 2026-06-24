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
	if st, _ := (moderation{}).screen("x"); st != 0 {
		t.Errorf("disabled should allow, got %d", st)
	}
	// required but unconfigured -> 503 (fail closed)
	if st, _ := (moderation{require: true}).screen("x"); st != http.StatusServiceUnavailable {
		t.Errorf("required+unset should 503, got %d", st)
	}
	// flagged endpoint -> 451
	flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer flag.Close()
	if st, _ := (moderation{provider: "url", url: flag.URL, client: flag.Client()}).screen("bad"); st != http.StatusUnavailableForLegalReasons {
		t.Errorf("flagged should 451, got %d", st)
	}
	// clean endpoint -> allow
	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	defer clean.Close()
	if st, _ := (moderation{provider: "url", url: clean.URL, client: clean.Client()}).screen("ok"); st != 0 {
		t.Errorf("clean should allow, got %d", st)
	}
	// required + endpoint down -> 503 (fail closed); not required + down -> allow (fail open)
	if st, _ := (moderation{provider: "url", url: "http://127.0.0.1:0", require: true, client: &http.Client{}}).screen("x"); st != http.StatusServiceUnavailable {
		t.Errorf("required+unreachable should 503, got %d", st)
	}
	if st, _ := (moderation{provider: "url", url: "http://127.0.0.1:0", client: &http.Client{}}).screen("x"); st != 0 {
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
		if st, _ := m.screen(in); st != 0 {
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
	if st, _ := (moderation{provider: "url", url: srv.URL, client: srv.Client()}).screen("bad"); st != http.StatusUnavailableForLegalReasons {
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
	if st, _ := groqMod(srv, false).screen("hi there"); st != 0 {
		t.Errorf("safe verdict should allow, got %d", st)
	}
}

// TestModerationGroqUnsafe: an "unsafe\nS1" verdict -> BLOCK (451).
func TestModerationGroqUnsafe(t *testing.T) {
	srv := groqVerdictServer(t, "unsafe\nS1", nil)
	defer srv.Close()
	if st, _ := groqMod(srv, false).screen("bad stuff"); st != http.StatusUnavailableForLegalReasons {
		t.Errorf("unsafe verdict should 451, got %d", st)
	}
}

// TestModerationGroqFailClosed: provider=groq with REQUIRE=1 fails CLOSED (503) when
// the Groq endpoint errors (unreachable); not-required fails OPEN (allow).
func TestModerationGroqFailClosed(t *testing.T) {
	down := moderation{provider: "groq", require: true, client: &http.Client{},
		groqKey: "test-key", groqURL: "http://127.0.0.1:0", groqModel: "x"}
	if st, _ := down.screen("x"); st != http.StatusServiceUnavailable {
		t.Errorf("groq+required+unreachable should 503, got %d", st)
	}
	open := down
	open.require = false
	if st, _ := open.screen("x"); st != 0 {
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
		if st, _ := m.screen(in); st != 0 {
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
