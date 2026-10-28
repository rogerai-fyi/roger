package main

// A COMMAND MAY NOT OVERSTATE WHAT IT DID.
//
// `roger edge model` and `roger edge job` write a durable ASSIGNMENT that the fleet screens read.
// They do not start anything, do not reach the upstream map, and do not make this node advertise a
// band. The copy said "it is on air to the Edge: other authorized nodes may route to it", which was
// false twice over: a node advertises what it really has on air (edgeBandsOnAir, from the rows the
// controller has live), and the dispatch ladder only routes to a band the serving probe has PROVEN.
//
// features/edge/jobs.feature says the route side of share/use across the network is NOT BUILT. A
// command that claims otherwise is the same fault as a screen drawing a link it has not seen, and
// it is worse than a missing feature: the owner believes it worked and stops looking.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBindingAModelDoesNotClaimToPutItOnAir(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "edge", "model", "watcher", "qwen3-8b", "share")
	require.Equal(t, 0, code, out)

	// It says what it recorded.
	require.Contains(t, out, "recorded")
	require.Contains(t, out, "qwen3-8b")
	require.Contains(t, out, "watcher")

	// And it does NOT claim the model is on air or routable.
	low := strings.ToLower(out)
	require.NotContains(t, low, "it is on air to the edge")
	require.NotContains(t, low, "may route to it")

	// It names what WOULD put a model on air, so the owner is not left guessing.
	require.Contains(t, out, "roger share")
}

func TestAssigningTheServeJobSaysWhatIsStillMissing(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "edge", "job", "watcher", "serve")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "no model is assigned yet")
	require.Contains(t, out, "roger share")
	require.NotContains(t, strings.ToLower(out), "to put one on air.")
}

// The private posture is a real distinction and keeps its own wording.
func TestAPrivateAssignmentSaysItIsPrivate(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "edge", "model", "watcher", "qwen3-8b", "use")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "private to this agent")
	require.NotContains(t, strings.ToLower(out), "on air")
}
