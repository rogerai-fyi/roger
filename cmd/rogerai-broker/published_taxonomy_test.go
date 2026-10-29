package main

import (
	"testing"

	"rogerai.fm/roger/v7/internal/protocol"
)

// An offer can reach the node map without passing registration (shared-registry mirror,
// lazy learn, DB re-hydrate), so its capabilities may carry an un-normalized spelling. The
// broker's guess must read them canonically, or a declared vision capability is missed.
func TestPublishedTaxonomyReadsCapabilitiesCanonically(t *testing.T) {
	o := protocol.ModelOffer{Model: "some-model", Capabilities: []string{" Vision "}}
	if got := publishedTaxonomy(o, false); got.Category != "vision" || got.Subcategory != "vlm" {
		t.Fatalf("a raw ' Vision ' capability was not read: %+v", got)
	}
}
