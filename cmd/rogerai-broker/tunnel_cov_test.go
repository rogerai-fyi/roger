package main

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// tunnelBroker builds a wired broker with one node + tunnel injected, so the node-facing
// poll/result handlers can be driven without the full register handshake.
func tunnelBroker(t *testing.T) (*broker, *nodeTunnel) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	tn := &nodeTunnel{token: "tok", jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1"}
	b.tunnels["n1"] = tn
	return b, tn
}

// authed builds a node request carrying the tunnel bearer token.
func authed(method, path, token, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// TestAgentPoll covers the node long-poll: unknown node -> 404, bad token -> 401, and a
// queued job is delivered immediately to an authenticated poll.
func TestAgentPoll(t *testing.T) {
	b, tn := tunnelBroker(t)

	// Unknown node -> 404.
	w := httptest.NewRecorder()
	b.agentPoll(w, authed(http.MethodGet, "/agent/poll?node=ghost", "tok", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("poll(unknown) = %d, want 404", w.Code)
	}
	// Bad token -> 401.
	w401 := httptest.NewRecorder()
	b.agentPoll(w401, authed(http.MethodGet, "/agent/poll?node=n1", "wrong", ""))
	if w401.Code != http.StatusUnauthorized {
		t.Fatalf("poll(bad token) = %d, want 401", w401.Code)
	}
	// A queued job is delivered to an authenticated poll (no 25s wait).
	tn.jobs <- protocol.Job{ID: "job-1", Body: []byte(`{"model":"m"}`)}
	wok := httptest.NewRecorder()
	b.agentPoll(wok, authed(http.MethodGet, "/agent/poll?node=n1", "tok", ""))
	if wok.Code != http.StatusOK {
		t.Fatalf("poll(job) = %d, want 200", wok.Code)
	}
	var job protocol.Job
	_ = json.Unmarshal(wok.Body.Bytes(), &job)
	if job.ID != "job-1" {
		t.Errorf("poll delivered job %q, want job-1", job.ID)
	}
}

// TestAgentResult covers the node result POST: unknown node -> 404, bad token -> 401,
// malformed body -> 400, and a valid result is delivered to the waiting relay.
func TestAgentResult(t *testing.T) {
	b, tn := tunnelBroker(t)

	// Unknown node -> 404.
	w := httptest.NewRecorder()
	b.agentResult(w, authed(http.MethodPost, "/agent/result?node=ghost", "tok", `{"id":"x"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("result(unknown) = %d, want 404", w.Code)
	}
	// Bad token -> 401.
	w401 := httptest.NewRecorder()
	b.agentResult(w401, authed(http.MethodPost, "/agent/result?node=n1", "wrong", `{"id":"x"}`))
	if w401.Code != http.StatusUnauthorized {
		t.Fatalf("result(bad token) = %d, want 401", w401.Code)
	}
	// Malformed body -> 400.
	wbad := httptest.NewRecorder()
	b.agentResult(wbad, authed(http.MethodPost, "/agent/result?node=n1", "tok", `{not json`))
	if wbad.Code != http.StatusBadRequest {
		t.Fatalf("result(bad json) = %d, want 400", wbad.Code)
	}
	// A registered waiter receives the posted result.
	ch := make(chan protocol.JobResult, 1)
	tn.mu.Lock()
	tn.waiters["job-9"] = ch
	tn.mu.Unlock()
	wok := httptest.NewRecorder()
	b.agentResult(wok, authed(http.MethodPost, "/agent/result?node=n1", "tok", `{"id":"job-9","status":200}`))
	if wok.Code != http.StatusOK {
		t.Fatalf("result(ok) = %d, want 200: %s", wok.Code, readBody(wok))
	}
	select {
	case got := <-ch:
		if got.ID != "job-9" || got.Status != 200 {
			t.Errorf("waiter got %+v, want job-9/200", got)
		}
	default:
		t.Error("the registered waiter should have received the result")
	}
}

func readBody(w *httptest.ResponseRecorder) string {
	b, _ := io.ReadAll(w.Result().Body)
	return string(b)
}
