package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// MLX is the ONLY way most Apple-Silicon stations serve, and it is not GGUF: there is no
// header to read and no Ollama API to ask. LM Studio's own listing is the only place the
// quant appears, which is why this path exists at all.
func TestLMStudioReportsMLXQuantAndPublisher(t *testing.T) {
	q, w := lmStudioVariants(LMStudioModel{
		ID: "qwen3-30b-a3b", Quantization: "4bit",
		Publisher: "mlx-community", CompatibilityType: "mlx",
	})
	if q != "4bit" {
		t.Fatalf("MLX quant = %q, want the published lower-case spelling %q", q, "4bit")
	}
	if w != "mlx-community" {
		t.Fatalf("publisher = %q", w)
	}
}

// The same endpoint serves GGUF on the same machine, and those keep llama.cpp's spelling.
func TestLMStudioReportsGGUFQuantUnchanged(t *testing.T) {
	q, _ := lmStudioVariants(LMStudioModel{
		ID: "qwen2.5-7b", Quantization: "Q4_K_M",
		Publisher: "lmstudio-community", CompatibilityType: "gguf",
	})
	if q != "Q4_K_M" {
		t.Fatalf("GGUF quant = %q, want Q4_K_M", q)
	}
}

// "4BIT" is a spelling no publisher uses. If one runtime reported it upper-cased and
// another lower-cased, the SAME weights would split across two dial rows.
func TestMLXCasingIsCanonicalNotJustUpperCased(t *testing.T) {
	for in, want := range map[string]string{
		"4bit": "4bit", "4BIT": "4bit", "8Bit": "8bit",
		"8bit-DWQ": "8bit-DWQ", "6bit": "6bit",
		"Q4_K_M": "Q4_K_M", "q4_k_m": "Q4_K_M", "MXFP4": "MXFP4",
	} {
		if got, _ := lmStudioVariants(LMStudioModel{Quantization: in}); got != want {
			t.Fatalf("%q canonicalised to %q, want %q", in, got, want)
		}
	}
}

// DWQ is a different RECIPE at the same bit width - folding it into "4bit" would merge
// two things people choose between, which is the exact failure this feature exists to fix.
func TestDWQIsNotFoldedIntoItsBitWidth(t *testing.T) {
	a, _ := lmStudioVariants(LMStudioModel{Quantization: "4bit"})
	b, _ := lmStudioVariants(LMStudioModel{Quantization: "4bit-DWQ"})
	if a == b {
		t.Fatalf("4bit and 4bit-DWQ collapsed to the same label %q", a)
	}
}

// A server that says nothing must yield nothing. "unknown" is a value LM Studio actually
// sends, and it is an absence wearing a word.
func TestLMStudioAbsentAndUnknownBothYieldNothing(t *testing.T) {
	for _, in := range []string{"", "   ", "unknown", "UNKNOWN"} {
		if got, _ := lmStudioVariants(LMStudioModel{Quantization: in}); got != "" {
			t.Fatalf("quantization %q became %q", in, got)
		}
	}
}

// The MLX naming convention must also be recognised in a file/model name, since that is
// how the bare mlx-lm server and the hub both spell it.
func TestMLXNamesAreRecognisedInNames(t *testing.T) {
	for in, want := range map[string]string{
		"Qwen3-30B-A3B-4bit":         "4bit",
		"Llama-3.2-3B-Instruct-8bit": "8bit",
		"Qwen3-8B-4bit-DWQ":          "4bit-DWQ",
		"Qwen3.8-27B-Q4_K_M.gguf":    "Q4_K_M",
	} {
		if got := quantInName(in); got != want {
			t.Fatalf("quantInName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The anchoring that keeps "IQ1" from becoming a quant has to hold for the bit forms too:
// a model whose NAME contains a number and "bit" is not thereby a 4-bit quant.
func TestBitFormsStillNeedSeparatorAnchoring(t *testing.T) {
	for _, in := range []string{"orbital-24bitrate", "rabbit-7b", "abit"} {
		if got := quantInName(in); got != "" {
			t.Fatalf("quantInName(%q) invented %q", in, got)
		}
	}
}

// End to end over the wire: an Apple-Silicon station serving MLX through LM Studio must
// come out of detection WITH its variant, not as a bare model id. This is the whole point
// of reading the axes from a response we were already fetching.
func TestLMStudioEnrichmentCarriesVariantsEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/models", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "qwen3-30b", "quantization": "4bit", "publisher": "mlx-community",
					"compatibility_type": "mlx", "loaded_context_length": 8192},
				{"id": "mistral-7b", "quantization": "Q4_K_M", "publisher": "lmstudio-community",
					"compatibility_type": "gguf", "max_context_length": 32768},
				{"id": "mystery", "loaded_context_length": 4096},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Found{Models: []string{"qwen3-30b", "mistral-7b", "mystery"}, Ctx: map[string]int{}}
	enrichLMStudioCtx(f, srv.URL)

	if f.Quant["qwen3-30b"] != "4bit" || f.Weights["qwen3-30b"] != "mlx-community" {
		t.Fatalf("MLX model lost its variant: q=%q w=%q", f.Quant["qwen3-30b"], f.Weights["qwen3-30b"])
	}
	if f.Quant["mistral-7b"] != "Q4_K_M" {
		t.Fatalf("GGUF model on the same server: q=%q", f.Quant["mistral-7b"])
	}
	// A model the server said nothing about stays absent - no key at all, so it
	// serialises as missing rather than as an empty claim.
	if _, ok := f.Quant["mystery"]; ok {
		t.Fatalf("undeclared model got a quant key: %q", f.Quant["mystery"])
	}
	// The context enrichment this function already did must not have regressed.
	if f.Ctx["qwen3-30b"] != 8192 || f.Ctx["mistral-7b"] != 32768 {
		t.Fatalf("ctx regressed: %v", f.Ctx)
	}
}

// "DWQ" on its own names a recipe, not a width. A quant label a consumer cannot compare
// by bit count is not a label, so it must not be extracted from a name.
func TestBareDWQIsNotAQuantLabel(t *testing.T) {
	for _, name := range []string{"Qwen3-8B-DWQ", "model-dwq", "DWQ"} {
		if got := quantInName(name); got != "" {
			t.Errorf("quantInName(%q) = %q, want none - a width-less DWQ is not a choosable quant", name, got)
		}
	}
	// With a width it is still recognised in full.
	if got := quantInName("Qwen3-8B-4bit-DWQ"); got != "4bit-DWQ" {
		t.Errorf("quantInName(4bit-DWQ) = %q", got)
	}
}

// Every MLX width LM Studio publishes is recognised, not only the four common ones.
func TestEveryMLXBitWidthIsRecognised(t *testing.T) {
	for _, w := range []string{"2bit", "3bit", "4bit", "5bit", "6bit", "7bit", "8bit"} {
		if got := quantInName("Qwen3-8B-" + w); got != w {
			t.Errorf("quantInName(%s) = %q, want %q", w, got, w)
		}
	}
}
