package main

// towercanary_coverage_test.go is the spec for WHICH Station a Tower's canary probes, and WHO
// its verdict is recorded against.
//
// Both were wrong in the same way and for the same reason as the pre-M1 placement bug, which is
// what made this worth its own file: canaryTargetFor returned the first row of a query that
// sorts by station id, so behind each Tower exactly one Station - the lexicographically first -
// was probed on every sweep forever. Its health became the whole Tower's reputation, one bad
// machine could suspend a relay carrying twenty good ones, and the other nineteen operators
// never produced a single piece of edge-path health evidence.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

// canaryFleet stands up one live tower with n self-attached stations on air behind it, and
// returns the tower id.
func canaryFleet(t *testing.T, b *broker, srv *httptest.Server, n int) string {
	t.Helper()
	tw := liveEdgeTower(t, b, srv, "canary-tower-op", "203.0.113.9:8443")
	for i := 0; i < n; i++ {
		node := signedInOperator(t, b, fmt.Sprintf("canary-node-op-%d", i))
		body, _ := selfAttachBodyFor(t, b, node)
		var out struct {
			StationID string `json:"station_id"`
		}
		code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
		require.Equal(t, http.StatusOK, code, raw)
	}
	return tw.id
}

// THE CANARY MAGNET. Every Station behind a Tower must be probed, or the ones that are not are
// riding on somebody else's uptime and the ones that are carry the Tower's whole reputation.
//
// Each iteration records the probe exactly as RunCanary does, so the selection is scored against
// the same evidence it produces in production - a rotation that only spreads while nothing is
// recorded is not a rotation.
func TestTheCanaryRotatesThroughEveryStationBehindATower(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 4)

	probed := map[string]int{}
	for i := 0; i < 40; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok, "probe %d found no station to canary", i)
		probed[row.StationID]++
		b.recordEdgeCanary(row.StationID, reputation.CanaryPass)
	}

	require.Len(t, probed, 4,
		"40 probes reached %d of 4 stations: %v", len(probed), probed)
	for id, n := range probed {
		require.Less(t, n, 20, "station %s absorbed %d of 40 probes: %v", id, n, probed)
	}
}

// AND IT REACHES THE NEVER-PROBED ONE FIRST. Coverage is the point, so the score is staleness
// rather than quality: a Station nobody has looked at is the one a probe is worth spending on.
// Ranking by quality instead would probe the healthy Stations most and leave a sick one
// un-probed precisely because it is sick.
func TestTheCanaryPrefersTheStationItHasNeverProbed(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 3)

	// Probe two of the three, then check the third is the one that comes up next.
	first := map[string]bool{}
	for i := 0; i < 2; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok)
		first[row.StationID] = true
		b.recordEdgeCanary(row.StationID, reputation.CanaryPass)
	}
	require.Len(t, first, 2, "the first two probes went to the same station")

	_, row, ok := b.canaryTargetFor(towerID)
	require.True(t, ok)
	require.False(t, first[row.StationID],
		"the third probe went back to an already-probed station while one had never been probed")
}

// WHEN NOTHING BEHIND A TOWER IS PLACEABLE, THE CANARY PROBES EVERYTHING ANYWAY - and it still
// rotates.
//
// This is the fallback canaryTargetFor takes when edgeEligible returns both tiers empty, and it
// had no test at all. The reasoning for it is written down and is the right reasoning: a Tower
// whose machines have all gone quiet would otherwise stop being probed at exactly the moment it
// stopped working, and its reputation would freeze at whatever it last was - unable to degrade,
// and unable to recover when the machines came back. A Tower carrying nothing reachable IS the
// finding the probe exists to make.
//
// What was untested is that the fallback path still SPREADS. It builds its candidates from
// scratch (scoredCand{idx: i}, load zero, because there is no eligibility reading to carry over
// for a candidate eligibility rejected), so it is a second construction of the thing the rest of
// this file is about, and a magnet here would be as invisible as the magnet that was here
// before: the probes all succeed, nothing errors, and one Station's health silently becomes the
// whole Tower's.
func TestTheCanaryStillRotatesWhenNothingBehindATowerIsPlaceable(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 3)

	// Every machine goes home. edgeEligible drops a registered node whose heartbeat is older
	// than nodeTTL outright - not to Tier B - so both tiers come back empty and the fallback is
	// the only thing left.
	b.mu.Lock()
	for id := range b.nodes {
		b.lastSeen[id] = time.Now().Add(-2 * nodeTTL)
	}
	b.mu.Unlock()

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 3)
	tierA, tierB := b.edgeEligible(rows, nil, time.Now())
	require.Empty(t, tierA)
	require.Empty(t, tierB, "the premise of this test is gone: something is still eligible")
	_, _, placeable := b.edgeTargetFor("m", edgePlacementRand())
	require.False(t, placeable, "a consumer can still be placed, so this is not the fallback case")

	probed := map[string]int{}
	for i := 0; i < 30; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok,
			"probe %d found nothing to canary, so a Tower whose fleet went quiet stops being measured", i)
		probed[row.StationID]++
		b.recordEdgeCanary(row.StationID, reputation.CanaryFail)
	}
	require.Len(t, probed, 3, "30 probes reached %d of 3 unplaceable stations: %v", len(probed), probed)
	for id, n := range probed {
		require.Less(t, n, 15, "station %s absorbed %d of 30 probes: %v", id, n, probed)
	}
}

// THE EVIDENCE BELONGS TO THE STATION THAT PRODUCED IT. A failing machine must cost itself
// placement and must not cost the Stations beside it anything.
func TestAFailingCanaryDemotesOnlyItsOwnStation(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 2)

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	sick, healthy := rows[0].StationID, rows[1].StationID

	for i := 0; i < edgeCanaryFailBar; i++ {
		b.recordEdgeCanary(sick, reputation.CanaryFail)
	}
	b.recordEdgeCanary(healthy, reputation.CanaryPass)

	// Placement must now avoid the sick one entirely: it is in Tier B and Tier A is not empty.
	for i := 0; i < 60; i++ {
		_, row, ok := b.edgeTargetFor("m", edgePlacementRand())
		require.True(t, ok)
		require.Equal(t, healthy, row.StationID,
			"placement %d used %s, whose own edge probes are failing, while %s was healthy",
			i, row.StationID, healthy)
	}

	// A pass clears the streak - the demotion is a current reading, not a life sentence.
	b.recordEdgeCanary(sick, reputation.CanaryPass)
	back := false
	for i := 0; i < 200 && !back; i++ {
		_, row, ok := b.edgeTargetFor("m", edgePlacementRand())
		require.True(t, ok)
		back = row.StationID == sick
	}
	require.True(t, back, "a station that started passing again never got traffic back")
}

// AND THE DEMOTED STATION IS STILL PROBED. Being in Tier B is a reason to canary a Station, not
// a reason not to - a selector that stopped probing what it had demoted would keep the evidence
// that could clear the demotion from ever arriving.
func TestADemotedStationIsStillCanaried(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 2)

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	sick := rows[0].StationID
	for i := 0; i < edgeCanaryFailBar+2; i++ {
		b.recordEdgeCanary(sick, reputation.CanaryFail)
	}

	reached := false
	for i := 0; i < 40 && !reached; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok)
		reached = row.StationID == sick
		b.recordEdgeCanary(row.StationID, reputation.CanaryPass)
	}
	require.True(t, reached, "a demoted station was never probed again, so it could never recover")
}

// A canary's verdict must not touch the CLASSIC fabric's trust reading. That map is what pickFor
// drops on, what probeOnce skips on and what /discover prints - so folding a Tower's canary
// result into it would let a relay operator who black-holes traffic depress the paid-fabric
// score of every node behind them. It is the same one-way rule the load counters already follow.
func TestACanaryVerdictDoesNotTouchTheClassicTrustReading(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 1)
	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)

	before := b.trustOf(rows[0].NodeID)
	for i := 0; i < 5; i++ {
		b.recordEdgeCanary(rows[0].StationID, reputation.CanaryFail)
	}
	require.Equal(t, before, b.trustOf(rows[0].NodeID),
		"a tower canary moved the classic fabric's trust state for this node")
}

// trustOf reads a node's classic trust reading under the lock that owns it.
func (b *broker) trustOf(nodeID string) trustState {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.trust[nodeID]
}
