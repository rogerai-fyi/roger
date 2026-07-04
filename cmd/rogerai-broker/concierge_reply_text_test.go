package main

import (
	"strings"
	"testing"
)

// TestConciergeReplyText pins the PUBLIC concierge reply extractor: it returns ONLY the
// user-facing answer and NEVER leaks the gpt-oss `reasoning` (chain-of-thought) into the
// visible reply. The real production leak: a reply whose visible `content` was the greeting
// AND whose `reasoning` was "We need to respond in character..." got both concatenated, so
// Ping showed the model's private analysis to the public homepage. conciergeReplyText must
// return the content alone; reasoning is only a FALLBACK when content is empty. It also
// normalizes the founder house style: no em/en dashes in public copy.
func TestConciergeReplyText(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		absent  string // substring that must NOT appear (leak guard); "" = skip
		noEmDsh bool   // assert no em/en dash remains
	}{
		{
			name:   "content and reasoning both present -> ONLY content (the regression)",
			body:   `{"choices":[{"message":{"role":"assistant","content":"I'm here to guide you on tuning in to RogerAI or sharing your GPU to earn - let me know!","reasoning":"We need to respond in character, short, on-topic. User asks is this real? It's off-topic. Politely decline."}}]}`,
			want:   "I'm here to guide you on tuning in to RogerAI or sharing your GPU to earn - let me know!",
			absent: "We need to respond in character",
		},
		{
			name: "empty content + reasoning present -> reasoning (fallback preserved)",
			body: `{"choices":[{"message":{"content":"","reasoning":"the answer is 42"}}]}`,
			want: "the answer is 42",
		},
		{
			name: "whitespace-only content + reasoning present -> reasoning (fallback)",
			body: `{"choices":[{"message":{"content":"   ","reasoning":"tune in with the install script"}}]}`,
			want: "tune in with the install script",
		},
		{
			name: "content only -> content",
			body: `{"choices":[{"message":{"content":"hello from Ping"}}]}`,
			want: "hello from Ping",
		},
		{
			name: "neither content nor reasoning -> empty",
			body: `{"choices":[{"message":{"content":""}}]}`,
			want: "",
		},
		{
			name: "text field as a secondary when content+reasoning empty",
			body: `{"choices":[{"message":{"content":"","reasoning":""},"text":"legacy completion text"}]}`,
			want: "legacy completion text",
		},
		{
			name: "unparseable body -> empty",
			body: `not json at all`,
			want: "",
		},
		{
			name: "multi-choice joins each choice's visible content (no reasoning leak)",
			body: `{"choices":[{"message":{"content":"first.","reasoning":"secret one"}},{"message":{"content":"second.","reasoning":"secret two"}}]}`,
			want: "first.second.",
		},
		{
			name:    "em dash in content becomes a spaced hyphen",
			body:    `{"choices":[{"message":{"content":"owners keep 70%—the platform takes 30%"}}]}`,
			want:    "owners keep 70% - the platform takes 30%",
			noEmDsh: true,
		},
		{
			name:    "en dash in content becomes a spaced hyphen",
			body:    `{"choices":[{"message":{"content":"one–two"}}]}`,
			want:    "one - two",
			noEmDsh: true,
		},
		{
			name:    "already-spaced em dash collapses to a single spaced hyphen",
			body:    `{"choices":[{"message":{"content":"share — earn"}}]}`,
			want:    "share - earn",
			noEmDsh: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := conciergeReplyText([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("conciergeReplyText:\n got  %q\n want %q", got, tc.want)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("reasoning LEAKED into visible reply: %q still contains %q", got, tc.absent)
			}
			if tc.noEmDsh && (strings.ContainsRune(got, '—') || strings.ContainsRune(got, '–')) {
				t.Fatalf("em/en dash survived normalization: %q", got)
			}
		})
	}
}

// TestCompletionTextUnchangedByConciergeFix guards the anti-fraud invariant: the recount /
// probe / void path still counts content AND reasoning together (everything the node
// generated), so the concierge fix must NOT alter completionText. A body with both fields
// still returns content+reasoning concatenated for the billing/verification path.
func TestCompletionTextUnchangedByConciergeFix(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"visible answer","reasoning":"hidden analysis"}}]}`)
	got := completionText(body)
	if !strings.Contains(got, "visible answer") || !strings.Contains(got, "hidden analysis") {
		t.Fatalf("completionText must still count content+reasoning for recount: got %q", got)
	}
}
