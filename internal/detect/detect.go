// Package detect finds a local OpenAI-compatible LLM server so `rogerai share`
// can make you a provider with zero config if you already run Ollama, LM Studio,
// llama.cpp, vLLM, or a LiteLLM gateway.
package detect

import (
	"encoding/json"
	"net/http"
	"time"
)

// Found is a reachable local OpenAI-compatible server discovered by Detect.
type Found struct {
	Name    string   // friendly server name (e.g. "ollama")
	BaseURL string   // .../v1
	Chat    string   // .../v1/chat/completions
	Models  []string // served model ids from GET /v1/models
}

// Common local OpenAI-compatible servers, by default port. Any server exposing
// GET /v1/models works; this just enables zero-config detection. Users can always
// point at anything with `rogerai share --upstream <url>`.
var probes = []struct{ name, base string }{
	{"ollama", "http://127.0.0.1:11434/v1"},
	{"lm-studio", "http://127.0.0.1:1234/v1"},
	{"jan", "http://127.0.0.1:1337/v1"},
	{"litellm", "http://127.0.0.1:4000/v1"},
	{"gpt4all", "http://127.0.0.1:4891/v1"},
	{"text-generation-webui/tabbyapi", "http://127.0.0.1:5000/v1"},
	{"koboldcpp", "http://127.0.0.1:5001/v1"},
	{"vllm/tgi", "http://127.0.0.1:8000/v1"},
	{"cpu-bots", "http://127.0.0.1:8060/v1"},
	{"llama.cpp/localai/llamafile", "http://127.0.0.1:8080/v1"},
	{"mlx-lm", "http://127.0.0.1:8082/v1"},
}

// Detect probes the common local endpoints and returns the reachable ones with
// their served model ids (from GET /v1/models).
func Detect() []Found {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	var out []Found
	for _, p := range probes {
		resp, err := client.Get(p.base + "/models")
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		var d struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		var models []string
		for _, m := range d.Data {
			models = append(models, m.ID)
		}
		out = append(out, Found{Name: p.name, BaseURL: p.base, Chat: p.base + "/chat/completions", Models: models})
	}
	return out
}
