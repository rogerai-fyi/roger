package main

import (
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v5/internal/store"
)

// The quota refusal must NAME THE BAND IN THE WAY, not just state the limit.
//
// THE INCIDENT (2026-08-07): the founder hit "private band limit reached (free plan allows
// 1) - revoke an existing band first" while their one band sat on a model on a DIFFERENT
// machine. The message named an action no client could perform and gave no way to find out
// which band was blocking them. Spec: features/sharing/band_management.feature -
// "The quota refusal names the band in the way, not just the limit".
func TestQuotaRefusalNamesTheBlockingBand(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{
		ID: "band_held", Owner: o.Pubkey, CodeHash: "h1",
		CodeDisplay: "145.225 MHz · ••••-••••", NodeID: "roggentoo-gemma-4-31b",
	})

	_, _, msg := b.mintBandForNode(o, "eager-puma-54-qwen3-vl-8b")
	if msg == "" {
		t.Fatal("a second mint must be refused while the free quota is spent")
	}
	low := strings.ToLower(msg)

	// It must name WHERE the blocking band lives - that is the fact the operator cannot
	// otherwise discover from any surface.
	if !strings.Contains(low, "roggentoo-gemma-4-31b") {
		t.Errorf("the refusal must name the node holding the band, got %q", msg)
	}
	// It must offer moving, which is now possible, as the primary remedy.
	if !strings.Contains(low, "move") {
		t.Errorf("the refusal must offer to MOVE the band, got %q", msg)
	}
	// It must NOT imply a purchase exists - there is no band-pack purchase path.
	for _, forbidden := range []string{"buy", "purchase", "upgrade", "$5", "pack"} {
		if strings.Contains(low, forbidden) {
			t.Errorf("the refusal must not imply a purchase (%q), got %q", forbidden, msg)
		}
	}
	// It must never leak the secret or its hash.
	if strings.Contains(msg, "h1") {
		t.Errorf("the refusal leaked the code hash: %q", msg)
	}
}

// A revoked band frees the quota, so the refusal must stop naming it.
func TestQuotaRefusalIgnoresRevokedBands(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_dead", Owner: o.Pubkey, CodeHash: "h1", NodeID: "old-node", Revoked: true})

	band, _, msg := b.mintBandForNode(o, "new-node")
	if msg != "" {
		t.Fatalf("a revoked band must not block a new mint, got refusal %q", msg)
	}
	if band.NodeID != "new-node" {
		t.Errorf("minted band bound to %q, want the new node", band.NodeID)
	}
}

// The blocking-band lookup must never reach across owners.
func TestQuotaRefusalOnlyConsidersTheCallersBands(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_other", Owner: "someone_else", CodeHash: "h9", NodeID: "not-yours"})

	band, _, msg := b.mintBandForNode(o, "my-node")
	if msg != "" {
		t.Fatalf("another owner's band must not consume this owner's quota, got %q", msg)
	}
	if band.ID == "" {
		t.Error("the mint should have succeeded")
	}
	// And a later refusal must never name a stranger's node.
	_ = b.db.CreateBand(store.Band{ID: "b2", Owner: "someone_else", CodeHash: "h8", NodeID: "secret-node"})
	_, _, msg2 := b.mintBandForNode(o, "another-node")
	if strings.Contains(msg2, "secret-node") || strings.Contains(msg2, "not-yours") {
		t.Errorf("a refusal must never name another owner's node: %q", msg2)
	}
}

// Belt and braces: the quota itself is still enforced. A refusal with a friendlier message
// is still a refusal, and nothing was minted.
func TestQuotaStillBlocksTheSecondMint(t *testing.T) {
	b, o := brokerWithOwner(t)
	if _, _, msg := b.mintBandForNode(o, "node-1"); msg != "" {
		t.Fatalf("the first mint should succeed, got %q", msg)
	}
	band, _, msg := b.mintBandForNode(o, "node-2")
	if msg == "" {
		t.Fatal("the second mint must be refused")
	}
	if band.ID != "" {
		t.Error("a refused mint must not return a band")
	}
	n, _ := b.db.CountActiveBands(o.Pubkey, time.Now())
	if n != 1 {
		t.Errorf("active bands = %d, want 1 (a refused mint must not create one)", n)
	}
}
