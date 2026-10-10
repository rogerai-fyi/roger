package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// slowHashCounter counts key-store reads by hash and holds each one briefly, the way a real
// store round trip does, so concurrent requests overlap inside the lookup.
type slowHashCounter struct {
	store.Store
	mu sync.Mutex
	n  int
}

func (c *slowHashCounter) AccountKeyByHash(h string) (store.AccountKey, bool, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	return c.Store.AccountKeyByHash(h)
}

// TestUnknownKeyBurstIsBoundedUnderConcurrency pins that the per-address budget for keys that
// do not resolve is taken BEFORE the store read: a concurrent burst of unknown keys from one
// address reaches the key store at most the limiter's burst times, however the requests overlap.
func TestUnknownKeyBurstIsBoundedUnderConcurrency(t *testing.T) {
	counter := &slowHashCounter{Store: store.NewMem()}
	b := newBroker(counter)
	burst := int(b.keyMissLimiter().burst)
	const n = 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Header.Set("Authorization", fmt.Sprintf("Bearer rog-key_burst%064d", i))
			r.Header.Set("CF-Connecting-IP", "203.0.113.9")
			<-start
			b.resolveRelayKey(r)
		}(i)
	}
	close(start)
	wg.Wait()
	if counter.n > burst {
		t.Fatalf("%d concurrent unknown keys reached the key store, want at most the burst of %d", counter.n, burst)
	}
}

// TestALiveKeyNeverSpendsTheMissBudget pins the other half: the token drawn before the lookup
// comes back when the key is live, so a working key from one address is never slowed by the
// budget for keys that do not resolve, however often it is used.
func TestALiveKeyNeverSpendsTheMissBudget(t *testing.T) {
	db := store.NewMem()
	const secret = "rog-key_live000000000000000000000000000000000000000000000000000000"
	if _, err := db.CreateAccountKey(store.AccountKey{ID: "key_live", SecretHash: acctKeyHash(secret), Account: "acct-live", Reset: "none", CreatedAt: time.Now().UnixNano()}, store.MintKeyRules{}); err != nil {
		t.Fatal(err)
	}
	b := newBroker(db)
	burst := int(b.keyMissLimiter().burst)
	for i := 0; i < 10*burst; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("CF-Connecting-IP", "203.0.113.10")
		if _, ok, ref := b.resolveRelayKey(r); !ok || ref != nil {
			t.Fatalf("live key relay %d refused: %+v", i+1, ref)
		}
	}
}
