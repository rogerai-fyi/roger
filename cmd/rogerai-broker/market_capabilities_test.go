package main

// market_capabilities_test.go pins that /market surfaces a chat model's vision capability as the
// UNION across its on-air providers (docs/BROKER-VISION-CAPABILITY.md): a model is vision-capable
// if it can be ROUTED to any provider that reports vision. Real computeMarket over registered
// nodes, no mocks.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// marketCapsFor registers one offer per provider then reads the /market capabilities. Each offer
// is ROUND-TRIPPED THROUGH JSON first, so the test reflects the real node->broker WIRE (omitempty:
// a text-only [] collapses to nil) instead of the pre-marshal Go struct - which was the earlier
// false-green blind spot.
func marketCapsFor(t *testing.T, model string, offers ...[]string) []string {
	t.Helper()
	b := relayBroker(store.NewMem())
	for i, caps := range offers {
		pub, _, _ := ed25519.GenerateKey(nil)
		id := "n" + string(rune('a'+i))
		wire, _ := json.Marshal(protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub),
			Offers: []protocol.ModelOffer{{Model: model, Capabilities: caps}}})
		var reg protocol.NodeRegistration // FRESH: an absent "capabilities" key stays nil (the wire shape)
		_ = json.Unmarshal(wire, &reg)
		b.nodes[id] = reg
		b.lastSeen[id] = time.Now()
	}
	res, _ := b.computeMarket().(map[string]any)
	rows, _ := res["market"].([]marketView)
	for _, r := range rows {
		if r.Model == model {
			return r.Capabilities
		}
	}
	t.Fatalf("model %q not found in /market", model)
	return nil
}

func TestMarketCapabilitiesUnion(t *testing.T) {
	// One provider reports vision, another text-only -> the model is vision-capable (union).
	// "vision" survives the wire; the text-only [] collapses to nil but the union still wins.
	if got := marketCapsFor(t, "m", []string{"vision"}, []string{}); len(got) != 1 || got[0] != "vision" {
		t.Errorf("mixed providers: /market caps = %v, want [vision] (routable to a vision node)", got)
	}
	// All providers text-only -> absent on the WIRE: [] collapses to nil (ModelOffer omitempty,
	// required for the registration possession-proof), so the app name-heuristics. This documents
	// the accepted trade-off; restoring a positive text-only signal is a follow-up off the offer.
	if got := marketCapsFor(t, "m", []string{}, []string{}); got != nil {
		t.Errorf("all-text-only over the wire: /market caps = %v, want nil (text-only [] collapses to absent)", got)
	}
	// No provider declared (old nodes / undetermined) -> omitted (nil) so the app falls back.
	if got := marketCapsFor(t, "m", nil, nil); got != nil {
		t.Errorf("undetermined: /market caps = %v, want nil (omitted -> app heuristic)", got)
	}
	// A single vision provider -> vision.
	if got := marketCapsFor(t, "m", []string{"vision"}); len(got) != 1 || got[0] != "vision" {
		t.Errorf("single vision provider: /market caps = %v, want [vision]", got)
	}
}
