package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// ptBroker builds a fully-wired in-memory broker (real constructor, no goroutines).
func ptBroker(t *testing.T) *broker {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	return buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
}

// ptAddNode registers one ONLINE node offering `model` at `priceOut` ($/1M out).
func ptAddNode(b *broker, id, model string, priceOut float64, private bool) {
	b.nodes[id] = protocol.NodeRegistration{
		NodeID: id, Offers: []protocol.ModelOffer{{Model: model, PriceOut: priceOut, Ctx: 4096}},
	}
	b.lastSeen[id] = time.Now()
	if private {
		b.private[id] = true
	}
}

// ptAddOfflineNode registers a priced node whose heartbeat has aged out (offline).
func ptAddOfflineNode(b *broker, id, model string, priceOut float64) {
	b.nodes[id] = protocol.NodeRegistration{
		NodeID: id, Offers: []protocol.ModelOffer{{Model: model, PriceOut: priceOut, Ctx: 4096}},
	}
	b.lastSeen[id] = time.Now().Add(-2 * time.Hour)
}

func ptDiscover(t *testing.T, b *broker) map[string]int {
	t.Helper()
	res := b.computeDiscover().(map[string]any)
	tiers := map[string]int{}
	for _, o := range res["offers"].([]offerView) {
		tiers[o.NodeID] = o.PriceTier
	}
	return tiers
}

// TestDiscoverInternalMedianTiers: a model with NO external reference is graded vs its
// live median over the offers in /discover (spec: internal-median fallback).
func TestDiscoverInternalMedianTiers(t *testing.T) {
	b := ptBroker(t)
	// "niche-x" is not in refPriceSeed -> internal-median fallback. median 0.10.
	ptAddNode(b, "n1", "niche-x", 0.04, false) // 0.4 -> $
	ptAddNode(b, "n2", "niche-x", 0.10, false) // 1.0 -> $$
	ptAddNode(b, "n3", "niche-x", 0.10, false)
	ptAddNode(b, "n4", "niche-x", 0.12, false) // 1.2 -> $$$
	got := ptDiscover(t, b)
	if got["n1"] != 1 || got["n2"] != 2 || got["n4"] != 3 {
		t.Errorf("internal-median tiers = %v, want n1=1 n2=2 n4=3", got)
	}
}

// TestDiscoverExternalRefTiers: a seeded model (qwen3-8b, ref 0.20) is graded vs the
// external reference, which also takes PRECEDENCE over the internal median (n4 at the
// reference price is $$$$ externally, but only $$$ vs the median 0.11).
func TestDiscoverExternalRefTiers(t *testing.T) {
	b := ptBroker(t)
	ptAddNode(b, "n1", "qwen3-8b", 0.04, false) // 0.20 -> $
	ptAddNode(b, "n2", "qwen3-8b", 0.10, false) // 0.50 -> $$
	ptAddNode(b, "n3", "qwen3-8b", 0.12, false) // 0.60 -> $$$
	ptAddNode(b, "n4", "qwen3-8b", 0.20, false) // 1.00 -> $$$$ (ref precedence)
	got := ptDiscover(t, b)
	if got["n1"] != 1 || got["n2"] != 2 || got["n3"] != 3 || got["n4"] != 4 {
		t.Errorf("external-ref tiers = %v, want 1/2/3/4", got)
	}
}

// TestDiscoverFreeBandIsTierZero: a band priced 0 is FREE (tier 0), never a $ tier.
func TestDiscoverFreeBandIsTierZero(t *testing.T) {
	b := ptBroker(t)
	ptAddNode(b, "n1", "qwen3-8b", 0.0, false)
	if got := ptDiscover(t, b); got["n1"] != 0 {
		t.Errorf("free band tier = %d, want 0", got["n1"])
	}
}

// TestDiscoverThinMarketWithholdsTier: a model with no reference and <3 online bands
// gets no tier (UNKNOWN), so a noisy 1-2 band median never mislabels anyone.
func TestDiscoverThinMarketWithholdsTier(t *testing.T) {
	b := ptBroker(t)
	ptAddNode(b, "n1", "niche-y", 0.10, false)
	ptAddNode(b, "n2", "niche-y", 0.50, false)
	if got := ptDiscover(t, b); got["n1"] != 0 {
		t.Errorf("thin-market tier = %d, want 0", got["n1"])
	}
}

// TestDiscoverOfflineExcludedFromMedian: offline bands do not count toward the
// internal-median fallback, so 2 online + 3 offline (no external ref) stays UNKNOWN.
func TestDiscoverOfflineExcludedFromMedian(t *testing.T) {
	b := ptBroker(t)
	ptAddNode(b, "on1", "niche-z", 0.10, false)
	ptAddNode(b, "on2", "niche-z", 0.50, false)
	ptAddOfflineNode(b, "off1", "niche-z", 0.01)
	ptAddOfflineNode(b, "off2", "niche-z", 0.02)
	ptAddOfflineNode(b, "off3", "niche-z", 0.03)
	if got := ptDiscover(t, b); got["on1"] != 0 {
		t.Errorf("online band tier = %d, want 0 (offline must not count toward the median)", got["on1"])
	}
}

// TestPrivateBandCarriesTier: a private band carries the SAME tier (vs the external
// reference) as the public feed would (single source of truth).
func TestPrivateBandCarriesTier(t *testing.T) {
	b := ptBroker(t)
	ptAddNode(b, "priv1", "qwen3-8b", 0.04, true)
	out, ok := b.bandOffers(store.Band{NodeID: "priv1"}, true, time.Now())
	if !ok || len(out) == 0 {
		t.Fatal("bandOffers should return the private band's offers")
	}
	if out[0].PriceTier != 1 {
		t.Errorf("private band tier = %d, want 1 ($, vs ref 0.20)", out[0].PriceTier)
	}
}

// TestSyncRefPricesMergeAndLastKnown: a successful sync MERGES (overriding the seed); a
// subsequent FAILED sync keeps the last-known value (classification never needs a live fetch).
func TestSyncRefPricesMergeAndLastKnown(t *testing.T) {
	b := ptBroker(t)
	orig := openRouterFetch
	t.Cleanup(func() { openRouterFetch = orig })

	openRouterFetch = func(context.Context) ([]byte, error) {
		return []byte(`{"data":[{"id":"qwen/qwen3-8b","pricing":{"completion":"0.0000003"}}]}`), nil
	}
	if n := b.syncRefPricesOnce(context.Background()); n == 0 {
		t.Fatal("a good sync should merge at least one model")
	}
	if v, ok := b.refOut("qwen3-8b"); !ok || !approxEq(v, 0.30) {
		t.Errorf("synced ref = %v ok=%v, want 0.30 (overrides seed 0.20)", v, ok)
	}

	openRouterFetch = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("boom") }
	b.syncRefPricesOnce(context.Background())
	if v, _ := b.refOut("qwen3-8b"); !approxEq(v, 0.30) {
		t.Errorf("a failed sync should keep last-known 0.30, got %v", v)
	}
}

// TestRefPriceSyncStopsOnClosedChannel: the sync loop primes once then returns at once on
// a closed stop channel (the testable half of the production nil-channel loop).
func TestRefPriceSyncStopsOnClosedChannel(t *testing.T) {
	b := ptBroker(t)
	orig := openRouterFetch
	t.Cleanup(func() { openRouterFetch = orig })
	openRouterFetch = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("skip") }

	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	go func() { b.refPriceSync(stop); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refPriceSync did not return on a closed stop channel")
	}
}
