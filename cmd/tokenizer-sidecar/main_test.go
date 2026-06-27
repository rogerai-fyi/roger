package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

// TestRunServesAndShutsDown exercises the real server lifecycle: run() binds an
// ephemeral port (port "0"), serves a live /health request over the wire, then returns
// cleanly when stop is closed. This covers the main() path (env defaults aside) without
// blocking forever.
func TestRunServesAndShutsDown(t *testing.T) {
	ready := make(chan string, 1)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- run("0", "", ready, stop) }()

	addr := <-ready // the actual 127.0.0.1:<chosen-port>
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health over the wire: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("/health = %d %q, want 200 ok", resp.StatusCode, body)
	}

	close(stop) // trigger graceful shutdown
	if err := <-errc; err != nil {
		t.Fatalf("run returned %v, want nil after clean shutdown", err)
	}
}

// TestRunBadPort: an invalid port makes the bind fail and run() returns the error
// (the path main() turns into log.Fatal).
func TestRunBadPort(t *testing.T) {
	if err := run("not-a-port", "", nil, nil); err == nil {
		t.Fatal("run with an invalid port should return a bind error")
	}
}

// TestRunWithDir exercises run()'s TOKENIZER_DIR != "" branch (the alternate startup
// log line + a dir-backed Counter): it binds an ephemeral port, serves a real /count
// request whose tiktoken path is independent of the (empty) dir, then shuts down clean.
func TestRunWithDir(t *testing.T) {
	dir := t.TempDir()
	ready := make(chan string, 1)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- run("0", dir, ready, stop) }()

	addr := <-ready
	resp, err := http.Post("http://"+addr+"/count", "application/json",
		strings.NewReader(`{"model":"gpt-4o","text":"hello world"}`))
	if err != nil {
		t.Fatalf("POST /count over the wire: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/count = %d %q, want 200", resp.StatusCode, body)
	}
	var res countResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Exact || res.Tokens <= 0 || !strings.HasPrefix(res.Method, "tiktoken:") {
		t.Fatalf("count = %+v, want exact tiktoken count > 0", res)
	}

	close(stop)
	if err := <-errc; err != nil {
		t.Fatalf("run returned %v, want nil after clean shutdown", err)
	}
}

// TestMainSuccess: with the run seam returning nil, main() must NOT invoke fatalFn.
func TestMainSuccess(t *testing.T) {
	origRun, origFatal := runFn, fatalFn
	defer func() { runFn, fatalFn = origRun, origFatal }()

	var gotPort, gotDir string
	runFn = func(port, dir string, ready chan<- string, stop <-chan struct{}) error {
		gotPort, gotDir = port, dir
		return nil
	}
	fatalCalled := false
	fatalFn = func(v ...any) { fatalCalled = true }

	t.Setenv("TOKENIZER_PORT", "12345")
	t.Setenv("TOKENIZER_DIR", "/some/dir")
	main()

	if gotPort != "12345" || gotDir != "/some/dir" {
		t.Fatalf("run got (port=%q dir=%q), want env values", gotPort, gotDir)
	}
	if fatalCalled {
		t.Fatal("fatalFn must NOT be called when run succeeds")
	}
}

// TestMainFatalOnError: when the run seam returns an error, main() forwards it to fatalFn.
func TestMainFatalOnError(t *testing.T) {
	origRun, origFatal := runFn, fatalFn
	defer func() { runFn, fatalFn = origRun, origFatal }()

	want := errors.New("bind failed")
	runFn = func(port, dir string, ready chan<- string, stop <-chan struct{}) error { return want }
	var got error
	fatalFn = func(v ...any) {
		if len(v) == 1 {
			got, _ = v[0].(error)
		}
	}

	main()

	if !errors.Is(got, want) {
		t.Fatalf("fatalFn got %v, want %v", got, want)
	}
}
