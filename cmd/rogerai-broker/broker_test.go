package main

import (
	"strings"
	"testing"
	"time"
)

func TestLockedPrice(t *testing.T) {
	b := &broker{quotes: map[string]priceQuote{}, lockWin: time.Hour}

	// first use → quote + lock at current price
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.20, 0.30); in != 0.20 || out != 0.30 {
		t.Fatalf("first quote = %v/%v, want 0.20/0.30", in, out)
	}
	// owner RAISES → user still billed the locked price (protection)
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.50, 0.80); in != 0.20 || out != 0.30 {
		t.Errorf("raise not protected: %v/%v, want 0.20/0.30", in, out)
	}
	// owner CUTS → user gets the lower price (min)
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.05, 0.05); in != 0.05 || out != 0.05 {
		t.Errorf("cut not passed through: %v/%v, want 0.05/0.05", in, out)
	}
	// a different user is quoted independently at the current price
	if in, _, _ := b.lockedPrice("other", "n", "m", 0.50, 0.80); in != 0.50 {
		t.Errorf("other user quote = %v, want 0.50", in)
	}
	// after the window expires → re-quote at current
	b.quotes["u|n|m"] = priceQuote{in: 0.20, out: 0.30, until: time.Now().Add(-time.Minute)}
	if in, _, _ := b.lockedPrice("u", "n", "m", 0.40, 0.40); in != 0.40 {
		t.Errorf("post-expiry re-quote = %v, want 0.40", in)
	}
}

func TestVerifyAttestation(t *testing.T) {
	if verifyAttestation("") || verifyAttestation("short") || verifyAttestation("dev-placeholder-attestation") {
		t.Error("empty/short/placeholder attestation must not pass")
	}
	if !verifyAttestation(strings.Repeat("a", 64)) {
		t.Error("a 64+ char attestation should pass the stub")
	}
}
