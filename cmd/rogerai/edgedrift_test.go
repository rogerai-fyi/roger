package main

// Executable pins for features/edge/drift.feature, the CLI half: `roger edge expect`, `roger edge
// drift`, and that an unknown is never shown as a wrong.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func driftSeed(t *testing.T) {
	t.Helper()
	offline(t)
	verified := []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}}
	edgeSeed(t,
		store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified),
			Tags: []string{"production"},
			Caps: []store.EdgeCap{{Name: string(edge.Maintain), State: string(edge.Claimed)}},
			Instances: []edge.Instance{{Name: "desk", Agent: true, Bands: []string{"qwen"},
				ServeProven: []string{"qwen"}, Caps: verified,
				Software: &store.EdgeSoftware{Roger: "6.13.0"}}}},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified),
			Tags:      []string{"production"},
			Instances: []edge.Instance{{Name: "shed", Software: &store.EdgeSoftware{Roger: "6.9.0"}}}},
		store.EdgeNode{ID: "n_unseen", Name: "unseen", Kind: "host", Presence: string(edge.PresenceVerified)},
	)
}

func TestEdgeExpectAndDrift(t *testing.T) {
	driftSeed(t)

	// Nothing expected yet: both commands say so rather than claiming order.
	out, code := edgeRun(t, "edge", "expect")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing is expected of this Edge yet")
	out, code = edgeRun(t, "edge", "drift")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing to be in drift from")

	// Say what it should look like.
	out, code = edgeRun(t, "edge", "expect", "roger", "6.13.0")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "expected of every node: roger 6.13.0")
	out, code = edgeRun(t, "edge", "expect", "serves", "qwen", "--scope", "tag:production")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "every node tagged production")
	out, code = edgeRun(t, "edge", "expect", "agent", "--scope", "desk")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "expected of desk: agent")

	// The list says what is expected, of whom, and how many differ.
	out, code = edgeRun(t, "edge", "expect")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "EXPECTED of this Edge")
	require.Contains(t, out, "roger 6.13.0")
	require.Contains(t, out, "tag production")
	require.Contains(t, out, "differ")

	// The drift report names the unit, what was expected, and what is there.
	out, code = edgeRun(t, "edge", "drift")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "DRIFT")
	require.Contains(t, out, "shed")
	require.Contains(t, out, "runs 6.9.0")
	require.Contains(t, out, "does not serve it")
	require.NotContains(t, out, "desk  ", "a unit in order is not in the report")

	// An unknown is marked as such, never as a wrong.
	require.Contains(t, out, "unknown")
	require.Contains(t, out, "has not reported")

	// JSON carries the same records.
	out, code = edgeRun(t, "edge", "drift", "--json")
	require.Equal(t, 0, code, out)
	var got struct {
		Expected edge.ExpectSet `json:"expected"`
		Drift    []edge.Drift   `json:"drift"`
		Checked  int            `json:"checked"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Expected, 3)
	require.Equal(t, 3, got.Checked)
	states := map[edge.DriftState]int{}
	for _, d := range got.Drift {
		states[d.State]++
	}
	require.Greater(t, states[edge.DriftOut], 0)
	require.Greater(t, states[edge.DriftUnknown], 0)

	// Dropping one stops it being judged.
	out, code = edgeRun(t, "edge", "expect", "rm", "roger")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "no longer expected")
	out, _ = edgeRun(t, "edge", "drift")
	require.NotContains(t, out, "runs 6.9.0")
	out, code = edgeRun(t, "edge", "expect", "rm", "roger")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing was expected", "removing what is not there is not an error")
}

func TestEdgeExpectRefusals(t *testing.T) {
	driftSeed(t)
	for _, args := range [][]string{
		{"edge", "expect", "colour", "red"},
		{"edge", "expect", "roger"},
		{"edge", "expect", "roger", "soon"},
		{"edge", "expect", "serves"},
		{"edge", "expect", "agent", "yes"},
		{"edge", "expect", "agent", "--scope", "tag:a b"},
		{"edge", "expect", "agent", "--scope", "nosuchnode"},
		{"edge", "expect", "rm"},
	} {
		out, code := edgeRun(t, args...)
		require.NotEqual(t, 0, code, "%v must be refused: %s", args, out)
	}
	// And the refusal for an unknown thing names the whole vocabulary.
	out, _ := edgeRun(t, "edge", "expect", "colour", "red")
	for _, w := range edge.ExpectWhats() {
		require.Contains(t, out, string(w))
	}
}

func TestEdgeDriftSaysWhenTheFleetIsAsAsked(t *testing.T) {
	driftSeed(t)
	_, code := edgeRun(t, "edge", "expect", "maintenance", "--scope", "desk")
	require.Equal(t, 0, code)
	out, code := edgeRun(t, "edge", "drift")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "this Edge is as you asked")
	require.Contains(t, out, "1 expectation(s)")
	require.NotContains(t, out, "DRIFT")
}

// The screen's DRIFT row carries only real drift, never an unknown.
func TestDriftLinesAreOnlyWhatIsKnownWrong(t *testing.T) {
	drift := []edge.Drift{
		{NodeID: "n_shed", Node: "shed", What: edge.ExpectRoger, Expected: "roger 6.13.0", Found: "runs 6.9.0", State: edge.DriftOut},
		{NodeID: "n_shed", Node: "shed", What: edge.ExpectAgent, Expected: "agent", Found: "runs no agent", State: edge.DriftOut},
		{NodeID: "n_unseen", Node: "unseen", What: edge.ExpectRoger, Expected: "roger 6.13.0", Found: "it has not reported which roger it runs", State: edge.DriftUnknown},
	}
	lines := edgeDriftLines(drift, "n_shed")
	require.Equal(t, []string{"roger 6.13.0 · runs 6.9.0", "agent · runs no agent"}, lines)
	require.Empty(t, edgeDriftLines(drift, "n_unseen"), "an unknown is not shown as a wrong")
	require.Empty(t, edgeDriftLines(drift, "n_desk"))
	require.Empty(t, edgeDriftLines(nil, "n_shed"))
	_ = time.Now
}
