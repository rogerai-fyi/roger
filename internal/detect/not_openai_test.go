package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 200 does not make something an OpenAI server. Open WebUI (and anything else with a
// browser UI) answers /v1/models with an HTML page; the decode quietly yielded nothing
// and the port was reported as a reachable server with zero models.
//
// That false positive was not cosmetic. On the founder's machine it took the saved
// upstream slot, Controller.Detect short-circuited on it because it was "reachable", and
// the console's SHARE tab was permanently empty with twelve real backends listening. The
// UI's own advice - "try re-detect" - re-took the same short-circuit forever.
func TestHTMLPageIsNotAnOpenAIServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Open WebUI</title></head></html>"))
	}))
	defer srv.Close()

	if _, st := ProbeKey(srv.URL+"/v1", ""); st == Reachable {
		t.Fatal("an HTML page was accepted as an OpenAI-compatible server")
	}
}

// JSON that is not a model listing is the same story with a different body.
func TestJSONWithoutDataIsNotAnOpenAIServer(t *testing.T) {
	for _, body := range []string{`{"status":"ok"}`, `{"models":["a"]}`, `[]`, `"hello"`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		if _, st := ProbeKey(srv.URL+"/v1", ""); st == Reachable {
			t.Fatalf("body %q was accepted as an OpenAI server", body)
		}
		srv.Close()
	}
}

// The distinction that makes this safe: an EMPTY list is a real server between loads -
// llama.cpp with nothing loaded, or a runtime mid-swap. It must stay reachable, or this
// fix would trade one false negative for another.
func TestEmptyModelListIsStillARealServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	f, st := ProbeKey(srv.URL+"/v1", "")
	if st != Reachable {
		t.Fatal("a server reporting an empty model list must stay reachable")
	}
	if len(f.Models) != 0 {
		t.Fatalf("expected no models, got %v", f.Models)
	}
}

// And the ordinary case keeps working.
func TestRealModelListStillDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "qwen3.8-27b", "n_ctx": 8192}},
		})
	}))
	defer srv.Close()

	f, st := ProbeKey(srv.URL+"/v1", "")
	if st != Reachable {
		t.Fatal("a real model listing must be reachable")
	}
	if len(f.Models) != 1 || f.Models[0] != "qwen3.8-27b" {
		t.Fatalf("models = %v", f.Models)
	}
	if f.Ctx["qwen3.8-27b"] != 8192 {
		t.Fatalf("ctx = %v", f.Ctx)
	}
}

// A key-gated server still reports NeedsKey rather than being swallowed: 401 is answered
// before the body is ever read, so this change must not disturb it.
func TestKeyGatedServerStillNeedsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, st := ProbeKey(srv.URL+"/v1", ""); st != NeedsKey {
		t.Fatalf("401 should report NeedsKey, got %v", st)
	}
}
