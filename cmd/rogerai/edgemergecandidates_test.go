package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// A candidate is kept across a missed pass, but only until the discovery TTL - past that it is
// stale (the device left) and must be dropped, not shown as "last seen an hour ago" forever.
func TestEdgeMergeCandidatesExpiresStale(t *testing.T) {
	now := time.Now()
	fresh := []store.EdgeNode{{ID: "n_live", Name: "gentle-ibex-14", LastSeen: now.Unix()}}
	old := []store.EdgeNode{
		{ID: "n_live", Name: "gentle-ibex-14", LastSeen: now.Add(-2 * time.Hour).Unix()}, // superseded by fresh
		{ID: "n_recent", LastSeen: now.Add(-1 * time.Minute).Unix()},                     // within TTL: kept
		{ID: "n_stale", LastSeen: now.Add(-1 * time.Hour).Unix()},                        // past TTL: dropped
	}
	got := edgeMergeCandidates(old, fresh)

	byID := map[string]store.EdgeNode{}
	for _, c := range got {
		byID[c.ID] = c
	}
	// The live one uses the FRESH timestamp, not the stale duplicate.
	require.Contains(t, byID, "n_live")
	require.Equal(t, now.Unix(), byID["n_live"].LastSeen)
	// A recently-seen candidate survives a missed pass.
	require.Contains(t, byID, "n_recent")
	// A candidate past the TTL is gone.
	require.NotContains(t, byID, "n_stale")
	_ = edge.DefaultTTL
}
