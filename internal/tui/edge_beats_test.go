package tui

// The Edge screen's LIVE half: the host's heartbeat channel.
//
// The screen animates on REAL events and nothing else (features/edge/topology_view.feature:
// "the animation is not decoration"). The host is what hears a node; this is the seam it
// hands those sightings through, and these are the rules that seam must keep:
//
//   - a heartbeat on the channel becomes exactly one node's pulse, and only for a node the
//     graph actually draws;
//   - the drain RE-ARMS, so the second heartbeat is not swallowed;
//   - with no host wired there is no drain at all, and a still graph stays still;
//   - a host that went away closes the channel, and that ENDS the drain rather than
//     spinning on a closed channel forever.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// batchLen runs a batched Cmd and reports how many commands it carries.
func batchLen(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	require.NotNil(t, cmd)
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		return len(msg)
	default:
		return 1
	}
}

// A heartbeat the host heard for ONE node pulses that node's edge and no other.
func TestEdgeHeartbeatChannelPulsesExactlyThatNode(t *testing.T) {
	beats := make(chan string, 4)
	m, f, _ := edgeFixture(t, Hooks{EdgeHeartbeats: beats})
	a := edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	b := edgeAddNode(t, f, "b-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.2"}}, edge.Sense)
	m.enterEdge()

	// The drain is armed at launch, so a heartbeat that arrives before the operator ever
	// opens the screen is not lost.
	require.GreaterOrEqual(t, batchLen(t, m.Init()), 4, "Init arms the heartbeat drain")

	beats <- a.ID
	msg := waitEdgeHeartbeat(beats)()
	require.Equal(t, edgeHeartbeatMsg{node: a.ID}, msg, "the drain calls EdgeHeartbeat for what it read")

	tm, cmd := m.Update(msg)
	mm := asModel(tm)
	require.Len(t, mm.edge.pulses, 1)
	require.Equal(t, a.ID, mm.edge.pulses[0].id)
	require.NotEqual(t, b.ID, mm.edge.pulses[0].id)
	require.Equal(t, 2, batchLen(t, cmd), "the animation frame AND the re-armed drain")
}

// A heartbeat for a node the graph does not draw still re-arms: the pump must survive a
// stranger, or the next real heartbeat never arrives.
func TestEdgeHeartbeatDrainReArmsEvenForANodeItDoesNotDraw(t *testing.T) {
	beats := make(chan string, 4)
	m, f, _ := edgeFixture(t, Hooks{EdgeHeartbeats: beats})
	edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	m.enterEdge()

	tm, cmd := m.Update(edgeHeartbeatMsg{node: "n_nobody"})
	require.Empty(t, asModel(tm).edge.pulses)
	require.NotNil(t, cmd, "the drain is re-armed even though nothing animated")
	beats <- "n_next"
	require.Equal(t, edgeHeartbeatMsg{node: "n_next"}, cmd(), "and the next heartbeat is read")
}

// With no host wired there is no drain, and the screen behaves exactly as it did before
// the seam existed: a still graph, and no command scheduled for a stranger.
func TestEdgeHeartbeatDrainIsAbsentWithoutAHost(t *testing.T) {
	require.Nil(t, waitEdgeHeartbeat(nil), "no host, no drain")
	m, f, _ := edgeFixture(t, Hooks{})
	edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	m.enterEdge()
	_, cmd := m.Update(edgeHeartbeatMsg{node: "n_nobody"})
	require.Nil(t, cmd)
}

// A host that has gone away closes the channel. That ends the drain: re-arming on a closed
// channel would spin a goroutine at full speed for the life of the program.
func TestEdgeHeartbeatDrainEndsWhenTheHostIsGone(t *testing.T) {
	beats := make(chan string)
	close(beats)
	require.Equal(t, edgeBeatsClosedMsg{}, waitEdgeHeartbeat(beats)())

	m, _, _ := edgeFixture(t, Hooks{EdgeHeartbeats: beats})
	m.enterEdge()
	_, cmd := m.Update(edgeBeatsClosedMsg{})
	require.Nil(t, cmd, "a closed channel is never re-read")
}
