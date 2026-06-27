package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestSignalAndLoadHelpers covers the pure market/router math: offerSignal, loadFactor,
// and ucbRadius across their branches.
func TestSignalAndLoadHelpers(t *testing.T) {
	if offerSignal(false, 0, 0, 0, 0, 0, 0, false) != 0 {
		t.Error("an offline offer should signal 0")
	}
	if offerSignal(true, 0, 100, 50, 0.99, 0.9, 1, true) <= 0 {
		t.Error("a healthy online offer should signal > 0")
	}
	if loadFactor(0, 0) != 1.0 { // capacity clamped to 1, inflight 0 -> 1/(1+0)=1
		t.Errorf("loadFactor(idle) = %v, want 1.0", loadFactor(0, 0))
	}
	if lf := loadFactor(3, 3); lf >= 1.0 || lf <= 0 {
		t.Errorf("loadFactor(loaded) = %v, want in (0,1)", lf)
	}
	if ucbRadius(0, 100, 0, 0, 0) != 0 { // C=0 -> deterministic, no lift
		t.Error("ucbRadius(C=0) should be 0")
	}
	if ucbRadius(0.3, 100, 0, 0, 0) <= 0 {
		t.Error("ucbRadius(fresh node) should be > 0")
	}
}

// TestProbeStaleness covers the probe recency helpers: measurementStale (zero/fresh/old)
// and stalenessFactor (off / fresh / aged).
func TestProbeStaleness(t *testing.T) {
	c := probeConfig{ceiling: time.Hour}
	now := time.Now()
	if !c.measurementStale(time.Time{}, now) {
		t.Error("a never-measured node is stale")
	}
	if c.measurementStale(now.Add(-time.Minute), now) {
		t.Error("a node measured a minute ago is fresh")
	}
	if !c.measurementStale(now.Add(-2*time.Hour), now) {
		t.Error("a node measured 2h ago (> ceiling) is stale")
	}
	if (probeConfig{}).stalenessFactor(time.Hour) != 1.0 {
		t.Error("no ceiling -> staleness factor 1.0")
	}
	if c.stalenessFactor(time.Minute) != 1.0 {
		t.Error("within ceiling -> 1.0")
	}
	if f := c.stalenessFactor(90 * time.Minute); f >= 1.0 || f < 0.7 {
		t.Errorf("aged staleness factor = %v, want in [0.7,1.0)", f)
	}
}

// TestStripeModeAndOwnsNode covers the billing-mode label + the node-ownership check.
func TestStripeModeAndOwnsNode(t *testing.T) {
	b := &broker{}
	b.bill.secretKey = "sk_live_abc"
	if b.stripeMode() != "live" {
		t.Errorf("stripeMode(live) = %q", b.stripeMode())
	}
	b.bill.secretKey = "sk_test_abc"
	if b.stripeMode() != "test" {
		t.Errorf("stripeMode(test) = %q", b.stripeMode())
	}
	b.bill.secretKey = ""
	if b.stripeMode() != "disabled" {
		t.Errorf("stripeMode(none) = %q", b.stripeMode())
	}

	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	wallet := protocol.UserIDFromPubkey(pubHex)
	if !b.ownsNode(wallet, pubHex) {
		t.Error("the pubkey-derived wallet should own its node")
	}
	if b.ownsNode("", pubHex) || b.ownsNode(wallet, "") || b.ownsNode("other", pubHex) {
		t.Error("a mismatch / empty arg must not own the node")
	}
}
