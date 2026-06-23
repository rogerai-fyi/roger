package tokenizer

import (
	"os"
	"path/filepath"
	"strings"
)

// hf.go is the exact-HuggingFace-tokenizer path (TOKENIZER_DIR). The DIRECTORY
// LOOKUP ships now (so the registry + sidecar are wired and a present file is
// reported), but a robust pure-Go HF fast-tokenizer is heavy to vendor, so the
// actual `tokenizer.json` LOADER is a documented follow-up (the cgo
// huggingface/tokenizers path). Until then hfCount returns ok=false and Count
// falls through to the calibrated heuristic, so the build never blocks on it.
//
// Layout under TOKENIZER_DIR (one file per model, ":" / "/" in the id flattened
// to "_"): e.g. for model "meta-llama/Llama-3.3-70B-Instruct" ->
//
//	$TOKENIZER_DIR/meta-llama_Llama-3.3-70B-Instruct.json
//	$TOKENIZER_DIR/meta-llama_Llama-3.3-70B-Instruct/tokenizer.json
//
// either form is accepted.

// scanHFDir records which models have a tokenizer.json under hfDir. Best-effort:
// a missing/unreadable dir just leaves hfHave empty (heuristic fallback).
func (c *Counter) scanHFDir() {
	if c.hfDir == "" {
		return
	}
	entries, err := os.ReadDir(c.hfDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(c.hfDir, name, "tokenizer.json")); err == nil {
				c.hfHave[name] = true
			}
			continue
		}
		if strings.HasSuffix(name, ".json") {
			c.hfHave[strings.TrimSuffix(name, ".json")] = true
		}
	}
}

// flattenModel turns a model id into the on-disk key used under TOKENIZER_DIR.
func flattenModel(model string) string {
	r := strings.NewReplacer("/", "_", ":", "_")
	return r.Replace(model)
}

// hfPresent reports whether a pinned tokenizer.json exists for model.
func (c *Counter) hfPresent(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hfHave[flattenModel(model)]
}

// hfCount is the exact HF tokenizer count. It is a documented FOLLOW-UP: a
// pure-Go/cgo HF fast-tokenizer is not vendored yet, so this always reports
// ok=false and the caller uses the heuristic. The signature + lookup are in
// place so wiring the loader later is a localized change.
func (c *Counter) hfCount(model, text string) (int, bool) {
	_ = model
	_ = text
	return 0, false
}
