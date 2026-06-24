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

	found := Detect()
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

	if found := Detect(); len(found) != 0 {
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

	if found := Detect(); len(found) != 0 {
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

	found := Detect()
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

	found := Detect()
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

	if found := Detect(); len(found) != 1 {
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
		f, ok := Probe(in)
		if !ok || len(f.Models) != 1 || f.Models[0] != "pasted-model" {
			t.Errorf("Probe(%q) failed: ok=%v found=%+v", in, ok, f)
		}
		if f.Chat != srv.URL+"/v1/chat/completions" {
			t.Errorf("Probe(%q) chat url = %q", in, f.Chat)
		}
	}
	// A dead endpoint is not verified.
	if _, ok := Probe("http://127.0.0.1:1/v1"); ok {
		t.Error("Probe of a dead endpoint should not verify")
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
