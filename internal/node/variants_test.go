package node

import (
	"testing"

	"rogerai.fm/roger/v6/internal/detect"
)

// THE VARIANT FIELDS MUST SURVIVE THE WHOLE PATH: detect -> ShareRow -> the offer a node
// registers. A field detected and then dropped anywhere in the middle is worse than one
// never detected, because every surface downstream renders "absent" for a model we in fact
// know all about. MODEL-VARIANTS-DESIGN-2026-08-22.

func TestDetectedVariantsReachTheShareRows(t *testing.T) {
	c := New(Config{Broker: "http://broker.local", Station: "amber-fox"})
	c.LoadRowsNoPersist([]detect.Found{{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Chat:   "http://127.0.0.1:11434/v1/chat/completions",
		Models: []string{"qwen3.8:27b", "plain:latest"},
		Ctx:    map[string]int{"qwen3.8:27b": 262144},
		Quant:  map[string]string{"qwen3.8:27b": "Q4_K_M"},
		// The publisher axes are OPTIONAL in GGUF and often absent - the second model has
		// none, and must come through blank rather than borrowing the first model's.
		Weights: map[string]string{"qwen3.8:27b": "unsloth"},
		Variant: map[string]string{"qwen3.8:27b": "thinking"},
	}})

	byModel := map[string]ShareRow{}
	for _, r := range c.Rows() {
		byModel[r.Model] = r
	}
	got := byModel["qwen3.8:27b"]
	if got.Quant != "Q4_K_M" || got.Weights != "unsloth" || got.Variant != "thinking" {
		t.Errorf("the detected variants did not reach the row: %+v", got)
	}
	// A model with nothing detected carries nothing - never the neighbour's values.
	plain := byModel["plain:latest"]
	if plain.Quant != "" || plain.Weights != "" || plain.Variant != "" {
		t.Errorf("a model with no metadata borrowed some: %+v", plain)
	}
}

// A runtime that reports nothing at all leaves every row blank, rather than the maps being
// absent turning into a panic on lookup.
func TestNoDetectedVariantsIsNotAnError(t *testing.T) {
	c := New(Config{Broker: "http://broker.local", Station: "amber-fox"})
	c.LoadRowsNoPersist([]detect.Found{{
		Name: "llama.cpp", Chat: "http://127.0.0.1:8080/v1/chat/completions",
		Models: []string{"m1"},
	}})
	rows := c.Rows()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Quant != "" || rows[0].Weights != "" || rows[0].Variant != "" {
		t.Errorf("nil maps produced values: %+v", rows[0])
	}
}
