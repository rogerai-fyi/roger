package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DETECTION OF THE VARIANT FIELDS, end to end against fake runtimes.
// MODEL-VARIANTS-DESIGN-2026-08-22.

// OLLAMA GIVES THE QUANT AWAY ON A CALL WE ALREADY MAKE. /api/tags lists the fleet AND
// carries details.quantization_level per model, so reading it costs no extra request -
// the field was already on the wire and simply not being read.
func TestOllamaQuantComesFromTheFleetListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[
				{"name":"qwen3.8:27b","details":{"quantization_level":"Q4_K_M"}},
				{"name":"llama3:8b","details":{"quantization_level":"Q8_0"}},
				{"name":"mystery:latest","details":{"quantization_level":"unknown"}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := Found{Name: "ollama", BaseURL: srv.URL + "/v1"}
	mergeOllamaNative(&f, f.BaseURL)

	if f.Quant["qwen3.8:27b"] != "Q4_K_M" {
		t.Errorf("qwen quant = %q, want Q4_K_M", f.Quant["qwen3.8:27b"])
	}
	if f.Quant["llama3:8b"] != "Q8_0" {
		t.Errorf("llama quant = %q, want Q8_0", f.Quant["llama3:8b"])
	}
	// Ollama's literal "unknown" is an absence wearing a word: it must not become a label
	// a consumer could filter on.
	if got, ok := f.Quant["mystery:latest"]; ok {
		t.Errorf("\"unknown\" became the label %q", got)
	}
}

// /api/show's model_info IS the GGUF KV map - which is why the context window is read from
// "<arch>.context_length" - so general.quantized_by and general.finetune are in a response
// already being fetched.
func TestOllamaWeightsAndVariantComeFromModelInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/show":
			_, _ = w.Write([]byte(`{"details":{"quantization_level":"Q4_K_M"},
				"model_info":{"qwen3.context_length":262144,
					"general.quantized_by":"unsloth",
					"general.finetune":"thinking",
					"general.organization":"Qwen"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := Found{Name: "ollama", Models: []string{"qwen3.8:27b"}, Ctx: map[string]int{}}
	enrichOllamaCtx(&f, srv.URL)

	if f.Weights["qwen3.8:27b"] != "unsloth" {
		t.Errorf("weights = %q, want unsloth - this is the axis people argue about", f.Weights["qwen3.8:27b"])
	}
	if f.Variant["qwen3.8:27b"] != "thinking" {
		t.Errorf("variant = %q, want thinking", f.Variant["qwen3.8:27b"])
	}
	// The call it rode in on still does its original job.
	if f.Ctx["qwen3.8:27b"] != 262144 {
		t.Errorf("the context window was lost: %d", f.Ctx["qwen3.8:27b"])
	}
}

// A model that already has its ctx from /api/ps must STILL be asked, because the publisher
// metadata rides the same response - otherwise a loaded model is the one model we know
// least about.
func TestALoadedModelIsStillAskedForItsPublisher(t *testing.T) {
	shows := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.8:27b","context_length":40960}]}`))
		case "/api/show":
			shows++
			_, _ = w.Write([]byte(`{"model_info":{"general.quantized_by":"bartowski"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := Found{Name: "ollama", Models: []string{"qwen3.8:27b"}, Ctx: map[string]int{}}
	enrichOllamaCtx(&f, srv.URL)

	if shows == 0 {
		t.Fatal("a model with a known ctx was never asked about its publisher")
	}
	if f.Weights["qwen3.8:27b"] != "bartowski" {
		t.Errorf("weights = %q, want bartowski", f.Weights["qwen3.8:27b"])
	}
}

// LLAMA.CPP exposes the loaded FILE and nothing else, so the header is read off disk.
func TestLlamaCppReadsTheLoadedFilesHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Qwen3.8-27B-Q4_K_M.gguf")
	raw := ggufBytes(t, 3, []kv{
		{"general.quantized_by", ggufTypeString, "unsloth"},
		{"general.finetune", ggufTypeString, "thinking"},
		{"general.file_type", ggufTypeUint32, uint32(15)},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"default_generation_settings": map[string]any{"n_ctx": 32768},
			"model_path":                  path,
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	f := Found{Name: "llama.cpp", Models: []string{"qwen3.8-27b"}, Ctx: map[string]int{}}
	enrichLlamaCppCtx(&f, srv.URL)

	if f.Quant["qwen3.8-27b"] != "Q4_K_M" {
		t.Errorf("quant = %q, want Q4_K_M", f.Quant["qwen3.8-27b"])
	}
	if f.Weights["qwen3.8-27b"] != "unsloth" {
		t.Errorf("weights = %q, want unsloth", f.Weights["qwen3.8-27b"])
	}
	if f.Variant["qwen3.8-27b"] != "thinking" {
		t.Errorf("variant = %q, want thinking", f.Variant["qwen3.8-27b"])
	}
	if f.Ctx["qwen3.8-27b"] != 32768 {
		t.Errorf("the context window was lost: %d", f.Ctx["qwen3.8-27b"])
	}
}

// AN UNREADABLE PATH SAYS NOTHING. A model server pointed at a file we cannot open is
// ordinary - permissions, a remote mount, a deleted download - and none of it is an
// incident for an operator who only asked to share a model.
func TestAnUnreadableModelPathDetectsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":8192},
			"model_path":"/definitely/not/here/model.gguf"}`))
	}))
	defer srv.Close()

	f := Found{Name: "llama.cpp", Models: []string{"m"}, Ctx: map[string]int{}}
	enrichLlamaCppCtx(&f, srv.URL)

	if len(f.Weights) != 0 || len(f.Variant) != 0 {
		t.Errorf("an unreadable path invented metadata: weights=%v variant=%v", f.Weights, f.Variant)
	}
	// The FILE NAME is still a legitimate quant source even when the file is gone -
	// it is what the publisher called these weights.
	if f.Quant["m"] != "" {
		t.Errorf("quant from a bare path with no quant token: %q", f.Quant["m"])
	}
	if f.Ctx["m"] != 8192 {
		t.Error("the context window was lost when the header read failed")
	}
}

// The maps stay NIL until something is detected, so a Found with nothing to say carries no
// empty objects into the offer it becomes.
func TestNothingDetectedLeavesTheMapsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"plain:latest"}]}`))
	}))
	defer srv.Close()

	f := Found{Name: "ollama", BaseURL: srv.URL + "/v1"}
	mergeOllamaNative(&f, f.BaseURL)
	if f.Quant != nil || f.Weights != nil || f.Variant != nil {
		t.Errorf("maps were created with nothing in them: %v %v %v", f.Quant, f.Weights, f.Variant)
	}
	if !strings.Contains(strings.Join(f.Models, ","), "plain:latest") {
		t.Error("the model itself was lost")
	}
}
