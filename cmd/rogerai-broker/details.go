package main

// details.go: the broker half of features/details - RogerAI's curated notes, loaded at start
// and stamped onto every published offer of a noted model id (market.go), and the
// rogerai.details block /v1/models carries (models.go, internal/about.Block).

import (
	"log"

	"rogerai.fm/roger/v7/internal/catalog"
)

// mustNotes loads the notes shipped in this binary. A malformed file never ships (the
// catalog tests refuse it), so a failure here is a broken build: stop rather than publish
// RogerAI's models with no description.
func mustNotes() catalog.Notes {
	n, err := catalog.Builtin()
	if err != nil {
		log.Fatalf("curated notes: %v", err)
	}
	return n
}
