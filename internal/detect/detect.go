// Package detect finds local OpenAI-compatible LLM servers so `rogerai share`
// can make you a provider with zero config if you already run Ollama, LM Studio,
// llama.cpp, vLLM, Jan, LiteLLM, or anything else that serves /v1/models.
//
// Detection v2 is grounded (no brute port scan, no assumptions about one fixed
// setup). It gathers candidate base URLs from, in order:
//
//	(a) documented default endpoints (Ollama 11434, LM Studio 1234, vLLM 8000,
//	    llama.cpp 8080, Jan 1337, ...) - the `probes` table;
//	(b) environment variables a user's tooling already exports
//	    (OPENAI_BASE_URL / OPENAI_API_BASE, OLLAMA_HOST, LMSTUDIO_* );
//	(c) native fleet discovery for Ollama (GET /api/tags + /api/ps) so models
//	    that are installed-but-swapped-out still show up;
//	(d) REAL listening-port enumeration (a build-tagged, cross-platform helper:
//	    Linux /proc/net/tcp, macOS lsof, Windows netstat) that lists the actual
//	    open localhost ports, so a model on any custom port (e.g. :8081) is found
//	    WITHOUT a brute scan;
//	(e) an explicit endpoint the caller passes in (--upstream / a saved config).
//
// Every candidate base URL is probed for GET /v1/models; only reachable,
// OpenAI-compatible servers are returned. Results are de-duplicated by base URL.
package detect

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Found is a reachable local OpenAI-compatible server discovered by Detect.
type Found struct {
	Name    string         // friendly server name (e.g. "ollama")
	BaseURL string         // .../v1
	Chat    string         // .../v1/chat/completions
	Models  []string       // served model ids from GET /v1/models (+ native discovery)
	Ctx     map[string]int // per-model context length when the server reports it
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

// httpClient is the short-timeout probe client. Detection must be fast (it gates
// the first paint of /share), so we give each probe a tight budget.
var httpClient = &http.Client{Timeout: 1500 * time.Millisecond}

// maxEnumPorts caps how many real listening ports the cross-platform enumerator
// returns, so a host with hundreds of open ports can't blow up the probe fan-out.
// The documented defaults + env vars already cover the common servers; this is the
// "found it on a custom port" tail, which is small in practice.
const maxEnumPorts = 64

// candidate is a base URL to probe, with a friendly label for the source.
type candidate struct{ name, base string }

// enumPorts / envCands are indirections over the real listening-port enumerator
// and the env-var source, so tests can make detection deterministic (the host's
// own open ports must not leak into a unit test's result). Production uses the
// real implementations.
var (
	enumPorts = listeningPorts
	envCands  = envCandidates
)

// Detect gathers candidate endpoints from every source (defaults, env, Ollama
// native, real listening ports), probes each for GET /v1/models, and returns the
// reachable OpenAI-compatible servers, de-duplicated by base URL.
func Detect() []Found {
	return detectWith(nil)
}

// DetectWith is Detect plus explicit extra base URLs to probe first (the (e)
// source: --upstream / a saved config endpoint). Each is normalized to a /v1
// base. The explicit endpoints win on de-dup so their friendly name is kept.
func DetectWith(extra ...string) []Found {
	cands := make([]candidate, 0, len(extra))
	for _, u := range extra {
		if b := toV1Base(u); b != "" {
			cands = append(cands, candidate{name: "configured", base: b})
		}
	}
	return detectWith(cands)
}

// Probe verifies that a single user-supplied endpoint serves /v1/models, and
// returns it as a Found (the guided-fallback "paste a URL" path). ok is false
// when the URL is unreachable or not OpenAI-compatible.
func Probe(rawURL string) (Found, bool) {
	base := toV1Base(rawURL)
	if base == "" {
		return Found{}, false
	}
	models, ctx, ok := probeModels(base)
	if !ok {
		return Found{}, false
	}
	return Found{Name: "configured", BaseURL: base, Chat: base + "/chat/completions", Models: models, Ctx: ctx}, true
}

// detectWith runs the full pipeline with optional priority candidates first.
func detectWith(priority []candidate) []Found {
	cands := priority
	// (a) documented default endpoints.
	for _, p := range probes {
		cands = append(cands, candidate{name: p.name, base: p.base})
	}
	// (b) environment variables the user's tooling already exports.
	cands = append(cands, envCands()...)
	// (d) real listening ports -> probe each on localhost for /v1/models. This is
	// what finds a model on a CUSTOM port without a brute scan: the OS already
	// knows which ports are open; we only probe those.
	for _, port := range enumPorts() {
		cands = append(cands, candidate{name: "port:" + strconv.Itoa(port), base: "http://127.0.0.1:" + strconv.Itoa(port) + "/v1"})
	}

	seen := map[string]bool{}
	var out []Found
	for _, c := range cands {
		base := strings.TrimRight(c.base, "/")
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		models, ctx, ok := probeModels(base)
		if !ok {
			continue
		}
		f := Found{Name: c.name, BaseURL: base, Chat: base + "/chat/completions", Models: models, Ctx: ctx}
		// (c) native fleet discovery: an Ollama base also exposes /api/tags and
		// /api/ps, which list models installed-but-swapped-out (a fresh /v1/models
		// only shows what is loaded). Union those in so the whole fleet is offerable.
		mergeOllamaNative(&f, base)
		out = append(out, f)
	}
	return out
}

// probeModels does GET base/models and parses the served model ids + per-model
// context length (the keys vary by server). ok is false on any non-200 / error.
func probeModels(base string) (models []string, ctx map[string]int, ok bool) {
	resp, err := httpClient.Get(base + "/models")
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, nil, false
	}
	// Many OpenAI-compatible servers (vLLM, llama.cpp, LM Studio, TGI) report a
	// per-model context length on /v1/models under one of these common keys.
	var d struct {
		Data []struct {
			ID         string `json:"id"`
			MaxLen     int    `json:"max_model_len"`  // vLLM
			CtxLen     int    `json:"context_length"` // some gateways
			NCtx       int    `json:"n_ctx"`          // llama.cpp
			MaxCtx     int    `json:"max_context_length"`
			ContextWin int    `json:"context_window"` // LM Studio / others
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	resp.Body.Close()
	ctx = map[string]int{}
	for _, m := range d.Data {
		if m.ID == "" {
			continue
		}
		models = append(models, m.ID)
		if c := firstPositive(m.MaxLen, m.CtxLen, m.NCtx, m.MaxCtx, m.ContextWin); c > 0 {
			ctx[m.ID] = c
		}
	}
	return models, ctx, true
}

// envCandidates derives base URLs from environment variables the user's existing
// tooling already exports, so a non-default endpoint is found without a scan.
func envCandidates() []candidate {
	var out []candidate
	add := func(name, raw string) {
		if b := toV1Base(raw); b != "" {
			out = append(out, candidate{name: name, base: b})
		}
	}
	// The OpenAI SDK convention (both spellings are in the wild).
	add("env:OPENAI_BASE_URL", os.Getenv("OPENAI_BASE_URL"))
	add("env:OPENAI_API_BASE", os.Getenv("OPENAI_API_BASE"))
	// Ollama: OLLAMA_HOST may be "host:port", ":11434", or a full URL.
	if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
		add("env:OLLAMA_HOST", ollamaHostURL(h))
	}
	// LM Studio exports a few spellings depending on version.
	for _, k := range []string{"LMSTUDIO_BASE_URL", "LMSTUDIO_API_BASE", "LMSTUDIO_HOST"} {
		add("env:"+k, os.Getenv(k))
	}
	return out
}

// ollamaHostURL turns an OLLAMA_HOST value (host:port, :port, host, or a URL)
// into an http base URL.
func ollamaHostURL(h string) string {
	if strings.Contains(h, "://") {
		return h
	}
	if strings.HasPrefix(h, ":") {
		return "http://127.0.0.1" + h
	}
	return "http://" + h
}

// mergeOllamaNative unions an Ollama server's full fleet (GET /api/tags = all
// installed models) and currently-loaded set (GET /api/ps) into f.Models, so a
// model that is installed but swapped out of VRAM still shows as offerable. It is
// a best-effort enrichment: a non-Ollama base simply has no /api/tags and is left
// as-is.
func mergeOllamaNative(f *Found, base string) {
	root := strings.TrimSuffix(base, "/v1")
	have := map[string]bool{}
	for _, m := range f.Models {
		have[m] = true
	}
	addNames := func(path string) {
		resp, err := httpClient.Get(root + path)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			return
		}
		var d struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		for _, m := range d.Models {
			id := m.Name
			if id == "" {
				id = m.Model
			}
			if id != "" && !have[id] {
				have[id] = true
				f.Models = append(f.Models, id)
			}
		}
	}
	addNames("/api/tags") // every installed model (the full fleet)
	addNames("/api/ps")   // currently-loaded (already in tags, but harmless)
	sort.Strings(f.Models)
}

// toV1Base normalizes a user/env/port URL to its .../v1 base (the form probeModels
// expects), accepting a bare host:port, a base URL, a /v1 URL, or a full
// /v1/chat/completions URL. Returns "" for empty input.
func toV1Base(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	u = strings.TrimRight(u, "/")
	switch {
	case strings.HasSuffix(u, "/v1/chat/completions"):
		return strings.TrimSuffix(u, "/chat/completions")
	case strings.HasSuffix(u, "/chat/completions"):
		// e.g. .../chat/completions without a /v1 - back off to its parent.
		return strings.TrimSuffix(u, "/chat/completions")
	case strings.HasSuffix(u, "/v1"):
		return u
	default:
		return u + "/v1"
	}
}

// firstPositive returns the first value > 0 (the first context-length key a server
// actually populated), or 0 when none is reported.
func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
