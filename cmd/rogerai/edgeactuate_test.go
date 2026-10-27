package main

// THE ACTUATE CEREMONY (features/edge/node_identity.feature). Confirming actuate is the one action
// on this Edge that licenses a machine to CHANGE THE PHYSICAL WORLD, and until 2026-09-29 no
// surface could perform it: Fleet.ConfirmActuate existed with its audit trail and had no caller, so
// a node declaring actuate stayed pending for ever.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func actuateNode(id, name string, state edge.CapState) store.EdgeNode {
	return store.EdgeNode{ID: id, Name: name, Kind: "board",
		Presence: string(edge.PresenceVerified), LastSeen: edgeNodeFor(id, name, "board").LastSeen,
		Caps: []store.EdgeCap{{Name: string(edge.Actuate), State: string(state)}}}
}

func capOf(t *testing.T, id string) store.EdgeCap {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get(id)
	require.NoError(t, err)
	require.True(t, ok)
	for _, c := range n.Caps {
		if c.Name == string(edge.Actuate) {
			return c
		}
	}
	return store.EdgeCap{}
}

func TestTheOwnerConfirmsActuateFromTheTerminal(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, actuateNode("n_door", "shed-door", edge.Claimed))

	// It says what confirming MEANS and asks first. Answering no changes nothing.
	edgeStdin = strings.NewReader("n\n")
	out, code := edgeRun(t, "edge", "actuate", "shed-door")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "PHYSICAL WORLD", "the ceremony must say what it licenses")
	require.Contains(t, out, "[y/N]")
	require.Equal(t, string(edge.Claimed), capOf(t, "n_door").State, "no still confirmed it")

	// Yes confirms it, and records WHO and WHEN.
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "actuate", "shed-door")
	require.Equal(t, 0, code, out)
	got := capOf(t, "n_door")
	require.Equal(t, string(edge.Verified), got.State)
	require.NotEmpty(t, got.ConfirmedBy, "a confirmation records who gave it")
	require.NotZero(t, got.ConfirmedAt, "a confirmation records when")

	// And the node's own history carries it, because that is the audit trail a physical action has.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, _, _ := st.fleet.Get("n_door")
	found := false
	for _, e := range n.History {
		found = found || e.What == "actuate-confirmed"
	}
	require.True(t, found, "the confirmation is not in the node's history: %+v", n.History)
}

func TestConfirmingIsRefusedWhenThereIsNothingToConfirm(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_nas", "nas", "host"))
	edgeStdin = strings.NewReader("y\n")
	out, code := edgeRun(t, "edge", "actuate", "nas")
	require.NotEqual(t, 0, code, out)
	require.Contains(t, out, "does not declare actuate")
}

func TestConfirmingTwiceSaysSoRatherThanReRecording(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, actuateNode("n_door", "shed-door", edge.Claimed))
	edgeStdin = strings.NewReader("y\n")
	_, code := edgeRun(t, "edge", "actuate", "shed-door")
	require.Equal(t, 0, code)
	first := capOf(t, "n_door")

	edgeStdin = strings.NewReader("y\n")
	out, code := edgeRun(t, "edge", "actuate", "shed-door")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "already confirmed")
	after := capOf(t, "n_door")
	require.Equal(t, first.ConfirmedBy, after.ConfirmedBy, "the original confirmation was overwritten")
	require.Equal(t, first.ConfirmedAt, after.ConfirmedAt, "the original timestamp was overwritten")
}

// --yes is for a script that has already decided, and it still records who.
func TestTheCeremonyCanBeAnsweredUpFront(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, actuateNode("n_door", "shed-door", edge.Claimed))
	out, code := edgeRun(t, "edge", "actuate", "shed-door", "--yes")
	require.Equal(t, 0, code, out)
	require.Equal(t, string(edge.Verified), capOf(t, "n_door").State)
	require.NotEmpty(t, capOf(t, "n_door").ConfirmedBy)
}
