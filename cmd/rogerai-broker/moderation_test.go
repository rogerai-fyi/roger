package main

import (
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
	if st, _ := (moderation{url: flag.URL, client: flag.Client()}).screen("bad"); st != http.StatusUnavailableForLegalReasons {
		t.Errorf("flagged should 451, got %d", st)
	}
	// clean endpoint -> allow
	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	defer clean.Close()
	if st, _ := (moderation{url: clean.URL, client: clean.Client()}).screen("ok"); st != 0 {
		t.Errorf("clean should allow, got %d", st)
	}
	// required + endpoint down -> 503 (fail closed); not required + down -> allow (fail open)
	if st, _ := (moderation{url: "http://127.0.0.1:0", require: true, client: &http.Client{}}).screen("x"); st != http.StatusServiceUnavailable {
		t.Errorf("required+unreachable should 503, got %d", st)
	}
	if st, _ := (moderation{url: "http://127.0.0.1:0", client: &http.Client{}}).screen("x"); st != 0 {
		t.Errorf("unreachable+not-required should fail open, got %d", st)
	}
}
