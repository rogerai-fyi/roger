package tui

// The tree's width threshold and the cell-true cut that keeps every row inside it
// (features/edge/enrolled_edge_look.feature). The tree needs 61 content columns, so a 65-column frame
// is the first that draws it; 70 columns sits inside the band no other golden covers.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestEdgeTreeThresholdIsPinned(t *testing.T) {
	require.False(t, edgeMapFitsTree(60))
	require.True(t, edgeMapFitsTree(61))
	f := newEnrolledLook(t)
	for _, tc := range []struct {
		w    int
		tree bool
	}{{64, false}, {65, true}, {70, true}, {73, true}} {
		m := f.model(t, tc.w, 24)
		require.Equal(t, tc.tree, m.edgeListNote(tc.w) != edgeNarrowNote, "at %d columns", tc.w)
	}
	m := f.model(t, 70, 30)
	out := lookFrame(m)
	for _, ln := range strings.Split(out, "\n") {
		require.LessOrEqual(t, lipgloss.Width(ln), 70, "%q", ln)
	}
	require.NotContains(t, out, "pane: the list", "70x30 draws the tree")
	require.NoError(t, goldenFile(t, "enrolled_edge_70x30.golden", out))
}

// A cut counts display cells, not runes: a name in wide characters is cut to the width it is given.
func TestEdgeTruncCountsCells(t *testing.T) {
	for _, tc := range []struct {
		s    string
		w    int
		want string
	}{
		{"garage", 10, "garage"},
		{"workshop-bench", 8, "worksho…"},
		{"作業場のサーバー", 8, "作業場…"},
		{"作業場のサーバー", 16, "作業場のサーバー"},
		{"作業場のサーバー", 1, "…"},
		{"garage", 0, "…"},
	} {
		got := edgeTrunc(tc.s, tc.w)
		require.Equal(t, tc.want, got, "edgeTrunc(%q, %d)", tc.s, tc.w)
		require.LessOrEqual(t, lipgloss.Width(got), max(1, tc.w))
	}
}
