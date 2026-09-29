package edge_test

// THE TOPOLOGY HAS A DEPTH. Row said only "Child", which can express two levels, so a relay chain
// three deep was drawn flat: the node reached through a relay that is itself reached through a relay
// looked exactly like one reached directly through the first. Depth is the fact a picture of the
// path needs, and Last is what lets a drawing close a branch off without looking ahead.
//
// Spec: features/edge/topology_view.feature (the hop is a thing you can see, never a thing you have
// to know).

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func relayedNode(id, name, via string) store.EdgeNode {
	n := store.EdgeNode{ID: id, Name: name}
	if via == "" {
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:1"}}
		return n
	}
	n.Transports = []store.EdgeTransport{{Kind: "relay", Addr: via}}
	return n
}

func TestEveryRowKnowsHowManyHopsAwayItIs(t *testing.T) {
	rows := edge.Arrange([]store.EdgeNode{
		relayedNode("n_a", "studio", ""),
		relayedNode("n_b", "wiltshire", ""),
		relayedNode("n_c", "nina", "wiltshire"),
		relayedNode("n_d", "shed", "nina"), // a THIRD level: reached through a relay's relay
	})
	depth := map[string]int{}
	for _, r := range rows {
		depth[r.Node.Name] = r.Depth
	}
	require.Equal(t, 1, depth["studio"], "a node the authority reaches itself is one hop away")
	require.Equal(t, 1, depth["wiltshire"])
	require.Equal(t, 2, depth["nina"], "through one relay is two hops")
	require.Equal(t, 3, depth["shed"], "through a relay's relay is three, and the old Child flag could not say so")

	// Depth agrees with the flag it replaces: anything past the first hop is a child.
	for _, r := range rows {
		require.Equal(t, r.Depth > 1, r.Child, "%s: Child and Depth disagree", r.Node.Name)
	}
}

func TestTheLastNodeAtItsLevelSaysSo(t *testing.T) {
	rows := edge.Arrange([]store.EdgeNode{
		relayedNode("n_b", "wiltshire", ""),
		relayedNode("n_c", "nina", "wiltshire"),
		relayedNode("n_d", "shed", "wiltshire"),
		relayedNode("n_a", "studio", ""),
	})
	last := map[string]bool{}
	for _, r := range rows {
		last[r.Node.Name] = r.Last
	}
	require.False(t, last["nina"], "the first of two children under one relay is not the last")
	require.True(t, last["shed"], "the second is")
	require.False(t, last["wiltshire"], "a root with another root after it is not the last")
	require.True(t, last["studio"], "the final root is")
}

// A relay chain that eats its own tail must still draw every member exactly once, with a depth that
// makes sense: the rule the arrangement already obeyed, now with the new fields on it.
func TestACycleStillDrawsEveryNodeOnceWithADepth(t *testing.T) {
	rows := edge.Arrange([]store.EdgeNode{
		relayedNode("n_a", "a", "b"),
		relayedNode("n_b", "b", "a"),
	})
	require.Len(t, rows, 2, "a cycle dropped a node")
	seen := map[string]bool{}
	for _, r := range rows {
		require.False(t, seen[r.Node.ID], "%s drawn twice", r.Node.Name)
		seen[r.Node.ID] = true
		require.GreaterOrEqual(t, r.Depth, 1, "every drawn node is at least one hop from the authority")
	}
}
