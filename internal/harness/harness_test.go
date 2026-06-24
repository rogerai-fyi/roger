package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/client"
)

// stubCompleter returns a scripted sequence of assistant messages, one per call, so a
// loop turn is fully deterministic (no network, no real model).
func stubCompleter(replies ...Message) Completer {
	i := 0
	return func(_ []Message, _ []map[string]any) (Message, error) {
		if i >= len(replies) {
			// After the script ends, return a bare final answer so the loop terminates.
			return Message{Role: "assistant", Content: "done"}, nil
		}
		r := replies[i]
		i++
		return r, nil
	}
}

// toolCall builds an assistant message that requests one tool call.
func toolCall(id, name, args string) Message {
	var tc ToolCall
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = args
	return Message{Role: "assistant", ToolCalls: []ToolCall{tc}}
}

// TestLoopExecutesToolAndFeedsResultBack: the loop runs a read-only tool_call, feeds
// the result back, and the model's NEXT turn (a plain answer) is the final answer.
// The completer is a stub and the file is real-but-temp, so it is deterministic.
func TestLoopExecutesToolAndFeedsResultBack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("carrier locked"), 0644); err != nil {
		t.Fatal(err)
	}
	// Capture what the completer sees on the SECOND call - it must include the tool
	// result fed back (the round-trip we are asserting).
	var sawToolResult bool
	calls := 0
	complete := func(msgs []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("c1", "read_file", `{"path":"hello.txt"}`), nil
		}
		for _, m := range msgs {
			if m.Role == "tool" && strings.Contains(m.Content, "carrier locked") {
				sawToolResult = true
			}
		}
		return Message{Role: "assistant", Content: "the file says: carrier locked"}, nil
	}
	l := NewLoop(root, "sys", complete, func(string, map[string]any) bool { return true })

	var kinds []EventKind
	final, err := l.Send("what does hello.txt say?", func(e Event) { kinds = append(kinds, e.Kind) })
	if err != nil {
		t.Fatal(err)
	}
	if !sawToolResult {
		t.Errorf("the tool result was not fed back to the model")
	}
	if !strings.Contains(final, "carrier locked") {
		t.Errorf("final answer should incorporate the tool result, got %q", final)
	}
	if calls != 2 {
		t.Errorf("expected 2 model calls (call, then answer), got %d", calls)
	}
	// The stream should include a tool call, a tool result, and a final.
	want := map[EventKind]bool{EventToolCall: false, EventToolResult: false, EventFinal: false}
	for _, k := range kinds {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("event kind %d not streamed", k)
		}
	}
}

// TestMutatingToolRequiresConfirm_DeniedNotRun: a write_file call with a DENYING
// confirmer must NOT write the file, and the model gets a "denied" result back.
func TestMutatingToolRequiresConfirm_DeniedNotRun(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	var confirmedFor string
	complete := stubCompleter(
		toolCall("w1", "write_file", `{"path":"out.txt","content":"nope"}`),
		Message{Role: "assistant", Content: "ok, I did not write it"},
	)
	deny := func(name string, _ map[string]any) bool { confirmedFor = name; return false }
	l := NewLoop(root, "sys", complete, deny)

	var denied bool
	_, err := l.Send("write out.txt", func(e Event) {
		if e.Kind == EventToolResult && e.Denied {
			denied = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmedFor != "write_file" {
		t.Errorf("confirmer should have been asked for write_file, got %q", confirmedFor)
	}
	if !denied {
		t.Errorf("a denied confirm should emit a Denied tool result")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("denied write_file must NOT create the file")
	}
}

// TestMutatingToolApprovedRuns: the same write with an APPROVING confirmer writes it.
func TestMutatingToolApprovedRuns(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	complete := stubCompleter(
		toolCall("w1", "write_file", `{"path":"out.txt","content":"roger that"}`),
		Message{Role: "assistant", Content: "wrote it"},
	)
	l := NewLoop(root, "sys", complete, func(string, map[string]any) bool { return true })
	if _, err := l.Send("write out.txt", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("approved write_file should create the file: %v", err)
	}
	if string(b) != "roger that" {
		t.Errorf("file content = %q, want %q", string(b), "roger that")
	}
}

// TestDegradesToPlainChat: a model that returns no tool_calls (a non-tool-capable
// channel, or a relay that strips tools) yields the assistant text as the final
// answer - the loop is a strict superset of plain chat.
func TestDegradesToPlainChat(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", stubCompleter(Message{Role: "assistant", Content: "just talking"}), nil)
	final, err := l.Send("hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if final != "just talking" {
		t.Errorf("plain-chat fallthrough = %q, want %q", final, "just talking")
	}
}

// TestSandboxBlocksEscape: read/write tools reject absolute paths and "../" escapes,
// so a tool call can never reach outside the cwd sandbox.
func TestSandboxBlocksEscape(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"/etc/passwd", "../secret", "a/../../b"} {
		if _, err := resolveInRoot(root, bad); err == nil {
			t.Errorf("resolveInRoot(%q) should be rejected (sandbox escape)", bad)
		}
	}
	for _, ok := range []string{"a.txt", "sub/dir/file", "."} {
		if _, err := resolveInRoot(root, ok); err != nil {
			t.Errorf("resolveInRoot(%q) should be allowed: %v", ok, err)
		}
	}
}

// TestDefaultPersonaWrittenWhenAbsent: LoadPersona writes the shipped default to disk
// when dj.md is absent, returns it, and a later load reads the (now present) file.
func TestDefaultPersonaWrittenWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rogerai", "dj.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dj.md should be absent at start")
	}
	got := LoadPersona(path)
	if !strings.Contains(got, "RogerAI") || got != DefaultPersona {
		t.Errorf("LoadPersona should return the shipped default when absent")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LoadPersona should have written dj.md: %v", err)
	}
	if string(b) != DefaultPersona {
		t.Errorf("written dj.md should be the default persona")
	}
	// A user edit is then honored (and not overwritten).
	custom := "# my own DJ\nbe terse"
	if err := os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LoadPersona(path); got != custom {
		t.Errorf("LoadPersona should return the user-edited persona, got %q", got)
	}
}

// TestDefaultPersonaSaysDontDumpToolOutput: the shipped persona must steer the model
// AWAY from re-typing long tool output verbatim (the user already sees it in the
// transcript preview), so a reasoning model spends its budget answering, not echoing.
func TestDefaultPersonaSaysDontDumpToolOutput(t *testing.T) {
	low := strings.ToLower(DefaultPersona)
	if !strings.Contains(low, "already see") {
		t.Errorf("persona should note the user already sees the tool output")
	}
	if !strings.Contains(low, "verbatim") {
		t.Errorf("persona should tell the model not to re-type tool output verbatim")
	}
	if !strings.Contains(low, "summarize") {
		t.Errorf("persona should tell the model to summarize and answer instead")
	}
}

// TestToolSchemasShape: the advertised OpenAI tools array has the right shape and
// marks exactly the mutating tools (write_file, run_shell) as such.
func TestToolSchemasShape(t *testing.T) {
	tools := BuiltinTools()
	mutating := map[string]bool{}
	for _, tl := range tools {
		mutating[tl.Name] = tl.Mutating
	}
	if !mutating["write_file"] || !mutating["run_shell"] {
		t.Errorf("write_file and run_shell must be mutating (confirm-gated)")
	}
	if mutating["read_file"] || mutating["list_dir"] || mutating["web_fetch"] {
		t.Errorf("read_file/list_dir/web_fetch must be read-only (auto-run)")
	}
	schemas := ToolSchemas(tools)
	if len(schemas) != len(tools) {
		t.Fatalf("schema count %d != tool count %d", len(schemas), len(tools))
	}
	for _, s := range schemas {
		if s["type"] != "function" {
			t.Errorf("tool schema type = %v, want function", s["type"])
		}
		fn, ok := s["function"].(map[string]any)
		if !ok || fn["name"] == "" || fn["parameters"] == nil {
			t.Errorf("tool schema missing function/name/parameters: %v", s)
		}
	}
}

// TestBrokerCompleterRequestsHigherMaxTokens: the agent turn must request the raised
// answer budget (agentMaxTokens, not the old 1024). gpt-oss-style reasoning models
// bill hidden reasoning into the SAME budget, so at 1024 the visible answer truncated
// mid-word; this asserts the value actually sent on the wire.
func TestBrokerCompleterRequestsHigherMaxTokens(t *testing.T) {
	// Keep SignRequest's key-on-first-use side effect inside the test sandbox.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var gotMaxTokens float64
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["max_tokens"].(float64); ok {
			gotMaxTokens, seen = v, true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	complete := BrokerCompleter(srv.URL, "tester", "gpt-oss-20b", false, 0, nil)
	if _, err := complete([]Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("completer error: %v", err)
	}
	if !seen {
		t.Fatalf("request did not carry a max_tokens field")
	}
	if int(gotMaxTokens) != agentMaxTokens {
		t.Errorf("agent turn requested max_tokens=%d, want %d (raised budget)", int(gotMaxTokens), agentMaxTokens)
	}
	if agentMaxTokens <= 1024 {
		t.Errorf("agentMaxTokens=%d must exceed the old 1024 ceiling that truncated answers", agentMaxTokens)
	}
}

// TestBrokerCompleterCarriesMaxOut: the [0] AGENT harness relay must carry the consumer
// out-price cap so an agent turn is bounded against overpay like `use`/chat. A 0 maxOut
// applies the default ($10); an explicit cap is honored (the opt-in-to-pay-more path).
func TestBrokerCompleterCarriesMaxOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, err := BrokerCompleter(srv.URL, "tester", "m", false, 0, nil)([]Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("completer error: %v", err)
	}
	if gotCap != "10" {
		t.Fatalf("agent relay cap header = %q, want the $10 default (harness overpay path was open)", gotCap)
	}
	if _, err := BrokerCompleter(srv.URL, "tester", "m", false, 25, nil)([]Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("completer error: %v", err)
	}
	if gotCap != "25" {
		t.Errorf("agent relay explicit cap header = %q, want 25 (opt-in to pay more)", gotCap)
	}
}

// TestParseCompletionToolCalls: a broker response carrying tool_calls is parsed into
// the assistant message's ToolCalls (the response-side passthrough).
func TestParseCompletionToolCalls(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]}}]}`
	msg, err := parseCompletion([]byte(body), 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "list_dir" {
		t.Errorf("tool_calls not parsed from the completion: %+v", msg)
	}
}

// TestParseCompletion402MapsToTopupHint: a broker 402 (insufficient balance) surfaces to
// the agent with the actionable topup next step appended (shared with client.Chat).
func TestParseCompletion402MapsToTopupHint(t *testing.T) {
	body := `{"error":{"message":"insufficient balance - add funds"}}`
	_, err := parseCompletion([]byte(body), http.StatusPaymentRequired)
	if err == nil {
		t.Fatalf("a 402 completion should be an error")
	}
	if !strings.Contains(err.Error(), client.TopupHint) {
		t.Errorf("402 agent error = %q, want it to contain the shared topup hint %q", err.Error(), client.TopupHint)
	}
}

// TestRunShellDescriptionNotSandboxed: the run_shell tool/persona copy must NOT claim it
// is sandboxed (c.Dir only sets the cwd; an approved command can escape). The over-claim
// was the bug - tighten the copy, keep default-DENY.
func TestRunShellDescriptionNotSandboxed(t *testing.T) {
	var desc string
	for _, tl := range BuiltinTools() {
		if tl.Name == "run_shell" {
			desc = tl.Description
		}
	}
	if desc == "" {
		t.Fatalf("run_shell tool not found")
	}
	low := strings.ToLower(desc)
	if !strings.Contains(low, "not sandboxed") {
		t.Errorf("run_shell description must say NOT sandboxed, got %q", desc)
	}
	// The persona must likewise not imply run_shell is sandboxed.
	plow := strings.ToLower(DefaultPersona)
	if !strings.Contains(plow, "run_shell") || !strings.Contains(plow, "not sandboxed") {
		t.Errorf("persona must note run_shell is NOT sandboxed")
	}
}

// TestAgentMaxTokensMatchesSharedConst: the agent and the in-channel chat must share one
// answer budget so they never drift apart.
func TestAgentMaxTokensMatchesSharedConst(t *testing.T) {
	if agentMaxTokens != client.MaxAnswerTokens {
		t.Errorf("agentMaxTokens=%d must equal client.MaxAnswerTokens=%d (single shared budget)", agentMaxTokens, client.MaxAnswerTokens)
	}
}
