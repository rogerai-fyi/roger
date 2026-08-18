package main

import (
	"testing"

	"rogerai.fm/roger/v5/internal/towercore/fleet"
)

// M1 (docs/relay-selection-design.md): edge placement ranks candidates on what was measured
// instead of returning whichever row the projection handed back first.
//
// The scoring function is tested directly because that is where the policy lives; the
// selection loop around it only takes the maximum.
func TestEdgeCandidateScoreRanksOnMeasuredHealth(t *testing.T) {
	b := &broker{
		trust:    map[string]trustState{},
		inflight: map[string]int{},
	}

	// A node the broker has never heard of scores NEUTRAL, not zero. This is the property
	// that keeps a newly attached station reachable: scoring absent evidence as bad would
	// require it to win traffic to earn a score, and it cannot win traffic without one.
	unmeasured := b.edgeCandidateScore(fleet.Station{StationID: "s-new", NodeID: "n-new"})
	noJoin := b.edgeCandidateScore(fleet.Station{StationID: "s-old"}) // pre-join row
	if unmeasured <= 0 {
		t.Errorf("an unprobed node scored %v; absent evidence must not read as bad", unmeasured)
	}
	if noJoin != unmeasured {
		t.Errorf("a row with no join (%v) and an unprobed node (%v) are both simply unmeasured", noJoin, unmeasured)
	}

	// A probed, healthy node beats an unmeasured one.
	b.trust["n-good"] = trustState{probed: true, probeOK: true, probeCompleted: true}
	good := b.edgeCandidateScore(fleet.Station{StationID: "s-good", NodeID: "n-good"})
	if good <= unmeasured {
		t.Errorf("a proven node (%v) must outrank an unproven one (%v)", good, unmeasured)
	}

	// A failing canary is punished, and lands below neutral.
	b.trust["n-bad"] = trustState{probed: true, probeOK: false, probeFails: 2}
	bad := b.edgeCandidateScore(fleet.Station{StationID: "s-bad", NodeID: "n-bad"})
	if bad >= unmeasured {
		t.Errorf("a failing node (%v) must rank below an unmeasured one (%v)", bad, unmeasured)
	}
	if bad >= good {
		t.Errorf("a failing node (%v) must rank below a healthy one (%v)", bad, good)
	}
}

// The load divisor is what stops a strong station becoming a magnet: two equally healthy
// stations are not equally good choices if one is already busy.
func TestEdgeCandidateScoreSpreadsLoad(t *testing.T) {
	b := &broker{
		trust:    map[string]trustState{},
		inflight: map[string]int{},
	}
	healthy := trustState{probed: true, probeOK: true, probeCompleted: true}
	b.trust["n-idle"] = healthy
	b.trust["n-busy"] = healthy
	b.inflight["n-busy"] = 3

	idle := b.edgeCandidateScore(fleet.Station{StationID: "s-idle", NodeID: "n-idle"})
	busy := b.edgeCandidateScore(fleet.Station{StationID: "s-busy", NodeID: "n-busy"})
	if idle <= busy {
		t.Errorf("an idle station (%v) must beat an equally healthy busy one (%v)", idle, busy)
	}

	// And load is not absolute: a busy EXCELLENT node can still beat an idle failing one,
	// which is the difference between spreading load and ignoring quality.
	b.trust["n-idlebad"] = trustState{probed: true, probeOK: false, probeFails: 3}
	idleBad := b.edgeCandidateScore(fleet.Station{StationID: "s-idlebad", NodeID: "n-idlebad"})
	if busy <= idleBad {
		t.Errorf("a busy healthy node (%v) should still beat an idle failing one (%v)", busy, idleBad)
	}
}

// Determinism: the same candidate set must produce the same score every time. The old code
// could not promise this at all, because its input order came from a Go map.
func TestEdgeCandidateScoreIsDeterministic(t *testing.T) {
	b := &broker{trust: map[string]trustState{}, inflight: map[string]int{}}
	b.trust["n"] = trustState{probed: true, probeOK: true, probeCompleted: true, recounts: 10, discrepancies: 1}
	row := fleet.Station{StationID: "s", NodeID: "n"}
	first := b.edgeCandidateScore(row)
	for i := 0; i < 50; i++ {
		if got := b.edgeCandidateScore(row); got != first {
			t.Fatalf("score drifted: %v then %v", first, got)
		}
	}
}
