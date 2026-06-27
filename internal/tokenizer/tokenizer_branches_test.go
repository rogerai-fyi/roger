package tokenizer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCountHFPresentFallsThroughToHeuristic exercises the Count branch where a
// pinned tokenizer.json IS present (hfPresent true) but the loader is still a
// follow-up (hfCount returns ok=false), so Count must fall through to the
// calibrated heuristic and report Exact=false / Method "heuristic". This is the
// branch the existing TestHFDirLookup never drives through Count itself.
func TestCountHFPresentFallsThroughToHeuristic(t *testing.T) {
	dir := t.TempDir()
	model := "meta-llama/Llama-3.3-70B-Instruct"
	if err := os.WriteFile(filepath.Join(dir, flattenModel(model)+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	c := New(dir)
	if !c.hfPresent(model) {
		t.Fatalf("precondition: hfPresent(%s) = false, want true", model)
	}
	text := "The quick brown fox jumps over the lazy dog."
	r := c.Count(model, text)
	if r.Exact {
		t.Errorf("hf-present but loader unimplemented: Exact=true, want false")
	}
	if r.Method != "heuristic" {
		t.Errorf("Method = %q, want heuristic", r.Method)
	}
	want := heuristicCount(text)
	if r.Tokens != want {
		t.Errorf("Tokens = %d, want %d (heuristic)", r.Tokens, want)
	}
}

// TestHFCountIsUnimplemented pins the documented follow-up contract: hfCount
// always reports ok=false (0 tokens) until the real loader is vendored. Asserted
// directly so the contract is a permanent regression guard if someone wires a
// loader without updating callers.
func TestHFCountIsUnimplemented(t *testing.T) {
	c := New("")
	n, ok := c.hfCount("meta-llama/Llama-3.3-70B-Instruct", "some text here")
	if ok {
		t.Errorf("hfCount ok=true, want false (loader is a follow-up)")
	}
	if n != 0 {
		t.Errorf("hfCount n=%d, want 0", n)
	}
}

// TestHeuristicFloor covers the floor branch of heuristicCount: any non-empty
// text whose byte length rounds below 1 token must still count as 1 (so a
// single-byte completion is never billed/gated as 0 tokens). "x" -> 1/3.7+0.5 =
// 0.77 -> int 0 -> floored to 1.
func TestHeuristicFloor(t *testing.T) {
	if got := heuristicCount("x"); got != 1 {
		t.Errorf("heuristicCount(%q) = %d, want 1 (floor)", "x", got)
	}
	if got := heuristicCount("ab"); got != 1 {
		t.Errorf("heuristicCount(%q) = %d, want 1 (floor)", "ab", got)
	}
	// Drive it end-to-end through Count on a non-tiktoken model too.
	r := New("").Count("mistralai/Mistral-7B", "x")
	if r.Tokens != 1 || r.Exact {
		t.Errorf("Count single byte = {%d,%v}, want {1,false}", r.Tokens, r.Exact)
	}
}

// TestHeuristicCountExactRatio checks the non-floored arithmetic for a longer
// string so both the rounding (+0.5) and the division are pinned with a concrete
// expected number, not just the floor branch.
func TestHeuristicCountExactRatio(t *testing.T) {
	// 37 bytes / 3.7 = 10.0 exactly, +0.5 -> 10 (int).
	text := "0123456789012345678901234567890123456" // 37 bytes
	if len(text) != 37 {
		t.Fatalf("test setup: len=%d, want 37", len(text))
	}
	if got := heuristicCount(text); got != 10 {
		t.Errorf("heuristicCount(37 bytes) = %d, want 10", got)
	}
}

// TestCl100kVsO200kRouting asserts the two distinct exact encodings are actually
// selected (not just "some tiktoken"), pinning the Method string for each family.
func TestCl100kVsO200kRouting(t *testing.T) {
	c := New("")
	if m := c.Count("gpt-4o", "hello world").Method; m != "tiktoken:o200k_base" {
		t.Errorf("gpt-4o Method = %q, want tiktoken:o200k_base", m)
	}
	if m := c.Count("gpt-4", "hello world").Method; m != "tiktoken:cl100k_base" {
		t.Errorf("gpt-4 Method = %q, want tiktoken:cl100k_base", m)
	}
	if m := c.Count("text-embedding-ada-002", "hello world").Method; m != "tiktoken:cl100k_base" {
		t.Errorf("ada Method = %q, want tiktoken:cl100k_base", m)
	}
}

// TestTiktokenCodecCaching exercises the memoization path in tiktokenCount: the
// second call for the same encoding must hit the cached codec (covers both the
// cache-miss-then-store and the cache-hit branches) and return a stable count.
func TestTiktokenCodecCaching(t *testing.T) {
	c := New("")
	first, err := c.tiktokenCount("o200k_base", "hello world")
	if err != nil {
		t.Fatalf("first tiktokenCount: %v", err)
	}
	second, err := c.tiktokenCount("o200k_base", "hello world")
	if err != nil {
		t.Fatalf("second tiktokenCount: %v", err)
	}
	if first != second {
		t.Errorf("cached count drift: %d != %d", first, second)
	}
	if first < 1 || first > 5 {
		t.Errorf("'hello world' o200k = %d, want small (1..4)", first)
	}
}

// TestTiktokenCountUnknownEncoding covers tiktokenCount's tk.Get error arm: an encoding
// the tiktoken codec does not know is a real invalid input to a real boundary, so it
// surfaces a non-nil error (and a zero count) rather than panicking.
func TestTiktokenCountUnknownEncoding(t *testing.T) {
	c := New("")
	if n, err := c.tiktokenCount("bogus-not-a-real-encoding", "hello"); err == nil {
		t.Errorf("tiktokenCount(unknown encoding) = %d/nil, want a non-nil error", n)
	}
}

// TestScanHFDirUnreadable covers the early-return path of scanHFDir when the
// configured dir does not exist (ReadDir error): no panic, no detected models.
func TestScanHFDirUnreadable(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if c.hfPresent("anything/at-all") {
		t.Errorf("hfPresent on missing dir = true, want false")
	}
}

// TestScanHFDirIgnoresNonJSONAndDirsWithoutTokenizer covers the loop branches in
// scanHFDir: a non-.json file is ignored, and a subdirectory lacking
// tokenizer.json is not recorded.
func TestScanHFDirIgnoresNonJSONAndDirsWithoutTokenizer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "empty-model"), 0700); err != nil {
		t.Fatal(err)
	}
	c := New(dir)
	if c.hfPresent("README.txt") || c.hfPresent("README") {
		t.Errorf("non-json file should not be detected as a model")
	}
	if c.hfPresent("empty-model") {
		t.Errorf("subdir without tokenizer.json should not be detected")
	}
}
