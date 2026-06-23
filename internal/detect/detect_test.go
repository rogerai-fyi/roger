package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestProbe exercises the GET /v1/models parsing against a real test server by
// reusing Detect's probe logic over a one-entry probe table.
func TestProbeParsesModels(t *testing.T) {
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
