package main

// capsule_multiinstance_test.go pins the SHARED-store, multi-instance behavior of the
// content-blind capsule rendezvous (the prod gap #64 left: a per-instance map does not
// resolve across instances). Two brokers share one Valkey (miniredis, or a real
// ROGERAI_TEST_REDIS_URL): a mint on A resolves on B, and two concurrent resolves race to
// exactly one winner (atomic single-use across instances). Also proves the broker is
// content-blind by INSPECTING what it stores: only {lookup, ciphertext}, never the code/key/
// plaintext.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// sharedBroker builds a broker whose capsule rendezvous is backed by the shared store at url.
func sharedBroker(t *testing.T, url string) *broker {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	vs, err := newValkeyStore(url)
	if err != nil {
		t.Fatalf("valkey: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	b.shared = vs
	return b
}

func TestCapsuleMultiInstanceMintAResolveB(t *testing.T) {
	url := xiRedisURL(t)
	a := sharedBroker(t, url)
	b := sharedBroker(t, url) // a SECOND instance sharing the same store
	_, priv, _ := ed25519.GenerateKey(nil)

	lookup := "lk-cross-instance"
	blob := []byte("opaque-ciphertext-A-minted")

	if w := capsuleMintReq(t, a, priv, lookup, blob, true); w.Code != http.StatusOK {
		t.Fatalf("mint on A status %d: %s", w.Code, w.Body.String())
	}
	// resolve on the OTHER instance must return the exact blob (the #64 gap).
	w := capsuleResolveReq(t, b, lookup)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve on B status %d, want 200 (mint on A must resolve on B): %s", w.Code, w.Body.String())
	}
	var out struct{ Blob string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	got, _ := base64.StdEncoding.DecodeString(out.Blob)
	if string(got) != string(blob) {
		t.Fatalf("resolved blob = %q, want %q", got, blob)
	}
	// one-time across instances: a second resolve on A (the minter) is now a 404 too.
	if w2 := capsuleResolveReq(t, a, lookup); w2.Code != http.StatusNotFound {
		t.Errorf("second resolve on A status %d, want 404 (one-time across instances)", w2.Code)
	}
}

func TestCapsuleMultiInstanceRaceOneWinner(t *testing.T) {
	url := xiRedisURL(t)
	a := sharedBroker(t, url)
	b := sharedBroker(t, url)
	_, priv, _ := ed25519.GenerateKey(nil)

	lookup := "lk-race"
	blob := []byte("exactly-one-resolver-wins")
	if w := capsuleMintReq(t, a, priv, lookup, blob, true); w.Code != http.StatusOK {
		t.Fatalf("mint status %d", w.Code)
	}

	// Two instances resolve the SAME lookup concurrently: exactly one 200, one 404.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	brokers := []*broker{a, b}
	wg.Add(2)
	for i := range brokers {
		i := i
		go func() {
			defer wg.Done()
			codes[i] = capsuleResolveReq(t, brokers[i], lookup).Code
		}()
	}
	wg.Wait()

	wins, misses := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			wins++
		case http.StatusNotFound:
			misses++
		default:
			t.Fatalf("unexpected resolve status %d", c)
		}
	}
	if wins != 1 || misses != 1 {
		t.Fatalf("race must yield exactly one winner: wins=%d misses=%d", wins, misses)
	}
}

// TestCapsuleBrokerContentBlind proves the broker cannot decrypt what it stores: after a
// mint, the shared store holds ONLY the ciphertext under the lookup - not the plaintext, the
// raw code, or the derived key. The plaintext is recoverable ONLY with the raw code.
func TestCapsuleBrokerContentBlind(t *testing.T) {
	url := xiRedisURL(t)
	a := sharedBroker(t, url)
	_, priv, _ := ed25519.GenerateKey(nil)

	code, _, _ := protocol.NewRCLinkCode()
	plaintext := []byte(`{"capsule":"roger.context.v1","messages":[{"content":"SENSITIVE-SECRET-BODY"}]}`)
	sealed, err := capsule.SealForCode(plaintext, code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	lookup := capsule.TransportLookup(code)

	if w := capsuleMintReq(t, a, priv, lookup, sealed, true); w.Code != http.StatusOK {
		t.Fatalf("mint status %d", w.Code)
	}

	// Inspect the raw stored value: it must equal the ciphertext and contain NEITHER the
	// plaintext NOR the code/tail.
	vs := a.shared.(*valkeyStore)
	raw, err := vs.rdb.Get(context.Background(), capsuleKeyPrefix+lookup).Bytes()
	if err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	if string(raw) != string(sealed) {
		t.Fatal("stored value must be exactly the ciphertext")
	}
	if bytes.Contains(raw, plaintext) {
		t.Fatal("the stored blob must NOT contain the plaintext (content-blind)")
	}
	if bytes.Contains(raw, []byte(code)) {
		t.Fatal("the stored blob must NOT contain the raw code")
	}
	// The broker holds no code/key field: the ONLY way back to plaintext is the raw code.
	if _, err := capsule.OpenWithCode(raw, lookup); err == nil {
		t.Fatal("the lookup (all the broker has) must not decrypt the blob")
	}
	got, err := capsule.OpenWithCode(raw, code)
	if err != nil || string(got) != string(plaintext) {
		t.Fatalf("only the raw code recovers the plaintext: err=%v", err)
	}
}
