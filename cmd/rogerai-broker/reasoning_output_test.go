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
	// Empty text but the usage backstop reports completion tokens: NOT flagged (the model
	// produced tokens per its own usage accounting; trusting it stops the false-void that
	// auto-banned honest reasoning nodes when capture missed the text).
	empty := []byte(`{"choices":[{"message":{"content":""}}]}`)
	if !producedUsableOutput(200, completionText(empty), 5) {
		t.Fatal("empty text WITH claimed completion tokens must NOT be voided (usage backstop)")
	}
	// The TRUE-negative: genuinely empty AND completion_tokens==0 is still flagged, so the
	// strike stays useful.
	if producedUsableOutput(200, completionText(empty), 0) {
		t.Fatal("a truly empty reply with zero tokens must still be flagged as no-output")
	}
}
