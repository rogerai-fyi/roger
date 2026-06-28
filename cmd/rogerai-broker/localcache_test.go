package main

import (
	"strconv"
	"testing"
	"time"
)

// TestLocalCachedJSONReusesWithinTTL: the in-process fallback memoizes a key within its TTL
// (compute runs once), recomputes per distinct key, and recomputes once the entry has expired.
func TestLocalCachedJSONReusesWithinTTL(t *testing.T) {
	b := &broker{} // shared == nil -> the in-process path
	n := 0
	compute := func() any { n++; return map[string]int{"n": n} }

	_ = b.localCachedJSON("k", time.Minute, compute)
	_ = b.localCachedJSON("k", time.Minute, compute)
	if n != 1 {
		t.Errorf("same key within TTL should compute once, got %d", n)
	}
	_ = b.localCachedJSON("other", time.Minute, compute)
	if n != 2 {
		t.Errorf("a distinct key should compute again, got %d", n)
	}
	// ttl 0 -> the entry is born expired, so each call recomputes (covers the expiry branch).
	m := 0
	c2 := func() any { m++; return m }
	_ = b.localCachedJSON("z", 0, c2)
	_ = b.localCachedJSON("z", 0, c2)
	if m != 2 {
		t.Errorf("ttl=0 should recompute every call (expired), got %d", m)
	}
}

// (handler-level flag-off caching is covered by TestServeCachedJSONFlagOff in sharedstore_test.go)

// TestLocalCacheBounded: a flood of distinct keys never grows the map past the cap (it resets),
// so query/identity variety can't leak memory in the no-Redis fallback.
func TestLocalCacheBounded(t *testing.T) {
	b := &broker{}
	for i := 0; i < localCacheCap*3; i++ {
		_ = b.localCachedJSON("k"+strconv.Itoa(i), time.Minute, func() any { return i })
	}
	b.localCacheMu.Lock()
	size := len(b.localCache)
	b.localCacheMu.Unlock()
	if size > localCacheCap+1 {
		t.Errorf("local cache grew to %d entries, want bounded near %d", size, localCacheCap)
	}
}
