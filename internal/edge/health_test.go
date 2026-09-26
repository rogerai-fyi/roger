package edge_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// features/edge/health.feature §1-2: the fleet exposes each node's LATEST readings and their age in
// one snapshot; a node that reports nothing carries no readings (never zeros); a stale reading is
// kept but labelled with its age; an offline node says how long it has been dark.

func f64(v float64) *float64 { return &v }

func healthFleet(t *testing.T, now time.Time) *edge.Fleet {
	t.Helper()
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	fl.SetClock(func() time.Time { return now })
	// jetson: two instances, the NEWER reading must win.
	jet := store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host", Presence: string(edge.PresenceVerified),
		LastSeen: now.Add(-10 * time.Second).Unix(),
		Instances: []store.EdgeInstance{
			{Name: "old", Resources: &store.EdgeResources{RAMUsedGB: 4, RAMTotalGB: 32, ReadAt: now.Add(-5 * time.Minute).Unix()}},
			{Name: "new", Resources: &store.EdgeResources{RAMUsedGB: 8, RAMTotalGB: 32, CPUTempC: f64(61.5), CPULoad1: f64(1.2), UptimeS: 86400,
				Disks: []store.EdgeDisk{{Mount: "/", UsedGB: 40, TotalGB: 100}}, ReadAt: now.Add(-90 * time.Second).Unix()}},
		}}
	// pi: a board with a reading that has NO total memory figure (a plain figure, not a bar).
	pi := store.EdgeNode{ID: "n_pi", Name: "pi", Kind: "board", Presence: string(edge.PresenceVerified),
		LastSeen:  now.Add(-3 * time.Second).Unix(),
		Instances: []store.EdgeInstance{{Name: "sensor", Resources: &store.EdgeResources{RAMUsedGB: 0.5, ReadAt: now.Unix()}}}}
	// quiet: reports nothing at all.
	quiet := store.EdgeNode{ID: "n_quiet", Name: "quiet", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()}
	// attic: DARK for 2 hours.
	attic := store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "host", Presence: string(edge.PresenceDark),
		LastSeen:  now.Add(-2 * time.Hour).Unix(),
		Instances: []store.EdgeInstance{{Name: "x", Resources: &store.EdgeResources{RAMUsedGB: 1, RAMTotalGB: 8, ReadAt: now.Add(-2 * time.Hour).Unix()}}}}
	for _, n := range []store.EdgeNode{jet, pi, quiet, attic} {
		_, err := fl.Enroll(n)
		require.NoError(t, err)
	}
	return fl
}

func TestFleetHealthIsOneSnapshotWithLatestReadingsAndAges(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	fl := healthFleet(t, now)
	hs, err := edge.FleetHealth(fl, now)
	require.NoError(t, err)
	byName := map[string]edge.NodeHealth{}
	for _, h := range hs {
		byName[h.Name] = h
	}
	require.Len(t, byName, 4)

	jet := byName["jetson"]
	require.NotNil(t, jet.Readings, "jetson reports")
	require.Equal(t, 8.0, jet.Readings.RAMUsedGB, "the NEWEST instance reading wins, not the older 4 GB")
	require.Equal(t, 61.5, *jet.Readings.CPUTempC)
	require.Equal(t, 90*time.Second, jet.ReadAge, "a stale reading is kept and labelled with its age")
	require.False(t, jet.Dark)
	// Memory with a real total is a LEVEL (drawable as a bar).
	lvl, ok := jet.MemoryLevel()
	require.True(t, ok)
	require.InDelta(t, 0.25, lvl, 0.001)

	pi := byName["pi"]
	require.NotNil(t, pi.Readings)
	_, ok = pi.MemoryLevel()
	require.False(t, ok, "a memory figure with no total is NOT a level - a bar would imply a denominator it lacks")

	quiet := byName["quiet"]
	require.Nil(t, quiet.Readings, "a node that reports nothing carries no readings, not zeros")

	attic := byName["attic"]
	require.True(t, attic.Dark)
	require.Equal(t, 2*time.Hour, attic.DarkFor, "an offline node says how long it has been dark")
	require.True(t, attic.LastHeard.Equal(now.Add(-2*time.Hour)), "last heard is the moment it was last seen")
}
