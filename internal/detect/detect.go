// Package detect finds local OpenAI-compatible LLM servers so `roger share`
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
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Found is a reachable local OpenAI-compatible server discovered by DetectFull.
type Found struct {
	Name    string         // friendly server name (e.g. "ollama")
	BaseURL string         // .../v1
	Chat    string         // .../v1/chat/completions
	Models  []string       // served model ids from GET /v1/models (+ native discovery)
	Ctx     map[string]int // per-model context length when the server reports it
	Key     string         // bearer key the upstream required (discovered from env), if any
	// Modality is the per-model kind: "chat" (default), "tts", or "stt" — filled by
	// classifyModalities from the served endpoints + an id heuristic. See VOICE-AUDIO-DESIGN.md.
	Modality map[string]string
	// Capabilities are the per-model chat sub-capabilities (e.g. ["vision"]) — filled by
	// classifyCapabilities from the served /v1/models metadata + an id heuristic. See
	// docs/BROKER-VISION-CAPABILITY.md.
	Capabilities map[string][]string
}

// Status is the tri-state result of probing a single endpoint: a 401/403 means an
// OpenAI-compatible server IS there but needs a key we couldn't supply - distinct
// from "nothing listening" - so the caller can ask for a key instead of giving up.
type Status int

const (
	Unreachable Status = iota // no OpenAI-compatible server answered
	Reachable                 // serves /v1/models (the Found is populated)
	NeedsKey                  // server present but 401/403 and no known key worked
)

// Common local OpenAI-compatible servers, by default port. Any server exposing
// GET /v1/models works; this just enables zero-config detection. Users can always
// point at anything with `roger share --upstream <url>`.
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

// authGet / authPost are the ONE place a probe request is built, so a discovered
// upstream key is attached uniformly as a Bearer (a key-protected local server -
// vLLM --api-key, a LiteLLM master key, llama.cpp --api-key, LM Studio's API-key
// toggle - returns 401 to an unauthenticated GET /v1/models and would otherwise be
// invisible). An empty key sends no header (the no-auth common case).
func authGet(url, key string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return httpClient.Do(req)
}

func authPost(url, key, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return httpClient.Do(req)
}

// maxEnumPorts caps how many real listening ports the cross-platform enumerator
// returns, so a host with hundreds of open ports can't blow up the probe fan-out.
// The documented defaults + env vars already cover the common servers; this is the
// "found it on a custom port" tail, which is small in practice.
const maxEnumPorts = 64

// candidate is a base URL to probe, with a friendly label for the source and an
// optional sibling API key (e.g. OPENAI_API_KEY paired with OPENAI_BASE_URL) tried
// first when the endpoint answers 401/403.
type candidate struct{ name, base, key string }

// enumPorts / envCands are indirections over the real listening-port enumerator
// and the env-var source, so tests can make detection deterministic (the host's
// own open ports must not leak into a unit test's result). Production uses the
// real implementations.
var (
	enumPorts = listeningPorts
	envCands  = envCandidates
	envKeysFn = envKeys
)

// DetectFull gathers candidate endpoints from every source - explicit extra base URLs
// FIRST (the --upstream / saved-config (e) source, each normalized to a /v1 base and
// winning de-dup so its friendly name is kept), then defaults, env, Ollama native, and
// real listening ports - probes each for GET /v1/models, and returns the reachable
// OpenAI-compatible servers (de-duplicated by base URL) PLUS the base URLs of servers
// that are present but answered 401/403 with no usable key (the needKey list), so the
// caller can prompt for an API key instead of reporting "nothing detected".
func DetectFull(extra ...string) (found []Found, needKey []string) {
	return detectWith(priorityCands(extra))
}

// priorityCands normalizes explicit --upstream/config URLs into priority candidates
// probed before the defaults (so an explicit endpoint wins de-dup and keeps its
// "configured" name).
func priorityCands(extra []string) []candidate {
	cands := make([]candidate, 0, len(extra))
	for _, u := range extra {
		if b := toV1Base(u); b != "" {
			cands = append(cands, candidate{name: "configured", base: b})
		}
	}
	return cands
}

// ProbeKey verifies that a single user-supplied endpoint serves /v1/models and returns
// it as a Found (the guided-fallback "paste a URL" path), trying an explicit key first
// (the user pasted one) then falling back to keys the environment exports. It returns the
// tri-state Status so the guided fallback can tell "needs a key" (prompt for one) apart
// from "unreachable".
func ProbeKey(rawURL, key string) (Found, Status) {
	base := toV1Base(rawURL)
	if base == "" {
		return Found{}, Unreachable
	}
	keys := envKeysFn()
	if key != "" {
		keys = append([]string{key}, keys...)
	}
	models, ctx, usedKey, res := probeModels(base, keys)
	switch res {
	case probeOK:
		f := Found{Name: "configured", BaseURL: base, Chat: base + "/chat/completions", Models: models, Ctx: ctx, Key: usedKey}
		mergeOllamaNative(&f, base)
		enrichCtx(&f, base)
		classifyModalities(&f, base)
		classifyCapabilities(&f, base)
		return f, Reachable
	case probeAuth:
		return Found{BaseURL: base}, NeedsKey
	default:
		return Found{}, Unreachable
	}
}

// detectWith runs the full pipeline with optional priority candidates first. It
// returns the reachable servers plus the base URLs of any that need a key we don't
// have (so the caller can ask for one).
func detectWith(priority []candidate) (found []Found, needKey []string) {
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

	// Keys the user's tooling exports, tried (as Bearer) against any candidate that
	// answers 401/403 - so a key-protected local server whose key is already in the
	// environment is detected with zero extra config.
	keys := envKeysFn()
	seen := map[string]bool{}
	needSeen := map[string]bool{}
	for _, c := range cands {
		base := strings.TrimRight(c.base, "/")
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		// Harvested env keys are retried (as Bearer) on a 401/403 — but ONLY against
		// candidates we have a reason to trust: a configured / known-default / env-derived
		// endpoint. A BLIND port-scan hit ("port:N") could be ANY local service, so we never
		// spray the user's API keys at it; if it 401s we surface it via needKey instead, so
		// the user explicitly supplies a key for a server they actually recognize.
		tryKeys := keys
		if strings.HasPrefix(c.name, "port:") {
			tryKeys = nil
		}
		// Try this candidate's own paired key first (e.g. OPENAI_API_KEY for an
		// OPENAI_BASE_URL endpoint), then the rest of the (trusted-candidate) env keys.
		if c.key != "" {
			tryKeys = append([]string{c.key}, tryKeys...)
		}
		models, ctx, usedKey, res := probeModels(base, tryKeys)
		switch res {
		case probeOK:
			f := Found{Name: c.name, BaseURL: base, Chat: base + "/chat/completions", Models: models, Ctx: ctx, Key: usedKey}
			// (c) native fleet discovery: an Ollama base also exposes /api/tags and
			// /api/ps, which list models installed-but-swapped-out (a fresh /v1/models
			// only shows what is loaded). Union those in so the whole fleet is offerable.
			mergeOllamaNative(&f, base)
			// Real per-model CONTEXT detection beyond /v1/models. Ollama reports its true
			// trained window on /api/show + the loaded num_ctx on /api/ps; llama.cpp reports
			// the real loaded n_ctx on /props; LM Studio reports loaded/max ctx on
			// /api/v0/models. These are more accurate than the optional /v1/models keys (and
			// Ollama omits ctx from /v1/models entirely), so a node advertises the REAL
			// served window instead of falling back to the 32768 last-resort default.
			enrichCtx(&f, base)
			classifyModalities(&f, base)
			classifyCapabilities(&f, base)
			found = append(found, f)
		case probeAuth:
			// Present but key-protected and no env key fit: surface it so the caller can
			// ask the user to paste a key rather than report "nothing detected".
			if !needSeen[base] {
				needSeen[base] = true
				needKey = append(needKey, base)
			}
		case probeMiss:
			// No usable /v1/models (kokoro-fastapi 404s it; most Whisper servers omit it).
			// Before giving up, probe the audio capability: a bare TTS/STT server has no model
			// list to enumerate, so synthesize ONE offer from what it can DO. endpointExists
			// treats a 401 as "route present", so a key-protected bare voice server is caught too
			// WITHOUT spraying a key (no key is sent — consistent with the port-scan policy). A
			// normal chat server never reaches here (its /v1/models is probeOK), so chat is untouched.
			if kind := probeVoice(base); kind != "" {
				name := voiceModelName(kind)
				f := Found{
					Name: c.name, BaseURL: base, Chat: base + "/chat/completions",
					Models:   []string{name},
					Modality: map[string]string{name: kind},
				}
				found = append(found, f)
			}
		}
	}
	return found, needKey
}

// probeResult is probeModels' tri-state: a usable server, a key-protected one, or
// nothing OpenAI-compatible.
type probeResult int

const (
	probeMiss probeResult = iota // unreachable / not OpenAI-compatible
	probeOK                      // 200: models parsed (usedKey is the key that worked, "" if none needed)
	probeAuth                    // 401/403: server present but no supplied key worked
)

// probeModels does GET base/models, first with no auth, and - only when the server
// answers 401/403 - retries with each candidate key until one returns 200. It
// returns the parsed model ids, per-model context length, the key that worked (""
// when none was needed), and the tri-state result.
func probeModels(base string, keys []string) (models []string, ctx map[string]int, usedKey string, res probeResult) {
	models, ctx, code := getModels(base, "")
	switch {
	case code == 200:
		return models, ctx, "", probeOK
	case code == 401 || code == 403:
		for _, k := range keys {
			if k == "" {
				continue
			}
			if m, c, code2 := getModels(base, k); code2 == 200 {
				return m, c, k, probeOK
			}
		}
		return nil, nil, "", probeAuth
	default:
		return nil, nil, "", probeMiss
	}
}

// getModels performs one GET base/models with the optional key and parses the model
// ids + per-model context length. status is the HTTP status code, or 0 on a
// transport error (treated as unreachable by the caller).
func getModels(base, key string) (models []string, ctx map[string]int, status int) {
	resp, err := authGet(base+"/models", key)
	if err != nil {
		return nil, nil, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, resp.StatusCode
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
	return models, ctx, 200
}

// envCandidates derives base URLs from environment variables the user's existing
// tooling already exports, so a non-default endpoint is found without a scan. Where
// the same tooling also exports an API key, that key is paired with the endpoint so
// a key-protected server is reached on the first try.
func envCandidates() []candidate {
	var out []candidate
	add := func(name, raw, key string) {
		if b := toV1Base(raw); b != "" {
			out = append(out, candidate{name: name, base: b, key: strings.TrimSpace(key)})
		}
	}
	// The OpenAI SDK convention (both spellings are in the wild); OPENAI_API_KEY is
	// the de-facto key for OpenAI-compatible servers behind these bases.
	openaiKey := os.Getenv("OPENAI_API_KEY")
	add("env:OPENAI_BASE_URL", os.Getenv("OPENAI_BASE_URL"), openaiKey)
	add("env:OPENAI_API_BASE", os.Getenv("OPENAI_API_BASE"), openaiKey)
	// Ollama: OLLAMA_HOST may be "host:port", ":11434", or a full URL.
	if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
		add("env:OLLAMA_HOST", ollamaHostURL(h), os.Getenv("OLLAMA_API_KEY"))
	}
	// LM Studio exports a few spellings depending on version.
	lmKey := os.Getenv("LMSTUDIO_API_KEY")
	for _, k := range []string{"LMSTUDIO_BASE_URL", "LMSTUDIO_API_BASE", "LMSTUDIO_HOST"} {
		add("env:"+k, os.Getenv(k), lmKey)
	}
	return out
}

// envKeys returns API keys the user's tooling already exports, tried (as a Bearer)
// against any candidate that answers 401/403. OPENAI_API_KEY is the de-facto key for
// OpenAI-compatible servers; the rest are the common tool-specific spellings. This
// is what makes a key-protected local server (vLLM --api-key, a LiteLLM master key,
// llama.cpp --api-key, LM Studio's API-key toggle) detectable with zero extra config
// whenever its key already lives in the environment.
func envKeys() []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{
		"OPENAI_API_KEY",
		"LITELLM_MASTER_KEY", "LITELLM_API_KEY",
		"LMSTUDIO_API_KEY",
		"VLLM_API_KEY",
		"OLLAMA_API_KEY",
	} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
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
		resp, err := authGet(root+path, f.Key)
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

// DefaultCtx is the last-resort context length used ONLY when no upstream reports
// a real per-model window. A node that falls back to this advertises CtxEstimated
// so the UI can render it as an estimate (~32k, dim) rather than a detected value.
const DefaultCtx = 32768

// ResolveCtx returns the real per-model context window for model, and whether it
// is the estimated DefaultCtx fallback (estimated=true) versus a value actually
// detected from the upstream (estimated=false). It is the ONE resolver both the CLI
// (`roger share`) and the TUI share table route through, so a detection improvement
// lands in both and the duplicated 32768 literal lives in exactly one place.
func ResolveCtx(ctx map[string]int, model string) (n int, estimated bool) {
	if ctx != nil {
		if c, ok := ctx[model]; ok && c > 0 {
			return c, false
		}
	}
	return DefaultCtx, true
}

// enrichCtx fills f.Ctx with the REAL per-model context window from each server's
// native endpoint, preferring the loaded/served window over the trained max. It is
// best-effort: a server that does not expose the endpoint is left as-is (the
// /v1/models value, else the DefaultCtx fallback at share time). Only fills a model
// that does not already have a (non-zero) ctx, so a /v1/models-reported window is
// not clobbered.
func enrichCtx(f *Found, base string) {
	if f.Ctx == nil {
		f.Ctx = map[string]int{}
	}
	root := strings.TrimSuffix(base, "/v1")
	enrichOllamaCtx(f, root)
	enrichLlamaCppCtx(f, root)
	enrichLMStudioCtx(f, root)
}

// modalityFromID classifies a model by its id when the server exposes no probeable audio
// endpoint (a gateway that only lists /v1/models) or when a mixed server's endpoints alone can't
// say which model is which. A hint, not a hard rule: the capability probe decides when the id is
// unknown. Empty => no hint. See VOICE-AUDIO-DESIGN.md §4.2.
func modalityFromID(id string) string {
	s := strings.ToLower(id)
	if strings.Contains(s, "whisper") || strings.Contains(s, "transcrib") || strings.HasSuffix(s, "-stt") {
		return protocol.ModalitySTT
	}
	for _, k := range []string{"kokoro", "tts", "parler", "chatterbox", "bark", "piper", "xtts", "vits", "speecht5", "orpheus"} {
		if strings.Contains(s, k) {
			return protocol.ModalityTTS
		}
	}
	return ""
}

// endpointExists reports whether base serves the given OpenAI endpoint: a minimal POST that a
// present route rejects with a 4xx (bad/empty body) while an ABSENT route 404s. Any non-404
// (incl. 401) means the route is there. Short-timeout, key-aware — same probe budget as detection.
func endpointExists(url, key string) bool {
	resp, err := authPost(url, key, "application/json", strings.NewReader("{}"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode != http.StatusNotFound
}

// audioRouteLive reports whether an audio route is actually IMPLEMENTED and handling POST — a
// stricter test than endpointExists (non-404), needed because worker servers STUB the OpenAI audio
// routes: they answer POST /v1/audio/speech with 501 Not Implemented (or 405), a non-404 that the
// loose check mistook for a real voice endpoint (7 false positives on the live box: Hermes workers
// on :8779, :8814, :8912-8915, :9090). A route is live only if it responds like a real handler:
// accept 2xx and a 4xx-that-isn't-absent (400 empty-body, 401 key-protected, 415/422), and reject
// 404 (absent), 405 (present but not for POST), 501 (stubbed) and any 5xx, plus a transport error.
// Keeping 401 live means a key-protected real voice server is still caught without sending a key.
func audioRouteLive(url, key string) bool {
	resp, err := authPost(url, key, "application/json", strings.NewReader("{}"))
	if err != nil {
		return false // transport error: nothing listening / no route
	}
	defer resp.Body.Close()
	code := resp.StatusCode
	// Implemented + POST-handling: below 500 (not a server-side stub/5xx), and neither 404
	// (route absent) nor 405 (route exists but rejects POST — a stub or a GET-only handler).
	return code < 500 && code != http.StatusNotFound && code != http.StatusMethodNotAllowed
}

// probeVoice classifies a BARE voice server — one with no usable GET /v1/models to enumerate
// (kokoro-fastapi on :8095 404s it; most Whisper servers omit it) — from its capability alone:
// a LIVE POST /v1/audio/speech route => "tts", a LIVE POST /v1/audio/transcriptions route =>
// "stt", otherwise "" (not a voice server). Uses audioRouteLive (not the loose endpointExists) so a
// worker that merely STUBS the audio routes (501/405) is not a false positive, while a key-protected
// real voice server (401) is still caught without a key. CPU vs GPU is irrelevant (endpoint-probed).
// Speech wins when a bare server answers both (one offer per server; a mixed bare server is a rare
// edge — we pick a deterministic label rather than emit two phantom ids).
func probeVoice(base string) string {
	switch {
	case audioRouteLive(base+"/audio/speech", ""):
		return protocol.ModalityTTS
	case audioRouteLive(base+"/audio/transcriptions", ""):
		return protocol.ModalitySTT
	default:
		return ""
	}
}

// voiceModelName is the stable default model id synthesized for a bare voice server that exposes
// no /v1/models to enumerate. It is overridable at share time (`roger share --model <name>`), which
// is how an operator names it (e.g. roger-operator-voice).
func voiceModelName(modality string) string {
	if modality == protocol.ModalitySTT {
		return "transcribe"
	}
	return "voice" // tts default
}

// classifyModalities fills f.Modality per model. A known voice/stt id wins first; otherwise the
// server's CAPABILITY decides — a pure speech endpoint => tts, a pure transcription endpoint =>
// stt, and everything else (chat, mixed, or unknown) stays chat. Endpoint-probed, so a model on
// CPU and the same model on GPU classify identically.
func classifyModalities(f *Found, base string) {
	if f.Modality == nil {
		f.Modality = map[string]string{}
	}
	hasSpeech := endpointExists(base+"/audio/speech", f.Key)
	hasTranscribe := endpointExists(base+"/audio/transcriptions", f.Key)
	hasChat := endpointExists(base+"/chat/completions", f.Key)
	for _, m := range f.Models {
		if hint := modalityFromID(m); hint != "" {
			f.Modality[m] = hint
			continue
		}
		switch {
		case hasSpeech && !hasChat && !hasTranscribe:
			f.Modality[m] = protocol.ModalityTTS
		case hasTranscribe && !hasChat && !hasSpeech:
			f.Modality[m] = protocol.ModalitySTT
		default:
			f.Modality[m] = protocol.ModalityChat
		}
	}
}

// visionMarkers are id substrings that mark a chat model as image-capable — the SAME hint set
// the iOS app uses as its fallback, so the broker's guess matches the app's. A hint, not a hard
// rule (like modalityFromID); the served metadata (visionFromMeta) wins when a server reports it.
var visionMarkers = []string{
	"-vl", "vl-", "vlm", "llava", "pixtral", "gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-5", "o3", "o4",
	"vision", "internvl", "minicpm-v", "moondream", "molmo", "gemma-3", "gemma3", "qwen2.5-omni",
	"qwen2-vl", "qwen2.5-vl", "phi-3.5-vision", "phi-4-multimodal", "idefics", "cogvlm", "glm-4v",
}

func visionFromID(id string) bool {
	s := strings.ToLower(id)
	for _, m := range visionMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// visionFromMeta best-effort reads the server's /v1/models to see which models it REPORTS as
// image-capable (authoritative when present): an entry whose modalities / input_modalities list
// contains "image"/"vision", or a truthy "vision"/"supports_vision" field. Servers that don't
// expose it simply yield an empty map and the id heuristic decides.
func visionFromMeta(base, key string) map[string]bool {
	out := map[string]bool{}
	resp, err := authGet(base+"/models", key)
	if err != nil || resp == nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var d struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil {
		return out
	}
	for _, m := range d.Data {
		id := ""
		if raw, ok := m["id"]; ok {
			_ = json.Unmarshal(raw, &id)
		}
		if id == "" {
			continue
		}
		// Scan the whole entry's lowercased JSON for an image/vision modality signal. Cheap and
		// robust across vLLM/llama.cpp/LM Studio shapes without hard-coding each field name.
		blob, _ := json.Marshal(m)
		s := strings.ToLower(string(blob))
		if strings.Contains(s, `"image"`) || strings.Contains(s, `"vision":true`) ||
			strings.Contains(s, `"supports_vision":true`) || strings.Contains(s, `"multimodal":true`) {
			out[id] = true
		}
	}
	return out
}

// CapabilitiesForModel classifies ONE chat model's sub-capabilities from the served /v1/models
// metadata (base = the .../v1 root) + the id heuristic - for the explicit --upstream share path,
// which skips full detection yet still knows the model id. Returns ["vision"] when image-capable,
// else [] (a chat model is always classifiable from its id, so this never returns nil/undetermined).
func CapabilitiesForModel(base, model, key string) []string {
	if visionFromMeta(base, key)[model] || visionFromID(model) {
		return []string{protocol.CapVision}
	}
	return []string{}
}

// classifyCapabilities fills f.Capabilities per CHAT model: ["vision"] when the served metadata
// or the id heuristic marks it image-capable, else [] (a positive "text only" for the app to
// trust over its own name guess). Voice (tts/stt) models get no capabilities. Endpoint-probed +
// id-hinted, so the same model classifies identically on CPU and GPU. See BROKER-VISION-CAPABILITY.md.
func classifyCapabilities(f *Found, base string) {
	if f.Capabilities == nil {
		f.Capabilities = map[string][]string{}
	}
	meta := visionFromMeta(base, f.Key)
	for _, m := range f.Models {
		if f.Modality != nil && f.Modality[m] != "" && f.Modality[m] != protocol.ModalityChat {
			continue // a voice/stt model has no chat sub-capabilities
		}
		if meta[m] || visionFromID(m) {
			f.Capabilities[m] = []string{protocol.CapVision}
		} else {
			// Classified text-only ([]). NOTE: this positive signal does NOT reach the app today -
			// ModelOffer.Capabilities carries omitempty (required to keep it out of the registration
			// possession-proof, see regSigningBytes), so an empty [] collapses to absent on the
			// node->broker wire. Only ["vision"] survives; for a non-vision model the app falls back
			// to its own name heuristic. Restoring the text-only signal needs a channel outside the
			// signed offer (TODO).
			f.Capabilities[m] = []string{}
		}
	}
}

// enrichOllamaCtx reads Ollama's real per-model context: the loaded runtime num_ctx
// from GET /api/ps (the window the model is ACTUALLY served at right now), else the
// trained window from POST /api/show .model_info["<arch>.context_length"]. A
// non-Ollama base simply has neither endpoint and is left untouched.
func enrichOllamaCtx(f *Found, root string) {
	// /api/ps: currently-loaded models carry context_length = the live num_ctx.
	if resp, err := authGet(root+"/api/ps", f.Key); err == nil && resp.StatusCode == 200 {
		var d struct {
			Models []struct {
				Name      string `json:"name"`
				Model     string `json:"model"`
				ContextLn int    `json:"context_length"`
			} `json:"models"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		for _, m := range d.Models {
			if m.ContextLn <= 0 {
				continue
			}
			for _, id := range []string{m.Name, m.Model} {
				if id != "" && f.Ctx[id] <= 0 {
					f.Ctx[id] = m.ContextLn
				}
			}
		}
	} else if resp != nil {
		resp.Body.Close()
	}
	// /api/show: the model's trained context window, keyed under "<arch>.context_length"
	// in model_info. Used for installed-but-not-loaded models (no live num_ctx yet).
	for _, id := range f.Models {
		if f.Ctx[id] > 0 {
			continue
		}
		body := strings.NewReader(`{"model":` + strconv.Quote(id) + `}`)
		resp, err := authPost(root+"/api/show", f.Key, "application/json", body)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		var d struct {
			ModelInfo map[string]json.RawMessage `json:"model_info"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		if c := ollamaContextFromInfo(d.ModelInfo); c > 0 {
			f.Ctx[id] = c
		}
	}
}

// ollamaContextFromInfo pulls the context window out of Ollama's model_info map,
// whose key is architecture-specific ("llama.context_length", "qwen2.context_length",
// ...). We accept any "*.context_length" key so it works across architectures without
// hardcoding each one.
func ollamaContextFromInfo(info map[string]json.RawMessage) int {
	for k, v := range info {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		var n int
		if json.Unmarshal(v, &n) == nil && n > 0 {
			return n
		}
	}
	return 0
}

// enrichLlamaCppCtx reads llama.cpp's real LOADED context from GET /props
// .default_generation_settings.n_ctx (the live window, more reliable than the
// optional /v1/models n_ctx). llama.cpp serves a single model, so the value applies
// to every model id this base advertises that lacks a detected ctx.
func enrichLlamaCppCtx(f *Found, root string) {
	resp, err := authGet(root+"/props", f.Key)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	var d struct {
		DefaultGen struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	resp.Body.Close()
	if d.DefaultGen.NCtx <= 0 {
		return
	}
	for _, id := range f.Models {
		if f.Ctx[id] <= 0 {
			f.Ctx[id] = d.DefaultGen.NCtx
		}
	}
}

// enrichLMStudioCtx reads LM Studio's per-model context from GET /api/v0/models,
// preferring loaded_context_length (the live window) over max_context_length (the
// model cap). A non-LM-Studio base has no /api/v0/models and is left untouched.
func enrichLMStudioCtx(f *Found, root string) {
	resp, err := authGet(root+"/api/v0/models", f.Key)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	var d struct {
		Data []struct {
			ID        string `json:"id"`
			LoadedCtx int    `json:"loaded_context_length"`
			MaxCtx    int    `json:"max_context_length"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	resp.Body.Close()
	for _, m := range d.Data {
		if m.ID == "" || f.Ctx[m.ID] > 0 {
			continue
		}
		if c := firstPositive(m.LoadedCtx, m.MaxCtx); c > 0 {
			f.Ctx[m.ID] = c
		}
	}
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
