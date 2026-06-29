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
