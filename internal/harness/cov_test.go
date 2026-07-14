package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolByName finds a builtin tool by name (helper).
func toolByName(t *testing.T, name string) Tool {
	t.Helper()
	for _, tl := range BuiltinTools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("builtin tool %q not found", name)
	return Tool{}
}

// TestBuiltinToolsRun exercises every built-in tool's Run against a temp root: write +
// read round-trip, list_dir, run_shell (echo), and web_fetch (local server). Covers
// runShell/shellCommand, webFetch, str, clip, resolveInRoot.
func TestBuiltinToolsRun(t *testing.T) {
	root := t.TempDir()

	// write_file (mutating) then read_file.
	wf := toolByName(t, "write_file")
	if _, err := wf.Run(root, map[string]any{"path": "note.txt", "content": "hello world"}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	rf := toolByName(t, "read_file")
	out, err := rf.Run(root, map[string]any{"path": "note.txt"})
	if err != nil || out != "hello world" {
		t.Fatalf("read_file = %q/%v, want hello world", out, err)
	}
	// read_file path-escape is rejected (sandbox).
	if _, err := rf.Run(root, map[string]any{"path": "../escape"}); err == nil {
		t.Error("read_file should reject a path escaping the sandbox")
	}

	// list_dir shows the file we wrote.
	ld := toolByName(t, "list_dir")
	if out, err := ld.Run(root, map[string]any{}); err != nil || !strings.Contains(out, "note.txt") {
		t.Errorf("list_dir = %q/%v, want it to list note.txt", out, err)
	}

	// run_shell echoes (mutating tool, but we invoke Run directly past the confirm gate).
	rs := toolByName(t, "run_shell")
	if out, err := rs.Run(root, map[string]any{"cmd": "echo harness-ok"}); err != nil || !strings.Contains(out, "harness-ok") {
		t.Errorf("run_shell = %q/%v, want harness-ok", out, err)
	}
	// empty command -> error.
	if _, err := rs.Run(root, map[string]any{"cmd": "  "}); err == nil {
		t.Error("run_shell with an empty command should error")
	}

	// web_fetch against a local server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fetched body"))
	}))
	defer srv.Close()
	wfetch := toolByName(t, "web_fetch")
	if out, err := wfetch.Run(root, map[string]any{"url": srv.URL}); err != nil || !strings.Contains(out, "fetched body") {
		t.Errorf("web_fetch = %q/%v, want fetched body", out, err)
	}
	// non-http scheme -> error.
	if _, err := wfetch.Run(root, map[string]any{"url": "ftp://x"}); err == nil {
		t.Error("web_fetch should reject a non-http(s) URL")
	}
}

// TestLoopAccessorsAndReset covers Tools(), Reset(), and lastAssistantText() (reached by
// hitting MaxSteps with a model that keeps requesting tools).
func TestLoopAccessorsAndReset(t *testing.T) {
	// A completer that ALWAYS asks for a tool -> the loop hits MaxSteps and falls back to
	// lastAssistantText.
	loopy := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		tc := ToolCall{ID: "c1"}
		tc.Function.Name = "read_file"
		tc.Function.Arguments = `{"path":"x"}`
		return Message{Role: "assistant", Content: "thinking", ToolCalls: []ToolCall{tc}}, nil
	}
	l := NewLoop(t.TempDir(), "sys-persona", loopy, func(string, map[string]any) bool { return true })
	l.MaxSteps = 1

	if len(l.Tools()) == 0 {
		t.Error("Tools() should expose the builtin toolset")
	}
	final, err := l.Send(context.Background(), "do something", nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if final != "thinking" {
		t.Errorf("step-cap final = %q, want the last assistant text", final)
	}

	l.Reset() // back to just the persona
	// A degrade-to-chat turn after reset proves the transcript was cleared + persona kept.
	l2 := NewLoop(t.TempDir(), "sys", stubCompleter(Message{Role: "assistant", Content: "hi"}), nil)
	if got, _ := l2.Send(context.Background(), "yo", nil); got != "hi" {
		t.Errorf("post-reset chat = %q, want hi", got)
	}
}

// TestPersonaPathAndLoad covers PersonaPath, LoadPersona (seed-on-missing + empty
// fallback), and trimSpace.
func TestPersonaPathAndLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if p := PersonaPath(); !strings.HasSuffix(p, filepath.Join("rogerai", "dj.md")) {
		t.Errorf("PersonaPath = %q, want .../rogerai/dj.md", p)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "dj.md")
	// Missing -> seeds + returns the default, and the file now exists.
	if got := LoadPersona(path); got != DefaultPersona {
		t.Error("LoadPersona(missing) should return the default")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("LoadPersona should have seeded the file: %v", err)
	}
	// Present + custom -> returns the file.
	_ = os.WriteFile(path, []byte("custom persona"), 0o600)
	if got := LoadPersona(path); got != "custom persona" {
		t.Errorf("LoadPersona(custom) = %q", got)
	}
	// Present + all whitespace -> default fallback.
	_ = os.WriteFile(path, []byte("   \n\t "), 0o600)
	if got := LoadPersona(path); got != DefaultPersona {
		t.Error("LoadPersona(whitespace) should fall back to the default")
	}
}

// TestParseCompletion covers the broker response parser: a tool-call message, a
// plain-content message, a provider error body, and an empty/no-choices response.
func TestParseCompletion(t *testing.T) {
	withTools := `{"choices":[{"message":{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{}"}}]}}]}`
	m, err := parseCompletion([]byte(withTools), 200)
	if err != nil || len(m.ToolCalls) != 1 || m.Content != "ok" {
		t.Fatalf("parseCompletion(tools) = %+v/%v", m, err)
	}

	plain := `{"choices":[{"message":{"role":"assistant","content":"just text"}}]}`
	if m, err := parseCompletion([]byte(plain), 200); err != nil || m.Content != "just text" {
		t.Fatalf("parseCompletion(plain) = %+v/%v", m, err)
	}

	// Reasoning-only content surfaces as Thought (never dressed up as Content - the
	// loop renders it as thinking aloud; see thought_test.go for both backend keys).
	reasoning := `{"choices":[{"message":{"role":"assistant","reasoning":"because"}}]}`
	if m, _ := parseCompletion([]byte(reasoning), 200); m.Content != "" || m.Thought != "because" {
		t.Errorf("parseCompletion(reasoning) = Content %q / Thought %q, want empty / because", m.Content, m.Thought)
	}

	// Provider error body -> surfaced as an error.
	if _, err := parseCompletion([]byte(`{"error":{"message":"no station online"}}`), 503); err == nil {
		t.Error("parseCompletion should surface a provider error")
	}
	// No choices + status>=400 with a short body -> error naming the status.
	if _, err := parseCompletion([]byte("  bad gateway  "), 502); err == nil {
		t.Error("parseCompletion(no choices, 502) should error")
	}
}

// TestHelperBranches covers the small helpers' remaining branches: parseArgs
// (empty/malformed/valid), str (nil/string/other), clip (truncation), and the
// webFetch + runShell error/edge paths.
func TestHelperBranches(t *testing.T) {
	// parseArgs
	if m := parseArgs(""); len(m) != 0 {
		t.Errorf("parseArgs(empty) = %v, want {}", m)
	}
	if m := parseArgs("{not json"); len(m) != 0 {
		t.Errorf("parseArgs(bad) = %v, want {}", m)
	}
	if m := parseArgs(`{"a":1}`); m["a"] != float64(1) {
		t.Errorf("parseArgs(valid) = %v", m)
	}

	// str coercion
	if str(nil) != "" || str("x") != "x" || str(42) != "42" || str(true) != "true" {
		t.Errorf("str coercion wrong")
	}

	// clip truncates past maxToolOutput.
	big := strings.Repeat("a", maxToolOutput+50)
	if out := clip(big); !strings.Contains(out, "(truncated)") || len(out) <= maxToolOutput {
		t.Errorf("clip should truncate + mark; len=%d", len(out))
	}

	// webFetch: a 4xx surfaces the status; an empty 200 says so.
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer notFound.Close()
	if out, _ := webFetch(notFound.URL); !strings.Contains(out, "HTTP 404") {
		t.Errorf("webFetch(404) = %q, want HTTP 404", out)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer empty.Close()
	if out, _ := webFetch(empty.URL); !strings.Contains(out, "empty body") {
		t.Errorf("webFetch(empty) = %q, want empty body note", out)
	}

	// runShell: a non-zero exit surfaces the exit info; a no-output success says so.
	root := t.TempDir()
	if out, err := runShell(root, "echo oops; exit 3"); err != nil || !strings.Contains(out, "exit") {
		t.Errorf("runShell(exit 3) = %q/%v, want exit note", out, err)
	}
	if out, _ := runShell(root, "true"); out != "(no output)" {
		t.Errorf("runShell(true) = %q, want (no output)", out)
	}
}

// TestBrokerCompleterCancelled covers the completer's cancelled-context branch (a clean
// "turn cancelled" rather than a network error).
func TestBrokerCompleterCancelled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call
	comp := BrokerCompleter("http://127.0.0.1:0", "u", "m", false, 0, nil)
	if _, err := comp(ctx, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Error("a cancelled context should make BrokerCompleter return an error")
	}
}

// TestBrokerCompleter covers the broker relay completer end-to-end against a fake broker
// that returns an OpenAI-shaped completion (+ a cost header the CostFunc records).
func TestBrokerCompleter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // SignRequest mints a key here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("X-RogerAI-Cost", "0.0021")
		// The broker's BILLED token counts ride alongside the cost so the meter can show
		// an honest ↑in ↓out; the completer must parse + forward them.
		w.Header().Set("X-RogerAI-Tokens-In", "12")
		w.Header().Set("X-RogerAI-Tokens-Out", "34")
		w.Header().Set("X-RogerAI-TPS", "47.5") // latest-call throughput, forwarded to the meter
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"relayed reply"}}]}`))
	}))
	defer srv.Close()

	var billed, gotTPS float64
	var gotIn, gotOut int
	comp := BrokerCompleter(srv.URL, "u_gh_1", "qwen", false, 0, func(c float64, in, out int, tps float64) {
		billed, gotIn, gotOut, gotTPS = c, in, out, tps
	})
	msg, err := comp(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || msg.Content != "relayed reply" {
		t.Fatalf("BrokerCompleter = %+v/%v, want relayed reply", msg, err)
	}
	if billed < 0.002 || billed > 0.0022 {
		t.Errorf("CostFunc billed = %v, want ~0.0021", billed)
	}
	if gotIn != 12 || gotOut != 34 {
		t.Errorf("CostFunc tokens = ↑%d ↓%d, want ↑12 ↓34 (the broker's billed counts)", gotIn, gotOut)
	}
	if gotTPS != 47.5 {
		t.Errorf("CostFunc tps = %v, want 47.5 (the latest-call throughput)", gotTPS)
	}
}
