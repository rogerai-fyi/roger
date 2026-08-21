package main

// toweredge_placement_test.go is the spec for WHICH station an edge consumer is sent to when
// several could serve them.
//
// It exists because the scoring function had tests and the placement did not, and the two
// disagreed. edgeCandidateScore divides quality by in-flight load, and a test poked
// b.inflight by hand to prove the divisor worked - but nothing on the edge path ever WROTE
// that counter, so in production every candidate scored at load zero forever and the
// highest-scoring station took every request. A green test over a value the real path never
// sets is worse than no test: it reports a property the system does not have.
//
// So these drive the real path: attach real stations to a real tower, place through the real
// edgeTargetFor, and open each placement the way towerEdgeAuthorize opens it.

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE MAGNET. Equally good stations must not all receive the same answer.
//
// Two mechanisms have to be present for this to hold, and before this change neither was: the
// placement has to sample rather than always take the maximum (power-of-two-choices, the
// classic router's spec-1.5 answer to exactly this), and work already placed has to count
// against the next decision (the load divisor, which needed something to divide).
func TestEdgePlacementSpreadsAcrossEquallyGoodStations(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	const stations = 4
	for i := 0; i < stations; i++ {
		node := signedInOperator(t, b, fmt.Sprintf("node-op-%d", i))
		body, _ := selfAttachBodyFor(t, b, node)
		var out struct {
			StationID string `json:"station_id"`
		}
		code, raw := node.attach(t, srv, body, &out)
		require.Equal(t, http.StatusOK, code, raw)
		require.NotEmpty(t, out.StationID)
	}

	// 200 placements, each one opened exactly as towerEdgeAuthorize opens it: the station is
	// now carrying work, and the next decision has to see that.
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		_, row, ok := b.edgeTargetFor("m", edgePlacementRand())
		require.True(t, ok, "placement %d found no station", i)
		counts[row.StationID]++
		b.edgeEnterInflight(fmt.Sprintf("at-%d", i), row.NodeID, "u_test", time.Now().Add(time.Hour))
	}

	require.Len(t, counts, stations,
		"every station must see traffic; 200 placements landed on %d of %d: %v", len(counts), stations, counts)
	// Not a strict fairness bound - P2C is a sampler, not a round-robin - but no station may
	// be a magnet. A quarter of the traffic is even; half of it is a magnet.
	for id, n := range counts {
		require.Less(t, n, 100, "station %s absorbed %d of 200 placements: %v", id, n, counts)
	}
}

// The half of the fix that is not randomness: edge work must count as load. With the sampler
// switched off (a nil rng is the documented deterministic mode), the ONLY thing that can move
// placement off the top-scoring station is the divisor - so this leg fails outright if the
// edge path is not accounting for the work it hands out.
func TestEdgeDispatchCountsItsOwnInFlightWork(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	for i := 0; i < 3; i++ {
		node := signedInOperator(t, b, fmt.Sprintf("node-op-%d", i))
		body, _ := selfAttachBodyFor(t, b, node)
		var out map[string]any
		code, raw := node.attach(t, srv, body, &out)
		require.Equal(t, http.StatusOK, code, raw)
	}

	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		_, row, ok := b.edgeTargetFor("m", nil) // deterministic: no sampling to hide behind
		require.True(t, ok)
		seen[row.StationID]++
		b.edgeEnterInflight(fmt.Sprintf("at-%d", i), row.NodeID, "u_test", time.Now().Add(time.Hour))

		require.Positive(t, b.nodeEdgeLoad(row.NodeID),
			"placing work on %s left its edge load at zero", row.NodeID)
		require.Zero(t, b.nodeInFlight(row.NodeID),
			"an edge placement moved %s's RELAYED in-flight count, which the paid router divides by", row.NodeID)
	}
	require.Len(t, seen, 3, "deterministic placement still piled onto one station: %v", seen)
}

// An edge attempt that never comes home must not pin a station's load up forever. There is no
// dispatch loop to unwind here - a consumer that never connects looks exactly like one still
// thinking - so the entry carries its own expiry, set to the attempt's finalization ceiling.
func TestEdgeInFlightExpiresWithTheAttempt(t *testing.T) {
	b := &broker{inflight: map[string]int{}, edgeInflight: map[string]edgeAttemptLoad{}, edgeLoad: map[string]int{}, edgeOpenByAccount: map[string]int{}}

	b.edgeEnterInflight("at-settles", "n-1", "u_a", time.Now().Add(time.Hour))
	b.edgeEnterInflight("at-abandoned", "n-1", "u_a", time.Now().Add(50*time.Millisecond))
	require.Equal(t, 2, b.nodeEdgeLoad("n-1"))

	// The receipt path closes its own attempt, and only its own.
	b.edgeExitInflight("at-settles")
	require.Equal(t, 1, b.nodeEdgeLoad("n-1"))
	b.edgeExitInflight("at-settles") // idempotent: the timer and the settle may both fire
	require.Equal(t, 1, b.nodeEdgeLoad("n-1"))

	require.Eventually(t, func() bool { return b.nodeEdgeLoad("n-1") == 0 }, 5*time.Second, 10*time.Millisecond,
		"an abandoned attempt held the station's load up past its own settlement window")

	// A settlement for an attempt this instance never opened is a no-op, not a decrement of
	// somebody else's count - the ordinary multi-instance case, where authorize and settle
	// land on different brokers.
	b.edgeEnterInflight("at-mine", "n-2", "u_a", time.Now().Add(time.Hour))
	b.edgeExitInflight("at-somebody-elses")
	require.Equal(t, 1, b.nodeEdgeLoad("n-2"))
}

// nodeInFlight reads a node's live RELAYED in-flight count under the lock that owns it - the
// number the classic paid router divides by.
func (b *broker) nodeInFlight(nodeID string) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.inflight[nodeID]
}

// nodeEdgeLoad reads a node's open EDGE attempt count. Separate from nodeInFlight on purpose:
// keeping the two apart is the fix, so a test that means one must not be able to read the other
// by accident.
func (b *broker) nodeEdgeLoad(nodeID string) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.edgeLoad[nodeID]
}

// totalInFlight sums every node's live RELAYED in-flight count - what a test asks when it
// wants to know whether the broker thinks anybody is busy on the classic paid fabric.
func (b *broker) totalInFlight() int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	n := 0
	for _, c := range b.inflight {
		n += c
	}
	return n
}

// totalEdgeLoad sums every node's open EDGE attempts - the counter edge placement divides by.
func (b *broker) totalEdgeLoad() int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	n := 0
	for _, c := range b.edgeLoad {
		n += c
	}
	return n
}
