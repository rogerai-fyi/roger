package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bownux/rogerai/internal/protocol"
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

// TestPickPinAndExclude verifies the failover routing hints: a pinned node is the
// only candidate, and excluded nodes (the ones a client just saw fail) are skipped.
func TestPickPinAndExclude(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			"a": {NodeID: "a", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5}}},
			"b": {NodeID: "b", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.2}}},
			"c": {NodeID: "c", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.1}}},
		},
		lastSeen:     map[string]time.Time{"a": now, "b": now, "c": now},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}

	// No pin/exclude → cheapest (c).
	if n, _, ok := b.pick("m", false, 0, 0, "", nil); !ok || n.NodeID != "c" {
		t.Errorf("cheapest pick = %q ok=%v, want c", n.NodeID, ok)
	}
	// Exclude the cheapest two → must fall back to a.
	if n, _, ok := b.pick("m", false, 0, 0, "", map[string]bool{"c": true, "b": true}); !ok || n.NodeID != "a" {
		t.Errorf("excluded pick = %q ok=%v, want a", n.NodeID, ok)
	}
	// Pin to b → only b, even though c is cheaper.
	if n, _, ok := b.pick("m", false, 0, 0, "b", nil); !ok || n.NodeID != "b" {
		t.Errorf("pinned pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// Pin to an excluded node → nothing eligible.
	if _, _, ok := b.pick("m", false, 0, 0, "b", map[string]bool{"b": true}); ok {
		t.Error("pin+exclude of the same node should yield nothing")
	}
	// Exclude every node → nothing.
	if _, _, ok := b.pick("m", false, 0, 0, "", map[string]bool{"a": true, "b": true, "c": true}); ok {
		t.Error("excluding all nodes should yield nothing")
	}
}

func TestParseNodeSet(t *testing.T) {
	if parseNodeSet("") != nil {
		t.Error("empty header should be nil set")
	}
	got := parseNodeSet(" a, b ,,c ")
	for _, want := range []string{"a", "b", "c"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("set size = %d want 3 (%v)", len(got), got)
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
