package harness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
)

// TestBrokerCompleterConfidentialHeader: a confidential agent turn sets the
// X-Roger-Confidential header so the relay routes it to a confidential-capable node.
func TestBrokerCompleterConfidentialHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var gotConfidential string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConfidential = r.Header.Get("X-Roger-Confidential")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	comp := BrokerCompleter(srv.URL, "u", "m", true, 0, nil)
	if _, err := comp(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("completer error: %v", err)
	}
	if gotConfidential != "1" {
		t.Errorf("confidential turn header = %q, want \"1\"", gotConfidential)
	}
}

// TestBrokerCompleterUnreachable: a connection-refused broker surfaces the "could not
// reach the broker" error (not a cancel, not a timeout).
func TestBrokerCompleterUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// 127.0.0.1:1 is a privileged port nothing listens on -> immediate connrefused.
	comp := BrokerCompleter("http://127.0.0.1:1", "u", "m", false, 0, nil)
	_, err := comp(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "could not reach the broker") {
		t.Fatalf("unreachable broker err = %v, want 'could not reach the broker'", err)
	}
}

// TestBrokerCompleterTimeout: when the request times out, the completer returns the
// "no reply from the station" message (the Timeout() branch). Uses the brokerHTTPTimeout
// seam shortened to a hair against a server that blocks.
func TestBrokerCompleterTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // hang until the test releases it
	}))
	defer srv.Close()
	defer close(block)

	orig := brokerHTTPTimeout
	brokerHTTPTimeout = 20 * time.Millisecond
	defer func() { brokerHTTPTimeout = orig }()

	comp := BrokerCompleter(srv.URL, "u", "m", false, 0, nil)
	_, err := comp(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "no reply from the station") {
		t.Fatalf("timeout err = %v, want 'no reply from the station'", err)
	}
}

// TestParseCompletionStatusBranches covers parseCompletion's no-choices error arms that
// the existing tests miss: a 402 with no error body (bare topup hint), a >=400 with no
// body (status-named error), and a <400 empty response.
func TestParseCompletionStatusBranches(t *testing.T) {
	// 402, no choices, empty body -> bare topup hint (status==StatusPaymentRequired arm).
	_, err := parseCompletion([]byte("   "), http.StatusPaymentRequired)
	if err == nil || !strings.Contains(err.Error(), client.TopupHint) {
		t.Errorf("402 empty body err = %v, want topup hint", err)
	}

	// >=400, no choices, empty body -> "returned status N with no reply".
	_, err = parseCompletion([]byte("  "), http.StatusInternalServerError)
	if err == nil || !strings.Contains(err.Error(), "status 500 with no reply") {
		t.Errorf("500 empty body err = %v, want 'status 500 with no reply'", err)
	}

	// <400, no choices -> "empty response (status N)".
	_, err = parseCompletion([]byte("{}"), http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "empty response (status 200)") {
		t.Errorf("200 no-choices err = %v, want 'empty response (status 200)'", err)
	}
}

// TestSendNilContextDefaults: Send tolerates a nil context (defaults to Background) and
// still completes a plain-chat turn.
func TestSendNilContextDefaults(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", stubCompleter(Message{Role: "assistant", Content: "hi there"}), nil)
	final, err := l.Send(nil, "yo", nil) //nolint:staticcheck // nil ctx is the path under test
	if err != nil {
		t.Fatalf("Send(nil ctx): %v", err)
	}
	if final != "hi there" {
		t.Errorf("nil-ctx final = %q, want 'hi there'", final)
	}
}

// TestSendCancelBetweenSteps: a context cancelled DURING the first step (after a tool
// call is produced) stops the loop at the top of the next step - no further model call.
func TestSendCancelBetweenSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			cancel() // tool call still runs this step; the cancel is seen at the next step top
			return toolCall("c1", "read_file", `{"path":"f.txt"}`), nil
		}
		return Message{Role: "assistant", Content: "should not reach"}, nil
	}
	l := NewLoop(root, "sys", complete, func(string, map[string]any) bool { return true })

	var cancelled bool
	_, err := l.Send(ctx, "go", func(e Event) {
		if e.Kind == EventError && strings.Contains(e.Text, "cancelled") {
			cancelled = true
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("model calls = %d, want 1 (no step after cancel)", calls)
	}
	if !cancelled {
		t.Error("a between-steps cancel should emit a 'cancelled' error event")
	}
}

// TestSendCompleterErrorNotCancelled: a completer that returns a real (non-cancel) error
// surfaces it as an EventError and Send returns that error.
func TestSendCompleterErrorNotCancelled(t *testing.T) {
	boom := errors.New("station exploded")
	complete := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		return Message{}, boom
	}
	l := NewLoop(t.TempDir(), "sys", complete, nil)
	var gotErrText string
	_, err := l.Send(context.Background(), "go", func(e Event) {
		if e.Kind == EventError {
			gotErrText = e.Text
		}
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Send err = %v, want the completer error", err)
	}
	if gotErrText != "station exploded" {
		t.Errorf("error event text = %q, want 'station exploded'", gotErrText)
	}
}

// TestRunOneUnknownTool: a tool_call for a name not in the toolset feeds an "unknown
// tool" error result back (and never runs anything).
func TestRunOneUnknownTool(t *testing.T) {
	complete := stubCompleter(
		toolCall("c1", "no_such_tool", `{}`),
		Message{Role: "assistant", Content: "ok"},
	)
	l := NewLoop(t.TempDir(), "sys", complete, func(string, map[string]any) bool { return true })
	var resultText string
	var isErr bool
	_, err := l.Send(context.Background(), "go", func(e Event) {
		if e.Kind == EventToolResult {
			resultText, isErr = e.Result, e.IsError
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isErr || !strings.Contains(resultText, `unknown tool "no_such_tool"`) {
		t.Errorf("unknown-tool result = %q (isErr=%v), want an 'unknown tool' error", resultText, isErr)
	}
}

// TestLastAssistantTextEmpty: the defensive empty-return arm (no assistant message in
// the transcript) yields "". Exercised directly because the Send step-cap path always
// has at least one assistant message.
func TestLastAssistantTextEmpty(t *testing.T) {
	l := NewLoop(t.TempDir(), "", stubCompleter(), nil)
	l.messages = []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "x"}}
	if got := l.lastAssistantText(); got != "" {
		t.Errorf("lastAssistantText with no assistant = %q, want \"\"", got)
	}
}

// TestPersonaPathConfigDirUnset: with both XDG_CONFIG_HOME and HOME unset, PersonaPath
// still returns a .../rogerai/dj.md suffixed path (the degraded-env fallback arm).
func TestPersonaPathConfigDirUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if p := PersonaPath(); !strings.HasSuffix(p, filepath.Join("rogerai", "dj.md")) {
		t.Errorf("PersonaPath (unset env) = %q, want .../rogerai/dj.md", p)
	}
}

// TestTrimSpaceTrailing covers trimSpace's trailing-trim loop (content with both leading
// and trailing whitespace), which the all-whitespace case never reaches.
func TestTrimSpaceTrailing(t *testing.T) {
	if got := trimSpace("  roger that \n\t"); got != "roger that" {
		t.Errorf("trimSpace = %q, want 'roger that'", got)
	}
	if got := trimSpace("x"); got != "x" {
		t.Errorf("trimSpace(no space) = %q, want 'x'", got)
	}
}

// TestListDirBranches covers list_dir's resolveInRoot error, ReadDir error, the dir-entry
// "/" suffix, and the empty-directory message.
func TestListDirBranches(t *testing.T) {
	root := t.TempDir()
	ld := toolByName(t, "list_dir")

	// resolveInRoot error (escape).
	if _, err := ld.Run(root, map[string]any{"path": "../up"}); err == nil {
		t.Error("list_dir should reject a sandbox escape")
	}
	// ReadDir error (missing dir).
	if _, err := ld.Run(root, map[string]any{"path": "missing"}); err == nil {
		t.Error("list_dir of a missing dir should error")
	}
	// A subdirectory is listed with a trailing slash; an empty subdir reports empty.
	if err := os.Mkdir(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := ld.Run(root, map[string]any{"path": "."})
	if err != nil || !strings.Contains(out, "sub/") {
		t.Errorf("list_dir = %q/%v, want a 'sub/' entry", out, err)
	}
	empty, err := ld.Run(root, map[string]any{"path": "sub"})
	if err != nil || empty != "(empty directory)" {
		t.Errorf("list_dir(empty) = %q/%v, want '(empty directory)'", empty, err)
	}
}

// TestWriteFileErrorBranches covers write_file's resolveInRoot error, the MkdirAll
// failure (parent is a file), and the WriteFile failure (target is a directory).
func TestWriteFileErrorBranches(t *testing.T) {
	root := t.TempDir()
	wf := toolByName(t, "write_file")

	// resolveInRoot error (empty path).
	if _, err := wf.Run(root, map[string]any{"path": "", "content": "x"}); err == nil {
		t.Error("write_file with an empty path should error")
	}
	// MkdirAll failure: a regular file in the parent position.
	if err := os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := wf.Run(root, map[string]any{"path": "afile/child.txt", "content": "y"}); err == nil {
		t.Error("write_file under a file-as-dir should error (MkdirAll)")
	}
	// WriteFile failure: target path is an existing directory.
	if err := os.Mkdir(filepath.Join(root, "adir"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := wf.Run(root, map[string]any{"path": "adir", "content": "z"}); err == nil {
		t.Error("write_file onto a directory should error (WriteFile)")
	}
}

// TestResolveInRootEmpty covers the empty-path guard directly.
func TestResolveInRootEmpty(t *testing.T) {
	if _, err := resolveInRoot(t.TempDir(), "   "); err == nil {
		t.Error("resolveInRoot('  ') should error (empty path)")
	}
}

// TestRunShellEmptyOutputError covers runShell's "error with no output" arm: a command
// that exits non-zero and prints nothing returns the bare error.
func TestRunShellEmptyOutputError(t *testing.T) {
	_, err := runShell(t.TempDir(), "false")
	if err == nil {
		t.Error("runShell('false') should return the exit error when there is no output")
	}
}

// TestRunShellTimeout covers runShell's deadline-exceeded arm via the shellTimeout seam.
func TestRunShellTimeout(t *testing.T) {
	orig := shellTimeout
	shellTimeout = 20 * time.Millisecond
	defer func() { shellTimeout = orig }()

	out, err := runShell(t.TempDir(), "sleep 2")
	if err != nil {
		t.Fatalf("timed-out runShell should not return an error, got %v", err)
	}
	if !strings.Contains(out, "timed out after") {
		t.Errorf("runShell timeout out = %q, want a 'timed out after' note", out)
	}
}

// TestWebFetchNetworkError covers webFetch's request-error arm (connection refused).
func TestWebFetchNetworkError(t *testing.T) {
	if _, err := webFetch("http://127.0.0.1:1"); err == nil {
		t.Error("webFetch to a dead port should return a network error")
	}
}
