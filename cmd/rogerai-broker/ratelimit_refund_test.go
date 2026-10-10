package main

import "testing"

// TestRateLimiterRefund pins refund: it returns one drawn token, never past the burst, and is a
// no-op for a nil limiter, a disabled one, or a key with no bucket yet.
func TestRateLimiterRefund(t *testing.T) {
	var none *rateLimiter
	none.refund("ip") // nil-safe
	(&rateLimiter{buckets: map[string]*tokenBucket{}}).refund("ip")

	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 2}
	rl.refund("ip") // no bucket yet: nothing to give back
	if _, ok := rl.buckets["ip"]; ok {
		t.Fatal("refund created a bucket")
	}
	for i := 0; i < 2; i++ {
		if ok, _ := rl.allow("ip"); !ok {
			t.Fatalf("draw %d refused", i+1)
		}
	}
	if ok, _ := rl.allow("ip"); ok {
		t.Fatal("a third draw passed an empty bucket of 2")
	}
	rl.refund("ip")
	if ok, _ := rl.allow("ip"); !ok {
		t.Fatal("the refunded token was not available")
	}
	rl.refund("ip")
	rl.refund("ip")
	rl.refund("ip") // past the burst: capped
	if got := rl.buckets["ip"].tokens; got > 2 {
		t.Fatalf("refund overfilled the bucket: %v tokens, burst 2", got)
	}
}
