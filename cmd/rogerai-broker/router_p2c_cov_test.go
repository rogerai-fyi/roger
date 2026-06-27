package main

import (
	"math/rand"
	"testing"
)

// TestSelectP2CLoadTiebreak locks the power-of-two-choices live-load tie-break: with two
// equally-scored band members the lower live-load one is always chosen (it dodges a
// same-burst stampede the EWMA has not caught yet).
func TestSelectP2CLoadTiebreak(t *testing.T) {
	cands := []scoredCand{
		{idx: 10, score: 0.9, load: 0.8}, // higher load
		{idx: 20, score: 0.9, load: 0.1}, // lower load -> should win
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		if got := selectP2C(cands, 2.0, rng); got != 20 {
			t.Fatalf("iter %d: selectP2C picked idx %d, want 20 (lower live load)", i, got)
		}
	}
}

// TestSelectP2CEqualLoadEqualScore locks the final determinism rung: when both sampled
// members tie on load AND score, the lower idx wins.
func TestSelectP2CEqualLoadEqualScore(t *testing.T) {
	cands := []scoredCand{
		{idx: 5, score: 0.5, load: 0.3},
		{idx: 2, score: 0.5, load: 0.3},
	}
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 50; i++ {
		if got := selectP2C(cands, 1.0, rng); got != 2 {
			t.Fatalf("iter %d: equal load+score picked %d, want 2 (lower idx)", i, got)
		}
	}
}

// TestSelectP2CBandClampedToMax locks the adaptive-band upper clamp: with many
// near-equal-score candidates the band is capped at bandMax members, and the routed
// index is always one of the strongest (clamped) members.
func TestSelectP2CBandClampedToMax(t *testing.T) {
	cands := make([]scoredCand, 0, 12)
	for i := 0; i < 12; i++ {
		// All within bandRelDiff of the top so every one qualifies for the band before clamp.
		cands = append(cands, scoredCand{idx: i, score: 1.0 - float64(i)*0.005, load: float64(i) * 0.01})
	}
	rng := rand.New(rand.NewSource(11))
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		got := selectP2C(cands, 2.0, rng)
		if got < 0 || got >= 12 {
			t.Fatalf("selectP2C returned out-of-range idx %d", got)
		}
		seen[got] = true
	}
	// The clamp keeps the strongest bandMax (=8) members, so the two weakest (idx 8..11,
	// definitely the very weakest idx 11) must never be routed to.
	if seen[11] {
		t.Errorf("the weakest candidate (idx 11) must be clamped out of the band, but was picked")
	}
}

// TestSelectP2CEmpty locks the degenerate guard: an empty candidate set returns -1.
func TestSelectP2CEmpty(t *testing.T) {
	if got := selectP2C(nil, 2.0, rand.New(rand.NewSource(1))); got != -1 {
		t.Fatalf("selectP2C(empty) = %d, want -1", got)
	}
}

// TestSelectP2CValueGapFill locks the value-gap-adaptive fill: when only ONE candidate is
// within bandRelDiff of the best, the band is widened to bandMin by taking the strongest
// overall, then P2C routes to the lower-load of the two.
func TestSelectP2CValueGapFill(t *testing.T) {
	cands := []scoredCand{
		{idx: 0, score: 1.00, load: 0.9}, // clear best, but high load
		{idx: 1, score: 0.50, load: 0.1}, // pulled into the band by the bandMin fill, low load
		{idx: 2, score: 0.40, load: 0.2}, // weakest, excluded by the fill
	}
	rng := rand.New(rand.NewSource(5))
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		got := selectP2C(cands, 2.0, rng)
		if got != 0 && got != 1 {
			t.Fatalf("fill routed to idx %d, want 0 or 1 (the two strongest)", got)
		}
		seen[got] = true
	}
	if seen[2] {
		t.Error("the weakest candidate must be excluded by the bandMin fill")
	}
}

// TestSelectP2CBandSorts locks the band best-first sort: candidates supplied in
// ascending-score order are reordered so the clamp/P2C keep the strongest members; the
// lower-load of the two band members is routed to.
func TestSelectP2CBandSorts(t *testing.T) {
	cands := []scoredCand{
		{idx: 0, score: 0.90, load: 0.8}, // within band of the top, higher load
		{idx: 1, score: 1.00, load: 0.1}, // the best, lower load -> should win
	}
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 50; i++ {
		if got := selectP2C(cands, 2.0, rng); got != 1 {
			t.Fatalf("iter %d: ascending-input band picked %d, want 1 (best+lower load)", i, got)
		}
	}
}
