package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolcall_nonce_red_test.go pins the per-probe NONCE the tool-call canary carries (PR #33
// review, minor #4): the canary body was deterministic/fingerprintable, so a hostile node could
// return a CANNED well-formed tool_calls to earn the verified "tools" badge without honoring
// tools. Each probe now carries a fresh random nonce (a suffix on the tool function name + a
// token the model must echo), and toolCallOK requires the response to reference THAT nonce.
//
// RED against origin/main: toolCallOK today is LENIENT about the second arg (it only used it in
// the reason string), so a well-formed tool_calls that does NOT reference the current nonce still
// returns true. These cases assert the nonce is enforced.

// TestToolCallOKRequiresNonce: a well-formed tool_calls that does not reference THIS probe's
// nonce (a canned/replayed fingerprint) does NOT pass; one that echoes it does.
func TestToolCallOKRequiresNonce(t *testing.T) {
	const nonce = "9f3ac71b"

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "canned well-formed tool_calls that omits the current nonce -> unproven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{\"ok\":true}"}}]}}]}`,
			want: false,
		},
		{
			name: "replayed tool_calls carrying a DIFFERENT (stale) nonce -> unproven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack_deadbeef","arguments":"{\"token\":\"deadbeef\"}"}}]}}]}`,
			want: false,
		},
		{
			name: "genuine echo: nonce in the function name suffix -> proven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack_9f3ac71b","arguments":"{}"}}]}}]}`,
			want: true,
		},
		{
			name: "genuine echo: nonce in the arguments token -> proven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"whatever","arguments":"{\"token\":\"9f3ac71b\"}"}}]}}]}`,
			want: true,
		},
		{
			name: "well-formed but empty args and non-nonce name -> unproven (no nonce anywhere)",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{}"}}]}}]}`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := toolCallOK([]byte(c.body), nonce)
			if got != c.want {
				t.Fatalf("toolCallOK(%s, %q) = %v (%q), want %v", c.body, nonce, got, reason, c.want)
			}
		})
	}
}

// TestToolCanaryBodyEmbedsNonce: the canary body must carry the nonce so the model has something
// to echo - in the forced tool's function name AND as the required argument the prompt asks it to
// set. RED against origin/main: toolCanaryBody takes no nonce today.
func TestToolCanaryBodyEmbedsNonce(t *testing.T) {
	const nonce = "abc12345"
	body := toolCanaryBody("m", nonce)

	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name       string `json:"name"`
				Parameters struct {
					Properties map[string]any `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("canary body did not parse: %v", err)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want exactly one tool, got %d", len(req.Tools))
	}
	if !strings.Contains(req.Tools[0].Function.Name, nonce) {
		t.Errorf("tool function name %q does not carry the nonce %q", req.Tools[0].Function.Name, nonce)
	}
	if len(req.Tools[0].Function.Parameters.Properties) != 1 {
		t.Errorf("canary must stay single-parameter, got %d params", len(req.Tools[0].Function.Parameters.Properties))
	}
	// The prompt must instruct the model to echo the nonce, so a genuine model has a token to
	// return (the arguments-echo channel toolCallOK also accepts).
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[0].Content, nonce) {
		t.Errorf("canary prompt does not ask the model to echo the nonce: %q", req.Messages)
	}
}
