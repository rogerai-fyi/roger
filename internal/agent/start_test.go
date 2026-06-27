package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestStartServesAJobAndStops drives the full node serve loop: Start registers with a
// fake broker, a poll returns one job, the worker relays it to a fake upstream, posts
// the result back, and folds the usage into the session counters. Then Stop ends it.
// Covers Start, loadOrCreateKey, pollLoop (the 200/job + 204 branches), serve,
// postResult, and recordIf.
func TestStartServesAJobAndStops(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp) // loadOrCreateKey writes here (Linux)
	t.Setenv("HOME", tmp)            // ...and here on macOS

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":11}}`))
	}))
	defer upstream.Close()

	var polls, results atomic.Int64
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/nodes/register"):
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/agent/poll"):
			if polls.Add(1) == 1 { // hand out exactly one job, then idle
				_ = json.NewEncoder(w).Encode(protocol.Job{
					ID: "job-1", User: "u", Body: json.RawMessage(`{"model":"m","messages":[]}`),
				})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/agent/result"):
			results.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default: // heartbeat + anything else
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	defer broker.Close()

	sess, err := Start(Config{
		Broker: broker.URL, Upstream: upstream.URL, NodeID: "n-test",
		Model: "m", PriceIn: 1, PriceOut: 2, Parallel: 1,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Stop()

	// Wait until the job is served and its result posted (or time out).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reqs, _ := sess.Served(); reqs >= 1 && results.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	reqs, toks := sess.Served()
	if reqs < 1 || toks < 11 {
		t.Fatalf("Served = %d reqs / %d toks, want >=1 / >=11", reqs, toks)
	}
	if results.Load() < 1 {
		t.Error("the served job's result was never posted to the broker")
	}
	// Earnings accrued from the served job (cost * (1-fee), > 0).
	if sess.Earnings() <= 0 {
		t.Error("Earnings should be > 0 after serving a priced job")
	}
}

// TestStartRegisterFailure: Start surfaces a broker registration rejection instead of
// returning a live session.
func TestStartRegisterFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer broker.Close()
	if _, err := Start(Config{Broker: broker.URL, NodeID: "n", Model: "m"}); err == nil {
		t.Fatal("Start should fail when the broker rejects registration")
	}
}

// TestFetchChallenge covers the attestation nonce fetch: a well-formed challenge is
// returned; an empty nonce and a non-200 are both errors.
func TestFetchChallenge(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.AttestChallenge{Nonce: "abcd", Expires: 1 << 40})
	}))
	defer ok.Close()
	ch, err := fetchChallenge(ok.URL)
	if err != nil || ch.Nonce != "abcd" {
		t.Fatalf("fetchChallenge = %+v, %v; want nonce abcd", ch, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.AttestChallenge{Nonce: ""})
	}))
	defer empty.Close()
	if _, err := fetchChallenge(empty.URL); err == nil {
		t.Error("an empty nonce must error")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := fetchChallenge(bad.URL); err == nil {
		t.Error("a non-200 challenge response must error")
	}
}

// TestAttestForRegistrationNoTEE: with a valid nonce but no TEE device on the host,
// attestForRegistration fails at quote generation (so a non-confidential host never
// emits a fake confidential claim).
func TestAttestForRegistrationNoTEE(t *testing.T) {
	if detectTEE() != "" {
		t.Skip("running on real TEE hardware; the no-TEE failure path is not reachable")
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.AttestChallenge{Nonce: "deadbeef", Expires: 1 << 40})
	}))
	defer broker.Close()
	_, priv, _ := ed25519.GenerateKey(nil)
	reg := &protocol.NodeRegistration{NodeID: "n", Confidential: true}
	if err := attestForRegistration(broker.URL, priv, reg); err == nil {
		t.Error("attestForRegistration should fail with no TEE present")
	}
}
