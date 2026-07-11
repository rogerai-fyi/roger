package client

// capsule_test.go exercises the client publish/fetch transport against a STREAM-FAITHFUL
// in-memory rendezvous stub that matches the broker's /capsule + /capsule/resolve contract
// (store {lookup, blob}; delete-on-read; uniform 404). REAL seal/open crypto - the stub only
// stands in for the content-blind store, never the crypto.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rendezvousStub is a minimal content-blind store: it stores exactly what the client sends
// (lookup -> base64 blob) and hands it back once. It never decrypts.
func rendezvousStub(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	store := map[string]string{} // lookup -> base64(ciphertext)
	mux := http.NewServeMux()
	mux.HandleFunc("/capsule", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup, Blob string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		store[req.Lookup] = req.Blob
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/capsule/resolve", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		blob, ok := store[req.Lookup]
		delete(store, req.Lookup) // delete-on-read (one-time)
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such capsule"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"blob": blob})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

func TestPublishFetchRoundTrip(t *testing.T) {
	srv, store := rendezvousStub(t)
	code, _, _ := protocol.NewRCLinkCode()
	capsuleJSON := []byte(`{"capsule":"roger.context.v1","redaction":"summary"}`)

	if err := PublishCapsule(srv.URL, code, capsuleJSON); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// what landed in the store must be the CIPHERTEXT, not the plaintext.
	stored, _ := base64.StdEncoding.DecodeString(store[capsule.TransportLookup(code)])
	if len(stored) == 0 {
		t.Fatal("nothing stored under the lookup")
	}
	if string(stored) == string(capsuleJSON) {
		t.Fatal("the stub stored plaintext - the client must seal before minting")
	}

	got, err := FetchCapsule(srv.URL, code)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != string(capsuleJSON) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, capsuleJSON)
	}
}

func TestFetchOneTimeThenGone(t *testing.T) {
	srv, _ := rendezvousStub(t)
	code, _, _ := protocol.NewRCLinkCode()
	if err := PublishCapsule(srv.URL, code, []byte(`{"capsule":"roger.context.v1"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := FetchCapsule(srv.URL, code); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := FetchCapsule(srv.URL, code); err != ErrCapsuleGone {
		t.Fatalf("second fetch err = %v, want ErrCapsuleGone (one-time)", err)
	}
}

func TestFetchWrongCodeGone(t *testing.T) {
	srv, _ := rendezvousStub(t)
	code, _, _ := protocol.NewRCLinkCode()
	if err := PublishCapsule(srv.URL, code, []byte(`{"capsule":"roger.context.v1"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// a DIFFERENT code hashes to a different lookup -> the broker never finds it -> gone.
	other, _, _ := protocol.NewRCLinkCode()
	if _, err := FetchCapsule(srv.URL, other); err != ErrCapsuleGone {
		t.Fatalf("wrong-code fetch err = %v, want ErrCapsuleGone", err)
	}
}

func TestPublishRefusesTaillessCode(t *testing.T) {
	srv, _ := rendezvousStub(t)
	if err := PublishCapsule(srv.URL, " --- ", []byte(`{}`)); err == nil {
		t.Fatal("a tail-less code must be refused before any broker call")
	}
}
