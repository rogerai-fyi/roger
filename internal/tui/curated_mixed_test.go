package tui

import "testing"

// TestCuratedOnlyLooksAtEveryRowOfTheModel pins the mixed-band defect: bands are grouped by
// (model, quant), and the U refusal read only the model's FIRST row, so a model whose first
// row was its curated station was refused as "curated only" although self-hosted rows of the
// same model were on air.
func TestCuratedOnlyLooksAtEveryRowOfTheModel(t *testing.T) {
	m := model{bands: []band{
		{model: "qwen3-32b", quant: "", stations: 1, curated: 1, curatedProvider: "acme"},
		{model: "qwen3-32b", quant: "Q8_0", stations: 1},
		{model: "mistral-large", stations: 2, curated: 2, curatedProvider: "acme"},
	}}
	if _, only := m.curatedOnly("qwen3-32b"); only {
		t.Error("a mixed band (curated + self-hosted rows) was treated as curated-only")
	}
	if p, only := m.curatedOnly("mistral-large"); !only || p != "acme" {
		t.Errorf("an all-curated band must be curated-only, got only=%v provider=%q", only, p)
	}
	if _, only := m.curatedOnly("absent"); only {
		t.Error("a model not on the dial is not curated-only")
	}
}
