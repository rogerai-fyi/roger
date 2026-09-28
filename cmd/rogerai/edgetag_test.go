package main

// Executable pins for features/edge/tags.feature, the CLI half: `roger edge tag`, the group filter
// on `roger edge list`, and a rule scoped to a tag.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func tagSeed(t *testing.T) {
	t.Helper()
	offline(t)
	edgeSeed(t,
		store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified)},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified)},
		store.EdgeNode{ID: "n_pi", Name: "quiet-pi", Kind: "host", Presence: string(edge.PresenceVerified)},
	)
}

func TestEdgeTagShowsAddsRemovesReplacesAndClears(t *testing.T) {
	tagSeed(t)

	out, code := edgeRun(t, "edge", "tag", "desk")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk carries no tags")
	require.Contains(t, out, "roger edge tag desk +production")

	out, code = edgeRun(t, "edge", "tag", "desk", "+Production", "+gpu")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk carries gpu · production")

	out, code = edgeRun(t, "edge", "tag", "desk")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "gpu · production")
	require.Contains(t, out, "--scope tag:gpu", "it says how to make the group a rule")

	out, code = edgeRun(t, "edge", "tag", "desk", "-gpu")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk carries production")
	require.NotContains(t, out, "gpu")

	out, code = edgeRun(t, "edge", "tag", "desk", "bench,lab")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk carries bench · lab")

	out, code = edgeRun(t, "edge", "tag", "desk", "--clear")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk carries no tags now")

	// Refusals: a word that cannot be a tag, mixing a bare word with +/-, an unknown node, and
	// --clear with words.
	out, code = edgeRun(t, "edge", "tag", "desk", "+a b")
	require.NotEqual(t, 0, code, out)
	out, code = edgeRun(t, "edge", "tag", "desk", "prod", "-bench")
	require.NotEqual(t, 0, code, out)
	require.Contains(t, out, "not both")
	out, code = edgeRun(t, "edge", "tag", "nosuch", "+prod")
	require.NotEqual(t, 0, code, out)
	out, code = edgeRun(t, "edge", "tag", "desk", "prod", "--clear")
	require.NotEqual(t, 0, code, out)
	require.Contains(t, out, "--clear takes no tags")
}

func TestEdgeListFiltersByTagAndShowsTheColumn(t *testing.T) {
	tagSeed(t)
	_, code := edgeRun(t, "edge", "tag", "desk", "+production")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "tag", "shed", "+production", "+rack1")
	require.Equal(t, 0, code)

	out, code := edgeRun(t, "edge", "list")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tags", "the column appears once a node carries one")
	require.Contains(t, out, "production")

	out, code = edgeRun(t, "edge", "list", "--tag", "production")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "desk")
	require.Contains(t, out, "shed")
	require.NotContains(t, out, "quiet-pi")

	out, code = edgeRun(t, "edge", "list", "--tag", "Rack1")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "shed")
	require.NotContains(t, out, "desk\n", "normalised, and only the group")

	out, code = edgeRun(t, "edge", "list", "--tag", "nothing")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing on this Edge matches that")

	out, code = edgeRun(t, "edge", "list", "--tag", "a b")
	require.NotEqual(t, 0, code, out)
}

func TestEdgeAlertRuleScopedToATag(t *testing.T) {
	tagSeed(t)
	_, code := edgeRun(t, "edge", "tag", "desk", "+production")
	require.Equal(t, 0, code)

	out, code := edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "70", "--scope", "tag:production")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "every node tagged production")
	require.Contains(t, out, "cpu_temp-tag-production")

	out, code = edgeRun(t, "edge", "alert")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tag production", "the scope reads as a group on every surface")

	// The same rule set again replaces it rather than adding a second.
	_, code = edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "65", "--scope", "tag:Production")
	require.Equal(t, 0, code)
	out, _ = edgeRun(t, "edge", "alert")
	require.Equal(t, 1, strings.Count(out, "tag production"), "one rule per metric per group")
	require.Contains(t, out, "65")

	// A scope that is neither a node nor a valid tag is refused.
	_, code = edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "70", "--scope", "tag:")
	require.NotEqual(t, 0, code)
	_, code = edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "70", "--scope", "tag:a b")
	require.NotEqual(t, 0, code)
	_, code = edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "70", "--scope", "nosuchnode")
	require.NotEqual(t, 0, code)
}
