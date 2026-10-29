package fleetview

// features/edge/fleet_window.feature §1 and §7: the worst unit first, stable within a group, an
// owner's own order on s, and / narrowing by what an owner would type.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func names(items []FleetItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func rackItems(now time.Time) []FleetItem {
	hot, cool := 88.0, 41.0
	return []FleetItem{
		{Kind: ItemHub, Name: "desk"},
		{Kind: ItemInstance, Name: "roger"},
		{Kind: ItemMember, Name: "nas", NodeKind: "host", LastSeen: now.Add(-3 * time.Second), Controllable: true,
			Health: &edge.NodeHealth{Readings: &store.EdgeResources{CPUTempC: &cool}}},
		{Kind: ItemMember, Name: "attic", NodeKind: "host", Dark: true, LastSeen: now.Add(-2 * time.Hour)},
		{Kind: ItemMember, Name: "greenhouse", NodeKind: "host", Alerts: []string{"cpu_temp > 85"}, LastSeen: now.Add(-1 * time.Second),
			Controllable: true, Tags: []string{"outdoor"}, Health: &edge.NodeHealth{Readings: &store.EdgeResources{CPUTempC: &hot}}},
		{Kind: ItemMember, Name: "nina", NodeKind: "mobile", LastSeen: now.Add(-9 * time.Second)},
		{Kind: ItemMember, Name: "bench", NodeKind: "host", LastSeen: now.Add(-1 * time.Second), Controllable: true},
		{Kind: ItemCandidate, Name: "raspberrypi", NodeKind: "host"},
	}
}

func TestTheWorstUnitIsAtTheTopAndTheOrderIsStable(t *testing.T) {
	now := time.Now()
	got := arrange(rackItems(now), orderWorst, "", now)
	require.Equal(t, []string{"desk", "roger", "attic", "greenhouse", "nina", "nas", "bench", "raspberrypi"}, names(got),
		"dark, then trouble, then heard-only, then on air - each group in the order it arrived; this machine first, the bench last")
	// Again: nothing jumps while it is being read.
	require.Equal(t, names(got), names(arrange(rackItems(now), orderWorst, "", now)))
}

func TestTheOwnerCanOrderTheRackTheirOwnWay(t *testing.T) {
	now := time.Now()
	require.Equal(t, []string{"attic", "bench", "greenhouse", "nas", "nina"}, members(arrange(rackItems(now), orderName, "", now)))
	require.Equal(t, []string{"greenhouse", "nas", "attic", "nina", "bench"}, members(arrange(rackItems(now), orderTemp, "", now)),
		"hottest first; units with no reading after, in arrival order")
	require.Equal(t, []string{"greenhouse", "bench", "nas", "nina", "attic"}, members(arrange(rackItems(now), orderHeard, "", now)),
		"most recently heard first")
	require.Equal(t, []string{"nas", "attic", "greenhouse", "bench", "nina"}, members(arrange(rackItems(now), orderKind, "", now)))
	require.Equal(t, orderName, nextOrder(orderWorst))
	require.Equal(t, orderWorst, nextOrder(orderTemp))
}

func members(items []FleetItem) []string {
	var out []string
	for _, it := range items {
		if it.Kind == ItemMember {
			out = append(out, it.Name)
		}
	}
	return out
}

func TestSlashNarrowsByWhatAnOwnerWouldType(t *testing.T) {
	now := time.Now()
	for q, want := range map[string][]string{
		"green":         {"greenhouse"},
		"kind:phone":    {"nina"},
		"tag:outdoor":   {"greenhouse"},
		"state:dark":    {"attic"},
		"state:heard":   {"nina"},
		"state:trouble": {"greenhouse"},
		"KIND:PHONE":    {"nina"},
		"nothing-here":  nil,
	} {
		require.Equal(t, want, members(arrange(rackItems(now), orderWorst, q, now)), q)
	}
	// The machine you are at and its rogers are never filtered away; the bench is filtered too.
	got := names(arrange(rackItems(now), orderWorst, "raspberry", now))
	require.Equal(t, []string{"desk", "roger", "raspberrypi"}, got)
	require.Equal(t, 5, hiddenBy(rackItems(now), "raspberry", now), "the five units it does not match")
	require.Zero(t, hiddenBy(rackItems(now), "", now))
}
