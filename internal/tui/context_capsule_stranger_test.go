package tui

// context_capsule_stranger_test.go exercises the TUI side of the encrypted stranger transport
// (Stage 3): publish a SUMMARY-ONLY sealed capsule to a content-blind rendezvous stub, resolve
// + merge a fresh-code recall, and the ratification gate. REAL crypto via client/capsule; the
// stub stands in only for the broker store.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// strangerStub is a content-blind rendezvous: stores lookup -> base64 blob, delete-on-read.
func strangerStub(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	store := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/capsule", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup, Blob string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		store[req.Lookup] = req.Blob
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/capsule/resolve", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		blob, ok := store[req.Lookup]
		delete(store, req.Lookup)
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such capsule"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"blob": blob})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

func TestPublishStrangerCapsuleSummaryOnly(t *testing.T) {
	srv, store := strangerStub(t)
	var m model
	m.endpoint = srv.URL
	m.recordTurn("user", "sensitive first turn", "user", nil, nil)
	m.recordTurn("assistant", "SECRET-BODY-should-not-leave-plaintext", "roger", nil, nil)
	m.recordTurn("user", "current turn", "user", nil, nil)

	code, _, _ := protocol.NewRCLinkCode()
	if err := m.publishStrangerCapsule(srv.URL, code); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// content-blind: what landed is ciphertext, not the plaintext body.
	stored, _ := base64.StdEncoding.DecodeString(store[capsule.TransportLookup(code)])
	if len(stored) == 0 {
		t.Fatal("nothing published under the lookup")
	}
	// the guest resolves + opens and gets a SUMMARY-ONLY capsule (redaction floor).
	raw, err := client.FetchCapsule(srv.URL, code)
	if err != nil {
		t.Fatalf("guest fetch: %v", err)
	}
	c, err := capsule.Import(raw)
	if err != nil {
		t.Fatalf("guest import: %v", err)
	}
	if c.Redaction != "summary" {
		t.Fatalf("stranger capsule redaction = %q, want summary", c.Redaction)
	}
	if len(c.Messages) != 1 {
		t.Fatalf("stranger capsule carries %d turns, want 1 (current only)", len(c.Messages))
	}
	if c.Messages[0].Content != "current turn" {
		t.Fatalf("stranger capsule current turn = %q", c.Messages[0].Content)
	}
}

func TestResolveStrangerRecallMergesFreshCode(t *testing.T) {
	srv, _ := strangerStub(t)
	var m model
	m.endpoint = srv.URL
	m.recordTurn("user", "t0", "user", nil, nil)
	m.recordTurn("assistant", "t1", "roger", nil, nil) // ringTurn now 2

	// the guest builds a return capsule (adds turn 2) and publishes under a FRESH recall code.
	_, gpriv, _ := ed25519.GenerateKey(nil)
	ret := capsule.Capsule{
		Capsule:  capsule.Version,
		Thread:   capsule.Thread{BaseWatermark: 3},
		Messages: []capsule.Message{{Role: "user", Content: "guest-added", XRoger: capsule.XRoger{Turn: 2, Agent: "user", TS: 1}}},
	}
	ret.Sign(gpriv)
	retRaw, _ := ret.Marshal()
	recallCode, _, _ := protocol.NewRCLinkCode()
	if err := client.PublishStrangerCapsule(srv.URL, recallCode, retRaw); err == nil {
		// the return capsule is summary? no - it is unredacted. PublishStrangerCapsule refuses
		// a non-summary capsule, so the return path uses PublishCapsule (verify-before-merge is
		// the return-side guard, not the redaction floor). Fall through to PublishCapsule.
		t.Fatal("expected the non-summary return to be refused by the stranger-guard path")
	}
	if err := client.PublishCapsule(srv.URL, recallCode, retRaw); err != nil {
		t.Fatalf("guest publish return: %v", err)
	}

	added, err := m.resolveStrangerRecall(srv.URL, recallCode)
	if err != nil {
		t.Fatalf("resolve recall: %v", err)
	}
	if added != 1 {
		t.Fatalf("recall merged %d turns, want 1", added)
	}
	if m.ringTurn != 3 || m.ring[len(m.ring)-1].Content != "guest-added" {
		t.Fatalf("recall did not append-only merge: ringTurn=%d last=%q", m.ringTurn, m.ring[len(m.ring)-1].Content)
	}
}

func TestStrangerHandoffBrokerGate(t *testing.T) {
	var m model
	m.endpoint = "https://broker.example"
	// default: OFF (no opt-in env) even with a known broker.
	t.Setenv("ROGERAI_CAPSULE_STRANGER", "")
	if got := m.strangerHandoffBroker(); got != "" {
		t.Fatalf("stranger handoff must be OFF by default, got %q", got)
	}
	// opt-in + known broker -> enabled.
	t.Setenv("ROGERAI_CAPSULE_STRANGER", "1")
	if got := m.strangerHandoffBroker(); got != m.endpoint {
		t.Fatalf("enabled gate must return the broker, got %q", got)
	}
	// opt-in but NO broker -> still off.
	m.endpoint = ""
	if got := m.strangerHandoffBroker(); got != "" {
		t.Fatalf("no broker => off, got %q", got)
	}
}

func TestPublishStrangerEmptyRingNoop(t *testing.T) {
	srv, store := strangerStub(t)
	var m model
	code, _, _ := protocol.NewRCLinkCode()
	if err := m.publishStrangerCapsule(srv.URL, code); err != nil {
		t.Fatalf("empty-ring publish: %v", err)
	}
	if len(store) != 0 {
		t.Fatal("an empty ring must publish nothing")
	}
}
