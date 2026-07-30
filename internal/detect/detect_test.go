package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// fakeServer mimics an OpenAI-compatible GET /v1/models response.
func fakeServer(models ...string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		var data []map[string]string
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	return httptest.NewServer(mux)
}

// quietSources makes detection deterministic in a unit test by disabling the
// env-var and real-listening-port sources, so only the (swapped) probe table is
// consulted. Returns a restore func.
func quietSources(t *testing.T) func() {
	t.Helper()
	oldEnum, oldEnv := enumPorts, envCands
	enumPorts = func() []int { return nil }
	envCands = func() []candidate { return nil }
	return func() { enumPorts, envCands = oldEnum, oldEnv }
}

// TestProbe exercises the GET /v1/models parsing against a real test server by
// reusing Detect's probe logic over a one-entry probe table.
func TestProbeParsesModels(t *testing.T) {
	defer quietSources(t)()
	srv := fakeServer("llama-3.1-8b", "qwen2.5-coder")
	defer srv.Close()

	old := probes
	probes = []struct{ name, base string }{{"test", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull()
	if len(found) != 1 {
		t.Fatalf("found %d servers, want 1: %+v", len(found), found)
	}
	f := found[0]
	if f.Name != "test" {
		t.Errorf("name = %q want test", f.Name)
	}
	if f.BaseURL != srv.URL+"/v1" {
		t.Errorf("base = %q", f.BaseURL)
	}
	if f.Chat != srv.URL+"/v1/chat/completions" {
		t.Errorf("chat = %q", f.Chat)
	}
	if len(f.Models) != 2 || f.Models[0] != "llama-3.1-8b" || f.Models[1] != "qwen2.5-coder" {
		t.Errorf("models = %v", f.Models)
	}
}

// TestProbeSkipsUnreachable: a probe pointed at a dead port yields nothing (no
// panic, no partial entry).
func TestProbeSkipsUnreachable(t *testing.T) {
	defer quietSources(t)()
	old := probes
	// 127.0.0.1:1 is reliably closed; the short client timeout makes this quick.
	probes = []struct{ name, base string }{{"dead", "http://127.0.0.1:1/v1"}}
	defer func() { probes = old }()

	if found, _ := DetectFull(); len(found) != 0 {
		t.Errorf("unreachable probe should yield nothing, got %+v", found)
	}
}

// TestProbeSkipsNon200: a server that answers but not with 200 is skipped.
func TestProbeSkipsNon200(t *testing.T) {
	defer quietSources(t)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	old := probes
	probes = []struct{ name, base string }{{"err", srv.URL + "/v1"}}
	defer func() { probes = old }()

	if found, _ := DetectFull(); len(found) != 0 {
		t.Errorf("non-200 probe should yield nothing, got %+v", found)
	}
}

// hostPort extracts the host:port of an httptest server URL.
func hostPort(t *testing.T, srvURL string) (host string, port int) {
	t.Helper()
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("parse %q: %v", srvURL, err)
	}
	p, _ := strconv.Atoi(u.Port())
	return u.Hostname(), p
}

// TestDetectFindsCustomPort: a model on a NON-default port (not in the probe
// table) is still found via real listening-port enumeration - no brute scan, just
// probing the OS's actual open ports. We simulate the enumerator returning the
// test server's port.
func TestDetectFindsCustomPort(t *testing.T) {
	defer quietSources(t)()
	srv := fakeServer("custom-model")
	defer srv.Close()
	_, port := hostPort(t, srv.URL)

	// Empty probe table (no defaults match), but the enumerator "sees" the port.
	old := probes
	probes = nil
	defer func() { probes = old }()
	enumPorts = func() []int { return []int{port} }

	found, _ := DetectFull()
	if len(found) != 1 || len(found[0].Models) != 1 || found[0].Models[0] != "custom-model" {
		t.Fatalf("custom-port detection failed: %+v", found)
	}
	if !strings.HasPrefix(found[0].Name, "port:") {
		t.Errorf("custom-port source should be labeled port:N, got %q", found[0].Name)
	}
}

// TestDetectEnvVar: an OPENAI_BASE_URL pointing at a server is detected even when
// it is on no default port and not enumerated.
func TestDetectEnvVar(t *testing.T) {
	oldEnum := enumPorts
	enumPorts = func() []int { return nil }
	defer func() { enumPorts = oldEnum }()
	srv := fakeServer("env-model")
	defer srv.Close()
	old := probes
	probes = nil
	defer func() { probes = old }()
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")

	found, _ := DetectFull()
	if len(found) != 1 || len(found[0].Models) != 1 || found[0].Models[0] != "env-model" {
		t.Fatalf("env-var detection failed: %+v", found)
	}
}

// TestUnslothStudioAuthTokenIsRead pins the variable Unsloth ITSELF documents for
// its local API key: UNSLOTH_STUDIO_AUTH_TOKEN, holding an sk-unsloth-* key (see
// its "Connect Python SDK to Unsloth" docs). A provider who followed Unsloth's own
// setup exports only this one, so reading it is what actually makes an
// authenticated Studio zero-config. UNSLOTH_API_KEY is a RogerAI-side alias, not
// something Unsloth exports.
func TestUnslothStudioAuthTokenIsRead(t *testing.T) {
	t.Run("default endpoint", func(t *testing.T) {
		defer quietSources(t)()
		srv := keyedServer("sk-unsloth-documented", "unsloth/model-GGUF")
		defer srv.Close()

		old := probes
		probes = []struct{ name, base string }{{"unsloth", srv.URL + "/v1"}}
		defer func() { probes = old }()
		t.Setenv("UNSLOTH_API_KEY", "")
		t.Setenv("UNSLOTH_STUDIO_AUTH_TOKEN", "sk-unsloth-documented")

		found, needKey := DetectFull()
		if len(needKey) != 0 || len(found) != 1 {
			t.Fatalf("UNSLOTH_STUDIO_AUTH_TOKEN not used: found %+v, needKey %v", found, needKey)
		}
		if found[0].Name != "unsloth" || found[0].Key != "sk-unsloth-documented" {
			t.Fatalf("Unsloth result = %+v", found[0])
		}
	})

	t.Run("paired with a custom URL", func(t *testing.T) {
		t.Setenv("UNSLOTH_API_KEY", "")
		t.Setenv("UNSLOTH_STUDIO_AUTH_TOKEN", "sk-unsloth-documented")
		t.Setenv("UNSLOTH_STUDIO_URL", "http://127.0.0.1:8899")

		for _, c := range envCandidates() {
			if c.name == "unsloth" {
				if c.key != "sk-unsloth-documented" {
					t.Fatalf("custom-URL Unsloth candidate key = %q, want the documented token", c.key)
				}
				return
			}
		}
		t.Fatal("no Unsloth candidate produced")
	})

	// The documented token is a local host credential like any other: it must stay
	// scoped to Unsloth candidates and never be sprayed at another named host.
	t.Run("stays scoped to Unsloth", func(t *testing.T) {
		defer quietSources(t)()
		var received string
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = r.Header.Get("Authorization")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer other.Close()

		old := probes
		probes = []struct{ name, base string }{{"vllm/tgi", other.URL + "/v1"}}
		defer func() { probes = old }()
		t.Setenv("UNSLOTH_STUDIO_AUTH_TOKEN", "sk-unsloth-documented")

		DetectFull()
		if received != "" {
			t.Fatalf("documented Unsloth token leaked to a non-Unsloth candidate: %q", received)
		}
	})
}

// TestUnslothDefaultPortDoesNotGetTheGlobalKeyPool guards a collision the port
// number makes likely: :8888 is JupyterLab's default, and Jupyter answers 403 to
// an unauthenticated caller. Because the Unsloth default is a NAMED candidate it
// escapes the "port:" blind-scan guard, so without this it would receive every
// harvested key (OPENAI_API_KEY and friends) as a Bearer. A local service that is
// not a model server must never be handed the user's cloud credentials.
func TestUnslothDefaultPortDoesNotGetTheGlobalKeyPool(t *testing.T) {
	defer quietSources(t)()
	var seen []string
	jupyterish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			seen = append(seen, a)
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer jupyterish.Close()

	old := probes
	probes = []struct{ name, base string }{{"unsloth", jupyterish.URL + "/v1"}}
	defer func() { probes = old }()

	oldKeys := envKeysFn
	envKeysFn = func() []string { return []string{"sk-openai-secret", "sk-litellm-secret"} }
	defer func() { envKeysFn = oldKeys }()
	t.Setenv("UNSLOTH_STUDIO_AUTH_TOKEN", "")
	t.Setenv("UNSLOTH_API_KEY", "")

	DetectFull()
	for _, a := range seen {
		if strings.Contains(a, "sk-openai-secret") || strings.Contains(a, "sk-litellm-secret") {
			t.Fatalf("global key pool sprayed at the Unsloth default port: %q", a)
		}
	}
}

// TestUnslothDiscoveryContract pins Unsloth Studio's public local API contract:
// its default OpenAI-compatible endpoint is :8888, plus RogerAI's own
// UNSLOTH_STUDIO_URL/UNSLOTH_API_KEY override pair, carried together so an
// authenticated custom-port Studio is still zero-config.
func TestUnslothDiscoveryContract(t *testing.T) {
	t.Run("default endpoint", func(t *testing.T) {
		var got string
		for _, p := range probes {
			if p.name == "unsloth" {
				got = p.base
				break
			}
		}
		if got != "http://127.0.0.1:8888/v1" {
			t.Fatalf("unsloth default probe = %q, want http://127.0.0.1:8888/v1", got)
		}
	})

	t.Run("custom authenticated endpoint", func(t *testing.T) {
		t.Setenv("UNSLOTH_STUDIO_URL", "http://127.0.0.1:8899")
		t.Setenv("UNSLOTH_API_KEY", "sk-unsloth-local")

		var got *candidate
		for _, c := range envCandidates() {
			if c.name == "unsloth" {
				copy := c
				got = &copy
				break
			}
		}
		if got == nil {
			t.Fatal("UNSLOTH_STUDIO_URL did not produce an Unsloth candidate")
		}
		if got.base != "http://127.0.0.1:8899/v1" || got.key != "sk-unsloth-local" {
			t.Fatalf("unsloth env candidate = %+v", *got)
		}
	})
}

// TestDetectUnslothWithItsEnvKey proves the environment key is not merely
// harvested: it authenticates the paired Unsloth endpoint and is retained for
// the local relay.
func TestDetectUnslothWithItsEnvKey(t *testing.T) {
	defer quietSources(t)()
	srv := keyedServer("sk-unsloth-local", "unsloth/model-GGUF")
	defer srv.Close()

	old := probes
	probes = nil
	defer func() { probes = old }()
	t.Setenv("UNSLOTH_STUDIO_URL", srv.URL)
	t.Setenv("UNSLOTH_API_KEY", "sk-unsloth-local")
	// Re-enabling the REAL env scan is the point of this test, so the developer's own
	// ambient endpoints must be cleared first: a live Ollama or LM Studio on this box
	// would add a second Found and fail the assertion below for no good reason.
	for _, k := range []string{
		"OPENAI_BASE_URL", "OPENAI_API_BASE", "OLLAMA_HOST",
		"LMSTUDIO_BASE_URL", "LMSTUDIO_API_BASE", "LMSTUDIO_HOST",
	} {
		t.Setenv(k, "")
	}
	envCands = envCandidates

	found, needKey := DetectFull()
	if len(needKey) != 0 || len(found) != 1 {
		t.Fatalf("Unsloth detection = found %+v, needKey %v", found, needKey)
	}
	if found[0].Name != "unsloth" || found[0].Key != "sk-unsloth-local" {
		t.Fatalf("Unsloth result = %+v", found[0])
	}
}

// TestUnslothKeyIsScopedToUnslothCandidates prevents a local host credential
// from being sprayed at another named default server that happens to return 401.
func TestUnslothKeyIsScopedToUnslothCandidates(t *testing.T) {
	defer quietSources(t)()
	var received string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer other.Close()

	old := probes
	probes = []struct{ name, base string }{{"vllm/tgi", other.URL + "/v1"}}
	defer func() { probes = old }()
	t.Setenv("UNSLOTH_API_KEY", "sk-unsloth-secret")

	DetectFull()
	if received != "" {
		t.Fatalf("Unsloth key leaked to a non-Unsloth candidate: %q", received)
	}
}

// TestDetectDedup: the same server reachable via two sources (a default probe and
// the port enumerator) yields ONE Found, not a duplicate.
func TestDetectDedup(t *testing.T) {
	defer quietSources(t)()
	srv := fakeServer("dup-model")
	defer srv.Close()
	_, port := hostPort(t, srv.URL)
	old := probes
	probes = []struct{ name, base string }{{"test", srv.URL + "/v1"}}
	defer func() { probes = old }()
	enumPorts = func() []int { return []int{port} }

	if found, _ := DetectFull(); len(found) != 1 {
		t.Fatalf("same server via two sources should de-dup to 1, got %d: %+v", len(found), found)
	}
}

// TestProbeVerifiesEndpoint: the guided-fallback "paste a URL" path accepts a
// base URL / host:port / full chat URL and confirms it serves /v1/models.
func TestProbeVerifiesEndpoint(t *testing.T) {
	srv := fakeServer("pasted-model")
	defer srv.Close()
	host, port := hostPort(t, srv.URL)
	hp := host + ":" + strconv.Itoa(port)
	for _, in := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/chat/completions", hp} {
		f, st := ProbeKey(in, "")
		if st != Reachable || len(f.Models) != 1 || f.Models[0] != "pasted-model" {
			t.Errorf("ProbeKey(%q) failed: status=%v found=%+v", in, st, f)
		}
		if f.Chat != srv.URL+"/v1/chat/completions" {
			t.Errorf("ProbeKey(%q) chat url = %q", in, f.Chat)
		}
	}
	// A dead endpoint is not verified.
	if _, st := ProbeKey("http://127.0.0.1:1/v1", ""); st == Reachable {
		t.Error("ProbeKey of a dead endpoint should not verify")
	}
}

// keyedServer mimics an OpenAI-compatible server that requires a Bearer key: it
// 401s GET /v1/models without the right Authorization header and serves the models
// with it.
func keyedServer(wantKey string, models ...string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var data []map[string]string
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	return httptest.NewServer(mux)
}

// TestDetectKeyedUpstreamFromEnv: a key-protected local server is detected when its
// key is in the environment (the zero-config harvest) and the working key is carried
// on the Found so the on-air agent can reuse it.
func TestDetectKeyedUpstreamFromEnv(t *testing.T) {
	defer quietSources(t)()
	srv := keyedServer("sk-secret", "keyed-model")
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "sk-secret")

	old := probes
	probes = []struct{ name, base string }{{"test", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull()
	if len(found) != 1 {
		t.Fatalf("keyed upstream with env key should be detected, got %+v", found)
	}
	if len(found[0].Models) != 1 || found[0].Models[0] != "keyed-model" {
		t.Errorf("models = %v", found[0].Models)
	}
	if found[0].Key != "sk-secret" {
		t.Errorf("Found.Key = %q, want the working key so the agent can reuse it", found[0].Key)
	}
}

// TestDetectFullSurfacesNeedsKey: a key-protected server with NO usable key is not
// returned as usable, but its base URL surfaces in needKey so the caller can prompt.
func TestDetectFullSurfacesNeedsKey(t *testing.T) {
	defer quietSources(t)()
	srv := keyedServer("sk-secret", "keyed-model")
	defer srv.Close()
	// No OPENAI_API_KEY in the environment for this test.
	t.Setenv("OPENAI_API_KEY", "")

	old := probes
	probes = []struct{ name, base string }{{"test", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, needKey := DetectFull()
	if len(found) != 0 {
		t.Fatalf("a server we can't authenticate to is not usable, got %+v", found)
	}
	if len(needKey) != 1 || needKey[0] != srv.URL+"/v1" {
		t.Fatalf("needKey should surface the key-protected base, got %v", needKey)
	}
}

// TestDetectDoesNotSprayEnvKeysToPortScans: a BLIND port-scan hit (candidate named
// "port:N") must never receive the user's harvested env API keys on a 401 — an arbitrary
// local service could be listening there. The key-protected server stays unauthenticated
// and surfaces via needKey instead, even though the matching key IS in the environment.
func TestDetectDoesNotSprayEnvKeysToPortScans(t *testing.T) {
	defer quietSources(t)()
	srv := keyedServer("sk-secret", "keyed-model")
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "sk-secret") // the working key is in the env...

	old := probes
	probes = []struct{ name, base string }{{"port:9999", srv.URL + "/v1"}} // ...but this hit is a blind scan
	defer func() { probes = old }()

	found, needKey := DetectFull()
	if len(found) != 0 {
		t.Fatalf("env key must NOT be sprayed at a blind port-scan candidate; got %+v", found)
	}
	if len(needKey) != 1 || needKey[0] != srv.URL+"/v1" {
		t.Fatalf("a key-protected port-scan hit should surface via needKey, got %v", needKey)
	}
}

// TestProbeKeyTriState: ProbeKey distinguishes reachable, needs-key, and unreachable.
func TestProbeKeyTriState(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	srv := keyedServer("sk-secret", "pasted-keyed")
	defer srv.Close()

	// Right key -> reachable, models served, key carried.
	if f, st := ProbeKey(srv.URL, "sk-secret"); st != Reachable || len(f.Models) != 1 || f.Key != "sk-secret" {
		t.Errorf("ProbeKey(correct) = %v, %+v", st, f)
	}
	// No key -> needs key (server is present).
	if _, st := ProbeKey(srv.URL, ""); st != NeedsKey {
		t.Errorf("ProbeKey(no key) status = %v, want NeedsKey", st)
	}
	// Dead endpoint -> unreachable.
	if _, st := ProbeKey("http://127.0.0.1:1", "anything"); st != Unreachable {
		t.Errorf("ProbeKey(dead) status = %v, want Unreachable", st)
	}
}

// TestToV1Base normalizes the inputs detection + the wizard accept.
func TestToV1Base(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8081":                            "http://127.0.0.1:8081/v1",
		"http://127.0.0.1:8081":                     "http://127.0.0.1:8081/v1",
		"http://127.0.0.1:8081/":                    "http://127.0.0.1:8081/v1",
		"http://127.0.0.1:8081/v1":                  "http://127.0.0.1:8081/v1",
		"http://127.0.0.1:8081/v1/chat/completions": "http://127.0.0.1:8081/v1",
		"": "",
	}
	for in, want := range cases {
		if got := toV1Base(in); got != want {
			t.Errorf("toV1Base(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMergeOllamaNative: an Ollama base also exposes /api/tags + /api/ps, which list
// installed-but-swapped-out models a bare /v1/models misses. mergeOllamaNative must
// UNION those into f.Models (de-duped, sorted), and a non-Ollama base (no /api/tags)
// must leave the model list untouched.
func TestMergeOllamaNative(t *testing.T) {
	// Ollama-like server: /v1/models shows only the loaded model; /api/tags lists the
	// whole installed fleet; /api/ps repeats a loaded one (must not duplicate).
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{
			{"name": "llama3:8b"}, {"name": "qwen2.5:7b"},
		}})
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{
			{"name": "llama3:8b"}, // already in tags
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := Found{Models: []string{"llama3:8b"}} // the one /v1/models reported (loaded)
	mergeOllamaNative(&f, srv.URL+"/v1")
	want := []string{"llama3:8b", "qwen2.5:7b"} // unioned, de-duped, sorted
	if len(f.Models) != len(want) {
		t.Fatalf("models = %v, want %v", f.Models, want)
	}
	for i := range want {
		if f.Models[i] != want[i] {
			t.Errorf("models[%d] = %q, want %q (full: %v)", i, f.Models[i], want[i], f.Models)
		}
	}

	// A non-Ollama base (no /api/tags) leaves the list untouched.
	bare := httptest.NewServer(http.NotFoundHandler())
	defer bare.Close()
	g := Found{Models: []string{"only-this"}}
	mergeOllamaNative(&g, bare.URL+"/v1")
	if len(g.Models) != 1 || g.Models[0] != "only-this" {
		t.Errorf("non-Ollama base must not change models, got %v", g.Models)
	}
}

// TestDetectWithExplicitUpstreamWins: an explicit --upstream/config endpoint is
// probed FIRST and, when the same server is also reachable via a default probe, the
// explicit entry wins the de-dup so its friendly "configured" name is kept.
func TestDetectWithExplicitUpstreamWins(t *testing.T) {
	defer quietSources(t)()
	srv := fakeServer("up-model")
	defer srv.Close()

	// The SAME server is in the default probe table under a different name; the
	// explicit endpoint must take precedence (probed first, wins de-dup).
	old := probes
	probes = []struct{ name, base string }{{"default-name", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull(srv.URL)
	if len(found) != 1 {
		t.Fatalf("explicit + default for one server should de-dup to 1, got %d: %+v", len(found), found)
	}
	if found[0].Name != "configured" {
		t.Errorf("explicit upstream should win the name, got %q", found[0].Name)
	}
}
