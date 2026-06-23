package tokenizer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTiktokenExact: GPT / gpt-oss / OpenAI-family models re-count EXACTLY via
// tiktoken (no weights), so Exact is true and the count is a real BPE count.
func TestTiktokenExact(t *testing.T) {
	c := New("")
	for _, model := range []string{"gpt-4o", "openai/gpt-4o-mini", "gpt-oss-120b", "gpt-4", "gpt-3.5-turbo"} {
		r := c.Count(model, "The quick brown fox jumps over the lazy dog.")
		if !r.Exact {
			t.Errorf("%s: exact=false, want exact (tiktoken)", model)
		}
		if r.Tokens <= 0 {
			t.Errorf("%s: tokens=%d, want >0", model, r.Tokens)
		}
	}
	// A known short string under cl100k/o200k tokenizes to a small, stable count.
	if got := c.Count("gpt-4o", "hello world").Tokens; got < 1 || got > 5 {
		t.Errorf("'hello world' o200k tokens = %d, want small (1..4)", got)
	}
}

// TestHeuristicFallback: a non-tiktoken family with no pinned tokenizer.json
// falls back to the calibrated heuristic and is marked Exact=false.
func TestHeuristicFallback(t *testing.T) {
	c := New("")
	r := c.Count("meta-llama/Llama-3.3-70B-Instruct", "The quick brown fox jumps over the lazy dog.")
	if r.Exact {
		t.Errorf("llama with no tokenizer.json: exact=true, want false (heuristic)")
	}
	if r.Tokens <= 0 {
		t.Errorf("heuristic tokens = %d, want >0", r.Tokens)
	}
	if r.Method != "heuristic" {
		t.Errorf("method = %q, want heuristic", r.Method)
	}
	// The heuristic must be in a sane ballpark of the byte length (chars/~3.7).
	text := "The quick brown fox jumps over the lazy dog."
	want := int(float64(len(text))/heuristicBytesPerToken + 0.5)
	if r.Tokens != want {
		t.Errorf("heuristic tokens = %d, want %d", r.Tokens, want)
	}
}

// TestEmptyText: empty text is 0 tokens and exact (no estimation needed).
func TestEmptyText(t *testing.T) {
	r := New("").Count("anything", "")
	if r.Tokens != 0 || !r.Exact {
		t.Errorf("empty text = {%d,%v}, want {0,true}", r.Tokens, r.Exact)
	}
}

// TestHFDirLookup: a present tokenizer.json under TOKENIZER_DIR is DETECTED
// (hfPresent true). The exact loader is a follow-up, so Count still falls through
// to the heuristic for now - the test asserts detection wiring, not exactness.
func TestHFDirLookup(t *testing.T) {
	dir := t.TempDir()
	model := "meta-llama/Llama-3.3-70B-Instruct"
	flat := flattenModel(model)
	if err := os.WriteFile(filepath.Join(dir, flat+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	c := New(dir)
	if !c.hfPresent(model) {
		t.Errorf("hfPresent(%s) = false, want true (tokenizer.json present)", model)
	}
	// Directory layout variant: <flat>/tokenizer.json.
	model2 := "Qwen/Qwen2.5-72B"
	sub := filepath.Join(dir, flattenModel(model2))
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "tokenizer.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if !New(dir).hfPresent(model2) {
		t.Errorf("hfPresent(%s) = false, want true (subdir tokenizer.json)", model2)
	}
}

// TestEncodingMapping: the model->encoding map routes families correctly.
func TestEncodingMapping(t *testing.T) {
	cases := map[string]bool{ // model -> expect tiktoken
		"gpt-4o":                   true,
		"gpt-oss-20b":              true,
		"o3-mini":                  true,
		"gpt-3.5-turbo":            true,
		"meta-llama/Llama-3.3-70B": false,
		"mistralai/Mistral-7B":     false,
		"qwen2.5-coder":            false,
	}
	for model, wantTik := range cases {
		_, ok := tiktokenEncodingFor(model)
		if ok != wantTik {
			t.Errorf("tiktokenEncodingFor(%s) ok=%v, want %v", model, ok, wantTik)
		}
	}
}
