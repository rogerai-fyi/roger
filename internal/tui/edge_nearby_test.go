package tui

// The approve flow's one name-matched step (features/edge/empty_edge_simple.feature §7, the spoofed
// and same-named sightings): a request is matched to the primed row by the name the device asked
// with, so when two waiting requests carry that name the row matches neither. Its y and n then fall
// through to the owner-join card, which answers one request with its own phrase on screen.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/store"
)

func TestNearbyRequestIsNeverGuessedBetweenTwoOfOneName(t *testing.T) {
	var m model
	m.own.reqs = []ownerjoin.Request{
		{Dir: "edge:authority", ID: "r1", Name: "deskside", Ask: "deskside wants to join · amber-river-71"},
		{Dir: "/srv/tower", ID: "r0", Name: "deskside", Ask: "a Tower's request, not the Edge's"},
	}
	r, ok := m.edgeNearbyRequest("deskside")
	require.True(t, ok)
	require.Equal(t, "r1", r.ID, "a Tower's request of the same name is not the Edge's")

	m.own.reqs = append(m.own.reqs, ownerjoin.Request{Dir: "edge:authority", ID: "r2", Name: "deskside",
		Ask: "deskside wants to join · quiet-otter-14"})
	_, ok = m.edgeNearbyRequest("deskside")
	require.False(t, ok, "two Edge requests named deskside: the row must not pick one")

	m.edge.nearby.approve, m.edge.nearby.approveN = "n_desk", "deskside"
	m.hooks.Owner = &ownerjoin.Hooks{Decide: func(string, string, bool) error { t.Fatal("decided a guessed request"); return nil }}
	_, handled := m.edgeNearbyDecide(true)
	require.False(t, handled, "y falls through to the owner-join card, which shows the phrase it answers")
}

// A device that is adopting (its claim granted, not joined yet) is still a NEARBY row, so the
// "no devices yet" hint must not be drawn under it (seen live on the simulated hub, 2026-10-12).
func TestNoDevicesHintIsNotDrawnUnderANearbyRow(t *testing.T) {
	var m model
	m.hooks.EdgeAdopting = func() []store.EdgeNode { return []store.EdgeNode{{ID: "n_phone", Name: "gentle-ibex-14"}} }
	var out []string
	m.edgeLookNoDevices(lookPalette(true), func(_ int, s string) { out = append(out, s) })
	require.Empty(t, out)
}
