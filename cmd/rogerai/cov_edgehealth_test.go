package main

// Coverage for `roger edge health`: the live screen's read with a node over its line, and one node in
// full - every reading it can carry, the ones it cannot, and a name that is not on this Edge.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeHealthSourceCarriesWhatIsFiring(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 99.0
	edgeSeed(t, store.EdgeNode{ID: "n_hot", Name: "hot-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Instances: []store.EdgeInstance{{Name: "sensor", Resources: &store.EdgeResources{CPUTempC: &temp, ReadAt: now.Unix()}}}})
	snap, err := edgeHealthSource()
	require.NoError(t, err)
	require.Equal(t, []string{"cpu_temp > 85"}, snap.Firing["n_hot"])
	require.Contains(t, snap.Sinks, "sinks: ")
	var state string
	for _, r := range snap.Rules {
		if r.ID == "cpu_temp" {
			state = r.State
		}
	}
	require.NotEmpty(t, state)
}

func TestEdgeHealthOneNodeInFull(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp, load, wear := 51.5, 0.42, 12.0
	edgeSeed(t,
		store.EdgeNode{ID: "n_jet", Name: "jetson", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
			Instances: []store.EdgeInstance{{Name: "roger", Resources: &store.EdgeResources{
				CPUTempC: &temp, CPULoad1: &load, RAMUsedGB: 3.5, UptimeS: 7200, GPU: "Orin", GPUMemUsed: 0.25,
				SDWearPct: &wear, Disks: []store.EdgeDisk{{Mount: "/", UsedGB: 10, TotalGB: 40}}, ReadAt: now.Unix()}}}},
		store.EdgeNode{ID: "n_bare", Name: "bare", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()},
	)

	out, code := edgeRun(t, "edge", "health", "jetson")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "jetson · board")
	require.Contains(t, out, "cpu temp   51.5°C")
	require.Contains(t, out, "cpu load   0.42")
	require.Contains(t, out, "memory     3.5 GB used (no total reported)")
	require.Contains(t, out, "disk       / 10 / 40 GB (25%)")
	require.Contains(t, out, "uptime     2h")
	require.Contains(t, out, "gpu        Orin 25% memory")
	require.Contains(t, out, "sd card    12% worn")
	require.Contains(t, out, "alerts     none firing")
	require.Contains(t, out, " ago", "each reading carries its age")

	out, code = edgeRun(t, "edge", "health", "bare")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "this node reports no health readings")

	out, code = edgeRun(t, "edge", "health", "ghost")
	require.Equal(t, 1, code)
	require.Contains(t, out, `no such node on this Edge: "ghost"`)
}
