package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// reasoning_detector_test.go is the unit half of the thinking-model output fix: the SHARED
// detector (producedUsableOutput) and the two capture functions that feed it (sseDelta /
// drainSSEDeltas on the STREAM path, completionText on the non-stream path) must recognise
// every thinking-model output signal so an honest reasoning node is never false-voided /
// false-struck / auto-banned. See features/trust/reasoning_stream_output.feature.

// TestSseDeltaCapturesThinkingSignals: the stream capture must fold content, the reasoning
// aliases (reasoning / reasoning_content / thinking), refusal, and tool/function calls - not
// just delta.content/delta.text. Dropping reasoning was the root cause of the empty capture
// that false-voided + auto-banned honest reasoning nodes.
func TestSseDeltaCapturesThinkingSignals(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string // substrings that MUST appear in the captured text
	}{
		{"content", `data: {"choices":[{"delta":{"content":"hello"}}]}`, []string{"hello"}},
		{"legacy text", `data: {"choices":[{"text":"legacy"}]}`, []string{"legacy"}},
		{"reasoning alias", `data: {"choices":[{"delta":{"reasoning":"the answer is 42"}}]}`, []string{"the answer is 42"}},
		{"reasoning_content alias", `data: {"choices":[{"delta":{"reasoning_content":"deliberating"}}]}`, []string{"deliberating"}},
		{"thinking alias", `data: {"choices":[{"delta":{"thinking":"pondering"}}]}`, []string{"pondering"}},
		{"content + reasoning", `data: {"choices":[{"delta":{"content":"final","reasoning":"scratch"}}]}`, []string{"final", "scratch"}},
		{"refusal", `data: {"choices":[{"delta":{"refusal":"I can't help with that"}}]}`, []string{"I can't help with that"}},
		{"tool_calls", `data: {"choices":[{"delta":{"content":null,"tool_calls":[{"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}}]}`, []string{"get_weather", "SF"}},
		{"function_call", `data: {"choices":[{"delta":{"function_call":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}}]}`, []string{"lookup"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sseDelta([]byte(c.line))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Fatalf("sseDelta(%s) = %q, must contain %q", c.name, got, w)
				}
			}
		})
	}
	// A keepalive comment / the [DONE] sentinel capture nothing.
	if got := sseDelta([]byte(": keepalive")); got != "" {
		t.Fatalf("keepalive must capture nothing, got %q", got)
	}
	if got := sseDelta([]byte("data: [DONE]")); got != "" {
		t.Fatalf("[DONE] must capture nothing, got %q", got)
	}
}

// TestDrainSSEDeltasFoldsReasoning: the streaming re-count reconstruction (drainSSEDeltas)
// must accumulate reasoning deltas across a multi-line SSE buffer, so the captured completion
// used by the void + re-count gates is non-empty for a reasoning stream.
func TestDrainSSEDeltasFoldsReasoning(t *testing.T) {
	var raw, out bytes.Buffer
	raw.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning\":\"step one \"}}]}\n")
	raw.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning\":\"step two \"}}]}\n")
	raw.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"the answer\"}}]}\n")
	drainSSEDeltas(&raw, &out)
	got := out.String()
	for _, w := range []string{"step one ", "step two ", "the answer"} {
		if !strings.Contains(got, w) {
			t.Fatalf("drainSSEDeltas dropped %q; captured %q", w, got)
		}
	}
}

// TestProducedUsableOutputOROfAllSignals is the SHARED void-gate predicate matrix. A request
// produced usable output when it did NOT error AND (there is any captured text OR the usage
// backstop reports completion tokens). It is voided (and the owner struck) ONLY for the
// TRUE-negative: an error, or genuinely no text AND completion_tokens==0.
func TestProducedUsableOutputOROfAllSignals(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		completion string
		claimed    int
		want       bool
	}{
		{"content present", 200, "hello", 0, true},
		{"reasoning captured as text", 200, "the answer is 42", 7, true},
		{"inline think tags in content", 200, "<think>reasoning</think>", 0, true},
		{"harmony channel markers in content", 200, "<|channel|>analysis<|message|>reasoning<|channel|>final<|message|>hi", 0, true},
		{"tool call folded into text", 200, "get_weather{\"city\":\"SF\"}", 0, true},
		{"refusal folded into text", 200, "I can't help with that", 0, true},
		{"usage backstop: empty text, tokens reported", 200, "", 5, true},
		{"whitespace only, no tokens", 200, "   ", 0, false},
		{"true-negative: empty and zero tokens", 200, "", 0, false},
		{"error status voids even with text", 502, "boom", 9, false},
		{"error status voids even with tokens", 400, "", 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := producedUsableOutput(c.status, c.completion, c.claimed); got != c.want {
				t.Fatalf("producedUsableOutput(%d, %q, %d) = %v, want %v", c.status, c.completion, c.claimed, got, c.want)
			}
		})
	}
}

// TestCompletionTextFoldsAllSignals: the non-stream extractor must fold the SAME signals as
// the stream capture (content + reasoning aliases + refusal + tool/function calls) so the
// void gate AND the re-count see identical output on both paths - the asymmetry that dropped
// reasoning on the stream path (but not here) was what stacked strikes into an auto-ban.
func TestCompletionTextFoldsAllSignals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"content", `{"choices":[{"message":{"content":"hi"}}]}`, []string{"hi"}},
		{"reasoning", `{"choices":[{"message":{"content":"","reasoning":"the answer is 42"}}]}`, []string{"the answer is 42"}},
		{"reasoning_content alias", `{"choices":[{"message":{"content":"","reasoning_content":"deliberating"}}]}`, []string{"deliberating"}},
		{"thinking alias", `{"choices":[{"message":{"content":"","thinking":"pondering"}}]}`, []string{"pondering"}},
		{"refusal", `{"choices":[{"message":{"content":"","refusal":"I can't help"}}]}`, []string{"I can't help"}},
		{"tool_calls", `{"choices":[{"message":{"content":null,"tool_calls":[{"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}}]}`, []string{"get_weather", "SF"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := completionText([]byte(c.body))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Fatalf("completionText(%s) = %q, must contain %q", c.name, got, w)
				}
			}
		})
	}
}

// TestEnsureStreamIncludeUsage: the broker requests usage on the final chunk (the usage
// backstop) by merging stream_options.include_usage=true into a streaming request, preserving
// existing keys and leaving a non-streaming request untouched.
func TestEnsureStreamIncludeUsage(t *testing.T) {
	// streaming request with no stream_options -> gains include_usage
	out := ensureStreamIncludeUsage([]byte(`{"model":"m","stream":true,"max_tokens":10}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	so, ok := m["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("include_usage not set: %s", out)
	}
	if m["model"] != "m" || m["max_tokens"].(float64) != 10 {
		t.Fatalf("existing keys not preserved: %s", out)
	}
	// existing stream_options object is preserved and augmented
	out2 := ensureStreamIncludeUsage([]byte(`{"stream":true,"stream_options":{"continuous_usage_stats":true}}`))
	_ = json.Unmarshal(out2, &m)
	so2 := m["stream_options"].(map[string]any)
	if so2["include_usage"] != true || so2["continuous_usage_stats"] != true {
		t.Fatalf("existing stream_options not merged: %s", out2)
	}
	// a NON-streaming request is left byte-for-byte unchanged
	in := []byte(`{"model":"m"}`)
	if got := ensureStreamIncludeUsage(in); string(got) != string(in) {
		t.Fatalf("non-stream request must be untouched, got %s", got)
	}
}
