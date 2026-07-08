package main

import "testing"

// TestToolCallOK is the PROBE half of the tool-call capability probe
// (features/trust/toolcall_probe.feature): the pure verdict function the broker's tool-call
// canary applies to a provider's /v1/chat/completions response, the twin of evalCanary's
// fingerprint check (probe.go). It is RED against origin/main because toolCallOK does NOT
// exist yet (undefined: toolCallOK) — the not-yet-existing prober the spec approves.
//
// CONTRACT (proposed; the exact-name strictness is FOUNDER FLAG T4):
//
//	toolCallOK(body []byte, wantFn string) (ok bool, reason string)
//
// ok == true ONLY when the response carries at least one well-formed tool_calls entry: a
// non-empty function.name AND JSON-parseable function.arguments. A plain-text answer, an empty
// tool_calls array, or an unparseable body all return false (unproven stays unproven). Under the
// LENIENT default a valid tool_calls to a DIFFERENT function name still proves tool-calling
// (structure proves the provider honors the protocol); wantFn is threaded so a STRICT ruling can
// require the name match without changing the signature.
func TestToolCallOK(t *testing.T) {
	const wantFn = "roger_probe_ack"

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "well-formed single tool_call with parseable arguments",
			body: `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"roger_probe_ack","arguments":"{\"ok\":true}"}}]},"finish_reason":"tool_calls"}]}`,
			want: true,
		},
		{
			name: "empty-object arguments are still valid",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{}"}}]}}]}`,
			want: true,
		},
		{
			name: "multiple tool_calls, first well-formed",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{}"}},{"function":{"name":"other","arguments":"{}"}}]}}]}`,
			want: true,
		},
		{
			name: "lenient default: well-formed call to a DIFFERENT function still proves tool-calling (FOUNDER FLAG T4)",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"some_other_fn","arguments":"{\"x\":1}"}}]}}]}`,
			want: true,
		},
		{
			name: "plain-text answer, no tool_calls -> unproven",
			body: `{"choices":[{"message":{"content":"Sure, I will call the function."},"finish_reason":"stop"}]}`,
			want: false,
		},
		{
			name: "finish_reason tool_calls but empty array -> unproven",
			body: `{"choices":[{"message":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`,
			want: false,
		},
		{
			name: "tool_call with empty function name -> unproven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"","arguments":"{}"}}]}}]}`,
			want: false,
		},
		{
			name: "tool_call with unparseable arguments -> unproven",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{not json"}}]}}]}`,
			want: false,
		},
		{
			name: "unparseable response body -> unproven",
			body: `{"choices":[ this is not json`,
			want: false,
		},
		{
			name: "no choices at all -> unproven",
			body: `{"choices":[]}`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := toolCallOK([]byte(c.body), wantFn)
			if got != c.want {
				t.Fatalf("toolCallOK(%s) = %v (%q), want %v", c.body, got, reason, c.want)
			}
		})
	}
}
