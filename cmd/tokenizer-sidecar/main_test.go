package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCountHandler returns the REAL routes from main.go (newMux) over a dir-less Counter,
// so these tests exercise the production handler wiring (not a copy) and count toward
// cmd/tokenizer-sidecar coverage.
func newCountHandler() http.Handler { return newMux("") }

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestSidecarHealth: GET /health -> 200 "ok".
func TestSidecarHealth(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodGet, "/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("health = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "ok" {
		t.Errorf("health body = %q, want ok", got)
	}
}

// TestSidecarCountExact: a GPT-family model -> an exact tiktoken count and the
// tiktoken method tag. The count is a stable, positive integer for known text.
func TestSidecarCountExact(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodPost, "/count", `{"model":"gpt-4o","text":"hello world"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("count = %d, want 200 body=%s", w.Code, w.Body.String())
	}
	var res countResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Exact {
		t.Errorf("gpt-4o must be an EXACT count, got exact=false (method=%s)", res.Method)
	}
	if res.Tokens <= 0 {
		t.Errorf("tokens = %d, want > 0 for non-empty text", res.Tokens)
	}
	if !strings.HasPrefix(res.Method, "tiktoken:") {
		t.Errorf("method = %q, want a tiktoken:* tag for gpt-4o", res.Method)
	}
}

// TestSidecarCountHeuristic: a non-tiktoken family (e.g. a Llama model) with no
// pinned HF tokenizer falls back to the calibrated heuristic, marked exact=false.
func TestSidecarCountHeuristic(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodPost, "/count",
		`{"model":"meta-llama/Llama-3.1-8B","text":"the quick brown fox jumps over the lazy dog"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("count = %d, want 200", w.Code)
	}
	var res countResponse
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Exact {
		t.Errorf("a non-tiktoken model with no HF dir must NOT be exact, got exact=true (method=%s)", res.Method)
	}
	if res.Method != "heuristic" {
		t.Errorf("method = %q, want heuristic", res.Method)
	}
	if res.Tokens <= 0 {
		t.Errorf("heuristic tokens = %d, want > 0", res.Tokens)
	}
}

// TestSidecarCountEmptyText: empty text -> 0 tokens, exact (nothing to estimate).
func TestSidecarCountEmptyText(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodPost, "/count", `{"model":"gpt-4o","text":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("count = %d, want 200", w.Code)
	}
	var res countResponse
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Tokens != 0 || !res.Exact {
		t.Errorf("empty text = {tokens:%d exact:%v}, want {0 true}", res.Tokens, res.Exact)
	}
}

// TestSidecarCountMethodNotAllowed: GET /count -> 405 with an Allow: POST header.
func TestSidecarCountMethodNotAllowed(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodGet, "/count", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /count = %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow header = %q, want POST", got)
	}
}

// TestSidecarCountBadJSON: a malformed body -> 400 bad request.
func TestSidecarCountBadJSON(t *testing.T) {
	w := do(t, newCountHandler(), http.MethodPost, "/count", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", w.Code)
	}
}
