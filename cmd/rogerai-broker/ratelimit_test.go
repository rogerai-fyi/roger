package main

import "testing"

func TestRateLimiter(t *testing.T) {
	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 3}
	// the burst (3) is allowed back to back
	for i := 0; i < 3; i++ {
		if ok, _ := rl.allow("u"); !ok {
			t.Fatalf("burst token %d should be allowed", i)
		}
	}
	// the next one is denied with a positive retry hint
	if ok, retry := rl.allow("u"); ok || retry < 1 {
		t.Errorf("over-burst should deny with retry>=1, got ok=%v retry=%d", ok, retry)
	}
	// a different caller has its own bucket
	if ok, _ := rl.allow("v"); !ok {
		t.Error("a different key should be allowed")
	}
	// rpm<=0 disables limiting; a nil limiter allows
	if ok, _ := (&rateLimiter{rpm: 0}).allow("x"); !ok {
		t.Error("rpm<=0 should allow")
	}
	var nilrl *rateLimiter
	if ok, _ := nilrl.allow("x"); !ok {
		t.Error("nil limiter should allow")
	}
}
