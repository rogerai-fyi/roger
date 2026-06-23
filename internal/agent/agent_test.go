package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bownux/rogerai/internal/protocol"
)

func TestWithUsageOption(t *testing.T) {
	// A normal request gains stream_options.include_usage without losing fields.
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	out := withUsageOption(in)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if _, ok := m["model"]; !ok {
		t.Error("model field dropped")
	}
	var so struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if err := json.Unmarshal(m["stream_options"], &so); err != nil || !so.IncludeUsage {
		t.Errorf("stream_options.include_usage not set: %s", m["stream_options"])
	}
}

func TestWithUsageOptionOverwrites(t *testing.T) {
	// An existing stream_options is replaced (we must guarantee include_usage).
	in := []byte(`{"model":"m","stream_options":{"include_usage":false}}`)
	out := withUsageOption(in)
	var m struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if !m.StreamOptions.IncludeUsage {
		t.Error("include_usage should be forced true")
	}
}

func TestWithUsageOptionInvalidJSON(t *testing.T) {
	// Non-JSON bodies are returned unchanged (don't corrupt the upstream request).
	in := []byte(`not json`)
	if got := withUsageOption(in); string(got) != string(in) {
		t.Errorf("invalid JSON should pass through unchanged, got %q", got)
	}
}

// TestRegisterStatus confirms register() surfaces a broker rejection: a non-200
// response is an error (so Run won't spin up poll loops), while 200 succeeds.
func TestRegisterStatus(t *testing.T) {
	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer srv.Close()
		if err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"}); err == nil {
			t.Error("register should error on a non-200 broker response")
		}
	})
	t.Run("200 succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		if err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"}); err != nil {
			t.Errorf("register should succeed on 200, got %v", err)
		}
	})
}

func TestParseUsage(t *testing.T) {
	cases := []struct {
		name         string
		line         string
		wantP, wantC int
		wantOK       bool
	}{
		{"sse usage chunk", `data: {"id":"x","usage":{"prompt_tokens":12,"completion_tokens":34}}`, 12, 34, true},
		{"plain json", `{"usage":{"prompt_tokens":5,"completion_tokens":0}}`, 5, 0, true},
		{"no usage", `data: {"choices":[{"delta":{"content":"hi"}}]}`, 0, 0, false},
		{"zero usage ignored", `data: {"usage":{"prompt_tokens":0,"completion_tokens":0}}`, 0, 0, false},
		{"no brace", `data: [DONE]`, 0, 0, false},
		{"empty", ``, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, comp, ok := parseUsage([]byte(c.line))
			if ok != c.wantOK || p != c.wantP || comp != c.wantC {
				t.Errorf("parseUsage(%q) = %d,%d,%v want %d,%d,%v", c.line, p, comp, ok, c.wantP, c.wantC, c.wantOK)
			}
		})
	}
}
