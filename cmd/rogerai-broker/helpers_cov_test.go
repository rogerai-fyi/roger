package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestParsePrefAndWeights covers the routing-preference parse + its weight anchors.
func TestParsePrefAndWeights(t *testing.T) {
	cases := map[string]pref{
		"cheap": prefCheap, "fast": prefFast, "reliable": prefReliable,
		"balanced": prefBalanced, "": prefBalanced, "GARBAGE": prefBalanced, " Fast ": prefFast,
	}
	for in, want := range cases {
		if got := parsePref(in); got != want {
			t.Errorf("parsePref(%q) = %v, want %v", in, got, want)
		}
	}
	// Each preference yields a distinct, populated weight set.
	for _, p := range []pref{prefBalanced, prefCheap, prefFast, prefReliable} {
		w := p.weights()
		if w.kPrice <= 0 || w.beta <= 0 || w.c <= 0 {
			t.Errorf("weights(%v) = %+v, want all positive anchors", p, w)
		}
	}
	// cheap weights price more heavily than fast (a sanity check the table differs).
	if prefCheap.weights().kPrice <= prefFast.weights().kPrice {
		t.Error("cheap should weight price more than fast")
	}
}

// TestAllow covers the method-guard helper: a matching method passes; a mismatch writes
// 405 + an Allow header.
func TestAllow(t *testing.T) {
	w := httptest.NewRecorder()
	if !allow(w, httptest.NewRequest(http.MethodGet, "/x", nil), http.MethodGet) {
		t.Error("allow should pass a matching method")
	}
	w2 := httptest.NewRecorder()
	if allow(w2, httptest.NewRequest(http.MethodPost, "/x", nil), http.MethodGet) {
		t.Error("allow should reject a mismatched method")
	}
	if w2.Code != http.StatusMethodNotAllowed || w2.Header().Get("Allow") != http.MethodGet {
		t.Errorf("mismatch = %d / Allow %q, want 405 / GET", w2.Code, w2.Header().Get("Allow"))
	}
}

// countingGetter is a fake trust.HTTPSGetter that records how many real fetches happen,
// so the cachingGetter's hit/miss behaviour is observable. It fakes the EXTERNAL AMD-KDS
// fetch boundary, not the cache under test.
type countingGetter struct {
	calls int
	fail  bool
}

func (c *countingGetter) Get(url string) ([]byte, error) {
	c.calls++
	if c.fail {
		return nil, fmt.Errorf("kds down")
	}
	return []byte("body-for:" + url), nil
}

// TestCachingGetter covers the AMD-KDS cache wrapper: first fetch hits the inner getter,
// a repeat within the TTL is served from cache (no second fetch), and an inner error
// propagates (and is not cached).
func TestCachingGetter(t *testing.T) {
	inner := &countingGetter{}
	g := &cachingGetter{inner: inner, ttl: time.Minute, cache: map[string]cacheEntry{}}

	b1, err := g.Get("https://kds/vcek")
	if err != nil || string(b1) != "body-for:https://kds/vcek" {
		t.Fatalf("first Get = %q / %v", b1, err)
	}
	if _, err := g.Get("https://kds/vcek"); err != nil { // cache hit
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Errorf("inner fetched %d times, want 1 (second served from cache)", inner.calls)
	}

	// An error from the inner getter propagates and is not cached.
	failing := &countingGetter{fail: true}
	gf := &cachingGetter{inner: failing, ttl: time.Minute, cache: map[string]cacheEntry{}}
	if _, err := gf.Get("https://kds/ask"); err == nil {
		t.Error("a failing inner Get should propagate the error")
	}
	if _, err := gf.Get("https://kds/ask"); err == nil || failing.calls != 2 {
		t.Errorf("an errored fetch must not be cached (calls=%d)", failing.calls)
	}
}
