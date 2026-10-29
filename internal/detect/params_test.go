package detect

import "testing"

func TestParamsFromID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want float64
		ok   bool
	}{
		{"llama-3.3-70b", 70, true},
		{"qwen3-32b", 32, true},
		{"qwen3-30b-a3b", 30, true},
		{"gpt-oss-120b", 120, true},
		{"gpt-oss-20b", 20, true},
		{"gemma-3-27b-it", 27, true},
		{"deepseek-v3-0324", 671, true},
		{"mistral-small-3.1-24b", 24, true},
		{"phi-4-mini-3.8b", 3.8, true},
		{"qwen2.5-0.5b", 0.5, true},
		{"wave-pico-293m", 0.293, true},
		{"Qwen3-32B", 32, true},
		{"my-custom-finetune", 0, false},
		{"llama3:8b", 8, true},
		{"qwen3-a3b", 0, false},
		{"", 0, false},
	} {
		got, ok := ParamsFromID(tc.id)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParamsFromID(%q) = %v, %v; want %v, %v", tc.id, got, ok, tc.want, tc.ok)
		}
	}
}
