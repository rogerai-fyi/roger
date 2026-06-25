package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveCtxPrefersRealOverDefault: a model with a detected window resolves to
// that window (not estimated); a model with none falls back to the default AND is
// flagged estimated, so a guess is never presented as a measured value.
func TestResolveCtxPrefersRealOverDefault(t *testing.T) {
	ctx := map[string]int{"llama-3.1-8b": 131072}

	if n, est := ResolveCtx(ctx, "llama-3.1-8b"); n != 131072 || est {
		t.Errorf("detected ctx: got (%d, est=%v), want (131072, false)", n, est)
	}
	if n, est := ResolveCtx(ctx, "unknown-model"); n != DefaultCtx || !est {
		t.Errorf("missing ctx: got (%d, est=%v), want (%d, true)", n, est, DefaultCtx)
	}
	if n, est := ResolveCtx(nil, "anything"); n != DefaultCtx || !est {
		t.Errorf("nil map: got (%d, est=%v), want (%d, true)", n, est, DefaultCtx)
	}
	// A zero/negative reported window is NOT a real window: it must fall back + flag.
	if n, est := ResolveCtx(map[string]int{"m": 0}, "m"); n != DefaultCtx || !est {
		t.Errorf("zero ctx: got (%d, est=%v), want (%d, true)", n, est, DefaultCtx)
	}
}

// TestBucketGPUCount: the count collapses to a privacy-safe class - 2+ -> multi-gpu,
// 1 -> single-gpu, 0 -> cpu - never the exact count past 1.
func TestBucketGPUCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{{0, HWCPU}, {1, HWSingleGPU}, {2, HWMultiGPU}, {4, HWMultiGPU}, {8, HWMultiGPU}}
	for _, c := range cases {
		if got := BucketGPUCount(c.n); got != c.want {
			t.Errorf("BucketGPUCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	// The class for a 4-GPU rig must NOT reveal the count (no "4" anywhere).
	if got := BucketGPUCount(4); strings.Contains(got, "4") {
		t.Errorf("class %q leaks the exact count", got)
	}
}

// TestCountNvidiaSMI: each non-empty CSV line is one GPU; the per-GPU name/VRAM are
// never returned (only the count crosses into the class).
func TestCountNvidiaSMI(t *testing.T) {
	out := "NVIDIA RTX PRO 4500, 131072 MiB\nNVIDIA RTX PRO 4500, 131072 MiB\nNVIDIA RTX PRO 4500, 131072 MiB\nNVIDIA RTX PRO 4500, 131072 MiB\n"
	if n := CountNvidiaSMI(out); n != 4 {
		t.Errorf("CountNvidiaSMI = %d, want 4", n)
	}
	if n := CountNvidiaSMI(""); n != 0 {
		t.Errorf("empty CountNvidiaSMI = %d, want 0", n)
	}
	// The function returns only an int - bucketing it gives a class with no rig detail.
	if cls := BucketGPUCount(CountNvidiaSMI(out)); cls != HWMultiGPU {
		t.Errorf("4x RTX PRO 4500 -> %q, want multi-gpu (no exact rig)", cls)
	}
}

// TestCountROCmSMI: GPU[n] index markers are counted distinctly.
func TestCountROCmSMI(t *testing.T) {
	out := "GPU[0]\t: Card series: AMD Radeon\nGPU[1]\t: Card series: AMD Radeon\n"
	if n := CountROCmSMI(out); n != 2 {
		t.Errorf("CountROCmSMI = %d, want 2", n)
	}
}

// TestEnrichOllamaCtx: an Ollama /api/ps runtime num_ctx is read as the REAL served
// window, overriding the 32768 default the model would otherwise fall back to.
func TestEnrichOllamaCtx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen3:8b", "context_length": 40960},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Found{Models: []string{"qwen3:8b"}, Ctx: map[string]int{}}
	enrichOllamaCtx(f, srv.URL)
	if f.Ctx["qwen3:8b"] != 40960 {
		t.Errorf("ollama runtime ctx = %d, want 40960", f.Ctx["qwen3:8b"])
	}
	// And ResolveCtx now treats it as real (not estimated).
	if n, est := ResolveCtx(f.Ctx, "qwen3:8b"); n != 40960 || est {
		t.Errorf("resolved = (%d, est=%v), want (40960, false)", n, est)
	}
}

// TestEnrichOllamaShowCtx: a model not loaded (no /api/ps) gets its trained window
// from /api/show .model_info["<arch>.context_length"].
func TestEnrichOllamaShowCtx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_info": map[string]any{"qwen3.context_length": 262144},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Found{Models: []string{"qwen3:8b"}, Ctx: map[string]int{}}
	enrichOllamaCtx(f, srv.URL)
	if f.Ctx["qwen3:8b"] != 262144 {
		t.Errorf("ollama trained ctx = %d, want 262144", f.Ctx["qwen3:8b"])
	}
}

// TestEnrichLlamaCppCtx: GET /props default_generation_settings.n_ctx is the real
// loaded window and applies to the served model.
func TestEnrichLlamaCppCtx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"default_generation_settings": map[string]any{"n_ctx": 16384},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Found{Models: []string{"gpt-oss-20b"}, Ctx: map[string]int{}}
	enrichLlamaCppCtx(f, srv.URL)
	if f.Ctx["gpt-oss-20b"] != 16384 {
		t.Errorf("llama.cpp /props ctx = %d, want 16384", f.Ctx["gpt-oss-20b"])
	}
}

// TestEnrichLMStudioCtx: /api/v0/models prefers loaded_context_length over max.
func TestEnrichLMStudioCtx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/models", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "mistral-7b", "loaded_context_length": 8192, "max_context_length": 32768},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Found{Models: []string{"mistral-7b"}, Ctx: map[string]int{}}
	enrichLMStudioCtx(f, srv.URL)
	if f.Ctx["mistral-7b"] != 8192 {
		t.Errorf("lm-studio loaded ctx = %d, want 8192 (loaded preferred over max)", f.Ctx["mistral-7b"])
	}
}
