package main

// features/edge/node_identity.feature: no id is ever a name. The rules pane, the sinks line and the
// firing list name a unit the way the rack does - this machine by edge.SelfLabel, every other unit
// by edge.UnitName - never by its id or a cut of it (a live render of `roger edge fleet --health`
// printed "read by n_ebab8e24..." and "firing on n_ebab8e24").

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebanri/limoni/uitest"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/fleetview"
	"rogerai.fm/roger/v7/internal/store"
)

const unnamedID = "n_ebab8e241f058e18234af760ad2a1ca976647af7583e0029"

func TestTheRulesPaneNamesUnitsNeverByID(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	// A board nobody named: its record name IS its id. It is dark (the offline default fires on it),
	// it reads the trouble feed (so it is a sink), and an owner rule is scoped to it.
	edgeSeed(t, store.EdgeNode{ID: unnamedID, Name: unnamedID, Kind: "board", Presence: string(edge.PresenceDark),
		LastSeen: now.Add(-2 * time.Hour).Unix()})
	require.NoError(t, edgeAlertLog().RecordRead(unnamedID, now.Add(-time.Hour)))
	out, code := edgeRun(t, "edge", "alert", "set", "disk", "gt", "30", "--scope", unnamedID)
	require.Equal(t, 0, code, out)

	snap, err := edgeHealthSource()
	require.NoError(t, err)
	require.Contains(t, snap.Sinks, "board", "the sink is named by what it is")
	require.NotContains(t, snap.Sinks, "n_", "never by its id: %s", snap.Sinks)
	scoped, firing := false, false
	for _, r := range snap.Rules {
		require.NotContains(t, r.Scope, "n_", "rule %s scope %q", r.ID, r.Scope)
		require.NotContains(t, r.State, "n_", "rule %s state %q", r.ID, r.State)
		scoped = scoped || r.Scope == "board"
		firing = firing || strings.Contains(r.State, "board")
	}
	require.True(t, scoped, "the rule scoped to it names it: %+v", snap.Rules)
	require.True(t, firing, "the rule firing on it names it: %+v", snap.Rules)
	require.Equal(t, "board", snap.Names[unnamedID], "the screen gets the unit's name to draw")

	// What the owner sees: the health screen and its rules pane, rendered.
	page := uitest.Run(t, 160, 40, fleetview.NewHealth(edgeHealthSource).Draw)
	s := page.Screen()
	require.NotContains(t, s, "n_eb", "no id on the screen:\n%s", s)
	require.Contains(t, s, "board")

	// And the CLI's own list, which shares the same columns.
	out, code = edgeRun(t, "edge", "alert")
	require.Equal(t, 0, code, out)
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "sinks") || strings.Contains(ln, "firing") {
			require.NotContains(t, ln, "n_eb", "a sentence names a unit, never its id: %q", ln)
		}
	}
}

// The labels are the rack's own: this machine by its owner's name or else its station's, the rest
// by edge.UnitName - so a screen and the rack never call one unit two things.
func TestEdgeUnitLabelsAreTheRacksNames(t *testing.T) {
	nodes := []store.EdgeNode{
		{ID: unnamedID, Name: unnamedID, Kind: "host"},
		{ID: "n_0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6071", Name: "n_0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6071", Kind: "board"},
		{ID: "n_nas", Name: "nas", Kind: "host"},
	}
	got := edgeUnitLabelsFor(nodes, unnamedID, "gm-93")
	require.Equal(t, "gm-93", got[unnamedID], "this machine, unnamed, is its station")
	require.Equal(t, "board", got["n_0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6071"])
	require.Equal(t, "nas", got["n_nas"])
	for id, name := range got {
		require.NotContains(t, name, "n_", "%s is called %q", id, name)
	}
}
