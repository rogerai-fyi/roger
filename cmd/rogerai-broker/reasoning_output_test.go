package main

import "testing"

// TestReasoningCountsAsOutput is the regression guard for the false-ban bug: a reasoning
// model reply (answer in `reasoning`, empty `content`) must count as real output, so the
// void/empty-output + recount-over-report strikes don't fire and auto-ban honest nodes.
func TestReasoningCountsAsOutput(t *testing.T) {
	reasoningOnly := []byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning":"the answer is 42"}}]}`)
	if got := completionText(reasoningOnly); got != "the answer is 42" {
		t.Fatalf("completionText ignored reasoning: got %q", got)
	}
	if !producedUsableOutput(200, completionText(reasoningOnly), 7) {
		t.Fatal("a reasoning-only reply must be USABLE output (else it false-strikes empty-output)")
	}
	// Plain content reply is unchanged.
	contentOnly := []byte(`{"choices":[{"message":{"content":"hello"}}]}`)
	if got := completionText(contentOnly); got != "hello" {
		t.Fatalf("content reply changed: got %q", got)
	}
	// Genuinely empty (no content, no reasoning) is still flagged.
	empty := []byte(`{"choices":[{"message":{"content":""}}]}`)
	if producedUsableOutput(200, completionText(empty), 5) {
		t.Fatal("a truly empty reply must still be flagged as no-output")
	}
}
