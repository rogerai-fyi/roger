// Package tokenizer is the hybrid, model-aware token counter that backs the
// tokenizer-sidecar and the broker's L1 independent re-count (see
// docs-internal/VERIFICATION-DESIGN.md, "L1 - Independent token re-count").
//
// It never trusts the node's self-reported `usage` block. Instead it re-counts
// completion (and prompt) text broker-side with the canonical tokenizer for the
// claimed model, so billing/trust can be reconciled against an independent count.
//
// Hybrid strategy, exact-first:
//  1. tiktoken (pure Go, github.com/tiktoken-go/tokenizer) for OpenAI/GPT-family
//     and gpt-oss models -> EXACT counts, no weights, microseconds.
//  2. a pinned HuggingFace `tokenizer.json` under TOKENIZER_DIR for other
//     families, when present -> exact (the loader is a follow-up; the lookup +
//     wiring ship now, see LoadHFDir).
//  3. a calibrated bytes-per-token HEURISTIC otherwise -> approximate, marked
//     exact=false, used only as an outlier gate (never silently trusted).
package tokenizer

import (
	"strings"
	"sync"

	tk "github.com/tiktoken-go/tokenizer"
)

// Result is one re-count outcome: the token count and whether it came from an
// exact tokenizer (true) or the bounded heuristic fallback (false).
type Result struct {
	Tokens int
	Exact  bool
	// Method names the path taken ("tiktoken:<enc>", "hf:<model>", "heuristic")
	// for logging / debugging; not load-bearing for billing.
	Method string
}

// heuristicBytesPerToken is the calibrated average bytes-per-token used when no
// exact tokenizer is available. ~3.6-4.0 for English on SentencePiece/BPE
// families; we pick a slightly conservative 3.7 so the estimate does not
// systematically UNDER-count (which would falsely flag honest nodes). This is an
// outlier gate, not a billing source.
const heuristicBytesPerToken = 3.7

// Counter is a hybrid tokenizer. It is safe for concurrent use.
type Counter struct {
	mu     sync.Mutex
	cache  map[tk.Encoding]tk.Codec // memoized tiktoken codecs
	hfDir  string                   // TOKENIZER_DIR for HF tokenizer.json files (optional)
	hfHave map[string]bool          // model -> tokenizer.json present (best-effort lookup)
}

// New builds a Counter. hfDir (TOKENIZER_DIR, may be "") is scanned for
// per-model `tokenizer.json` files used by the (follow-up) exact HF path.
func New(hfDir string) *Counter {
	c := &Counter{
		cache:  map[tk.Encoding]tk.Codec{},
		hfDir:  strings.TrimSpace(hfDir),
		hfHave: map[string]bool{},
	}
	c.scanHFDir()
	return c
}

// Count re-tokenizes text with the canonical tokenizer for model. It always
// returns a usable count: exact when a real tokenizer matched, otherwise the
// calibrated heuristic with Exact=false.
func (c *Counter) Count(model, text string) Result {
	if text == "" {
		return Result{Tokens: 0, Exact: true, Method: "empty"}
	}
	// 1. tiktoken-exact for GPT / gpt-oss / OpenAI-family ids.
	if enc, ok := tiktokenEncodingFor(model); ok {
		if n, err := c.tiktokenCount(enc, text); err == nil {
			return Result{Tokens: n, Exact: true, Method: "tiktoken:" + string(enc)}
		}
	}
	// 2. pinned HF tokenizer.json (exact) - lookup wired now; loader is a
	//    follow-up (documented), so a present file still falls through to the
	//    heuristic until LoadHFDir is implemented.
	if c.hfPresent(model) {
		if n, ok := c.hfCount(model, text); ok {
			return Result{Tokens: n, Exact: true, Method: "hf:" + model}
		}
	}
	// 3. calibrated heuristic - bounded estimate, never exact.
	return Result{Tokens: heuristicCount(text), Exact: false, Method: "heuristic"}
}

func (c *Counter) tiktokenCount(enc tk.Encoding, text string) (int, error) {
	c.mu.Lock()
	codec := c.cache[enc]
	c.mu.Unlock()
	if codec == nil {
		got, err := tk.Get(enc)
		if err != nil {
			return 0, err
		}
		c.mu.Lock()
		c.cache[enc] = got
		c.mu.Unlock()
		codec = got
	}
	return codec.Count(text)
}

// heuristicCount estimates tokens from bytes with the calibrated ratio, with a
// floor of 1 for any non-empty text.
func heuristicCount(text string) int {
	n := int(float64(len(text))/heuristicBytesPerToken + 0.5)
	if n < 1 {
		n = 1
	}
	return n
}

// tiktokenEncodingFor maps a model id to a tiktoken encoding when the model is
// in the GPT / gpt-oss / OpenAI-tokenizer family. Matching is by lowercase
// substring so provider-prefixed ids ("openai/gpt-4o", "gpt-oss-120b") still
// resolve. Returns ok=false for non-tiktoken families (Llama/Qwen/Mistral/...).
func tiktokenEncodingFor(model string) (tk.Encoding, bool) {
	m := strings.ToLower(model)
	switch {
	// o200k_base: GPT-4o, GPT-4.1, o1/o3/o4, and gpt-oss (OpenAI's open-weight
	// models share the o200k tokenizer family).
	case strings.Contains(m, "gpt-4o"),
		strings.Contains(m, "gpt-4.1"),
		strings.Contains(m, "gpt-5"),
		strings.Contains(m, "gpt-oss"),
		strings.Contains(m, "o1"),
		strings.Contains(m, "o3"),
		strings.Contains(m, "o4"),
		strings.Contains(m, "omni"):
		return tk.O200kBase, true
	// cl100k_base: GPT-4, GPT-3.5-turbo, text-embedding-3.
	case strings.Contains(m, "gpt-4"),
		strings.Contains(m, "gpt-3.5"),
		strings.Contains(m, "gpt-35"),
		strings.Contains(m, "text-embedding-3"),
		strings.Contains(m, "text-embedding-ada"):
		return tk.Cl100kBase, true
	}
	return "", false
}
