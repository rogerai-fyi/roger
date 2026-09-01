package main

// Pre-push audit regression: computeMarket accumulated qualitySum over EVERY serving node
// (curated included) but divided by the human-only providers count, so a 1-human+2-curated
// band published Quality ~3.0 on a 0..1 field and inflated computeSignal's trust term.
// The mean must run over every node that contributed to the sum.

import (
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestMixedBandQualityIsTheMeanOverAllServingNodes(t *testing.T) {
	b, _, _, _ := newBandBroker(t)
	now := time.Now()
	add := func(id string, curated bool) {
		b.nodes[id] = protocol.NodeRegistration{
			NodeID: id, Curated: curated, CuratedProvider: map[bool]string{true: "openrouter"}[curated],
			Offers: []protocol.ModelOffer{{Model: "mixq", Ctx: 8192}},
		}
		b.lastSeen[id] = now
	}
	add("h1", false)
	add("c1", true)
	add("c2", true)
	// The human node carries a measured, imperfect score (0.4); the curated pair are
	// unmeasured (score 1.0). The honest band mean is (0.4+1+1)/3 = 0.8.
	b.trust = map[string]trustState{"h1": {recounts: 5, discrepancies: 3}}

	rows, _ := b.computeMarket().(map[string]any)["market"].([]marketView)
	var got *marketView
	for i := range rows {
		if rows[i].Model == "mixq" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("model mixq missing from /market")
	}
	if got.Quality > 1.0 {
		t.Fatalf("Quality = %v: a 0..1 field over 1.0 - curated nodes summed but not counted", got.Quality)
	}
	if got.Quality < 0.79 || got.Quality > 0.81 {
		t.Fatalf("Quality = %v, want the 3-node mean 0.8", got.Quality)
	}
	if got.Providers != 1 || got.CuratedProviders != 2 {
		t.Fatalf("providers=%d curated=%d, want 1/2 counted apart", got.Providers, got.CuratedProviders)
	}
}

func TestPagerCountsCuratedSupplyAsOnAir(t *testing.T) {
	// Pre-push audit round 5: liveModelProviders (the 0-provider pager's source) counted
	// only human Providers, so a model served SOLELY by a live curated station read as
	// off-air - a false "0 providers / requests will fail" page held open over supply
	// that serves requests fine, and a real curated-supply outage invisible.
	b, _, _, _ := newBandBroker(t)
	b.nodes["conly"] = protocol.NodeRegistration{
		NodeID: "conly", Curated: true, CuratedProvider: "openrouter",
		Offers: []protocol.ModelOffer{{Model: "curated-only", Ctx: 8192}},
	}
	b.lastSeen["conly"] = time.Now()
	live := b.liveModelProviders()
	if live["curated-only"] == 0 {
		t.Fatal("a model served solely by a live curated station reads off-air to the pager")
	}
}
