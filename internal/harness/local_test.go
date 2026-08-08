package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// LocalCompleter runs an agent turn DIRECTLY against a model on the operator's own machine
// (llama.cpp, ollama, vLLM, LM Studio - anything OpenAI-compatible), never through the
// broker. Founder ask 2026-08-07: "use my own models on the TUI agent without having to
// share them". Nothing registers, nothing is metered, and the weights never leave the box.

func TestLocalCompleterTalksStraightToTheLocalServer(t *testing.T) {
	var gotPath, gotAuth, gotSig, gotCap, gotUser string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSig = r.Header.Get("X-Roger-Sig")
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		gotUser = r.Header.Get("X-Roger-User")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello from home"}}]}`))
	}))
	defer srv.Close()

	msg, err := LocalCompleter(srv.URL+"/v1/chat/completions", "", "qwen3-vl-8b")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("LocalCompleter: %v", err)
	}
	if msg.Content != "hello from home" {
		t.Errorf("content = %q, want the local server's answer", msg.Content)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("posted to %q, want the chat URL verbatim", gotPath)
	}
	if body["model"] != "qwen3-vl-8b" {
		t.Errorf("model = %v, want the local model id", body["model"])
	}

	// NOTHING about the marketplace may ride along: no request signature (there is no
	// wallet to bill), no consumer price cap (nobody is charging), no user hint.
	if gotSig != "" {
		t.Error("a local turn must not be signed - there is no broker to authenticate to")
	}
	if gotCap != "" {
		t.Error("a local turn must carry no out-price cap - nothing is being billed")
	}
	if gotUser != "" {
		t.Error("a local turn must not announce a broker user")
	}
	if gotAuth != "" {
		t.Error("no key was configured, so no Authorization header should be sent")
	}
}

// A key-protected local server (vLLM --api-key, a LiteLLM master key, LM Studio's toggle)
// must still be reachable - detect already discovers these keys.
func TestLocalCompleterSendsTheUpstreamKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, err := LocalCompleter(srv.URL+"/v1/chat/completions", "sk-local", "m")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("LocalCompleter: %v", err)
	}
	if gotAuth != "Bearer sk-local" {
		t.Errorf("Authorization = %q, want the discovered upstream key", gotAuth)
	}
}

// Tool calls must round-trip, or the agent degrades to plain chat on local models.
func TestLocalCompleterRoundTripsToolCalls(t *testing.T) {
	var sentTools bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if tl, ok := body["tools"].([]any); ok && len(tl) > 0 {
			sentTools = true
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}}]}`))
	}))
	defer srv.Close()

	msg, err := LocalCompleter(srv.URL+"/v1/chat/completions", "", "m")(
		context.Background(), []Message{{Role: "user", Content: "hi"}},
		[]map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}})
	if err != nil {
		t.Fatalf("LocalCompleter: %v", err)
	}
	if !sentTools {
		t.Error("the tools array was not sent - the agent could never call a tool locally")
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool_calls did not round-trip: %+v", msg.ToolCalls)
	}
}

// A local server that is down must say so plainly. The operator's remedy is to start their
// server - not to go put a station on air, which is what a broker-shaped error would imply.
func TestLocalCompleterErrorNamesTheLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	_, err := LocalCompleter(dead+"/v1/chat/completions", "", "m")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("a dead local server must error")
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "local") {
		t.Errorf("the error should name the LOCAL server as the thing that failed, got %q", err)
	}
	if strings.Contains(low, "station") || strings.Contains(low, "broker") {
		t.Errorf("a local failure must not be described in marketplace terms: %q", err)
	}
}

// A non-2xx from the local server surfaces its body, which is where llama.cpp puts the
// real reason (a context overflow, a bad model id, an out-of-memory).
func TestLocalCompleterSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
	}))
	defer srv.Close()

	_, err := LocalCompleter(srv.URL+"/v1/chat/completions", "", "m")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("a 400 must be an error")
	}
	if !strings.Contains(err.Error(), "context length exceeded") {
		t.Errorf("the server's reason was lost: %q", err)
	}
}

// Cancellation (esc) must abort a local turn immediately, like a relayed one.
func TestLocalCompleterHonoursCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LocalCompleter(srv.URL+"/v1/chat/completions", "", "m")(
		ctx, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Error("a cancelled context must abort the local turn")
	}
}
