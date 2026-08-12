package reputation

// evaluate_test.go pins the thresholds and, more importantly, the shape of the judgement: a
// rate flags, evidence of not-working quarantines, and too little data does nothing.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTooLittleEvidenceDoesNothing(t *testing.T) {
	p := DefaultPolicy()
	// Five settled, all uncorroborated - a 100% rate, and still below the minimum sample.
	// The spec's whole point: one closed laptop, or five, is not a finding.
	tower := Tally{Corroborated: 0, Uncorroborated: 5}
	require.Equal(t, Clean, p.Evaluate(tower, Tally{}))
}

// An unusual uncorroborated rate is a FINDING, not a punishment: Investigate, and no
// settlement is touched (this function returns a verdict, it does not act).
func TestAnUnusualUncorroboratedRateIsFlaggedForInvestigation(t *testing.T) {
	p := DefaultPolicy()
	// 25 settled, 20 uncorroborated: 0.8, against a fleet baseline near 0.1.
	tower := Tally{Corroborated: 5, Uncorroborated: 20}
	fleet := Tally{Corroborated: 900, Uncorroborated: 100}
	require.Equal(t, Investigate, p.Evaluate(tower, fleet))
}

// RELATIVE to the fleet, not absolute: a network where most clients never ack has a high
// baseline that is nobody's fault, and a Tower near that baseline is clean.
func TestATowerNearAHighFleetBaselineIsClean(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{Corroborated: 5, Uncorroborated: 20}    // 0.8
	fleet := Tally{Corroborated: 250, Uncorroborated: 750} // 0.75 baseline
	require.Equal(t, Clean, p.Evaluate(tower, fleet))
}

// Repeated canary failures QUARANTINE: a Tower that does not carry work is not a rate
// question, it is off the network.
func TestRepeatedCanaryFailuresQuarantine(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{CanaryPass: 1, CanaryFail: 9} // 0.9 fail over 10 canaries
	require.Equal(t, Quarantine, p.Evaluate(tower, Tally{}))
}

// One or two canary failures is not enough to be sure - noise, a restart, a blip.
func TestAFewCanaryFailuresAreNotEnoughToQuarantine(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{CanaryPass: 2, CanaryFail: 2} // below MinCanaries
	require.Equal(t, Clean, p.Evaluate(tower, Tally{}))
}

// A single audit mismatch quarantines: a transcript that does not match what both ends
// signed is not a rate, it is a Tower or Station producing evidence that does not hold up.
func TestASingleAuditMismatchQuarantines(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{Corroborated: 100, AuditMismatch: 1}
	require.Equal(t, Quarantine, p.Evaluate(tower, Tally{}),
		"otherwise-normal settlement does not buy back a mismatched transcript")
}

// Quarantine evidence wins over a clean rate AND over an unknown one - it is checked first
// and independently.
func TestQuarantineEvidenceIsIndependentOfTheRate(t *testing.T) {
	p := DefaultPolicy()
	// Plenty of good settlement, but the canaries say it is not carrying work.
	tower := Tally{Corroborated: 500, Uncorroborated: 10, CanaryPass: 1, CanaryFail: 9}
	require.Equal(t, Quarantine, p.Evaluate(tower, Tally{}))
}

// A rate with no fleet to compare against falls back to an absolute margin from zero rather
// than dividing by nothing.
func TestWithNoFleetBaselineTheMarginIsFromZero(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{Corroborated: 5, Uncorroborated: 20} // 0.8 > 0.30 margin
	require.Equal(t, Investigate, p.Evaluate(tower, Tally{}))
}

func TestAnEmptyTallyHasNoKnownRates(t *testing.T) {
	_, known := Tally{}.UncorroboratedRate()
	require.False(t, known, "no settled attempts is no evidence, not a clean bill")
	_, known = Tally{}.CanaryFailRate()
	require.False(t, known)
}

// Without is the "rest of the fleet" baseline: a Tower removed from the fleet tally, exactly.
func TestWithoutSubtractsComponentwise(t *testing.T) {
	fleet := Tally{Total: 100, Corroborated: 60, Uncorroborated: 30, Disputed: 5,
		CanaryPass: 3, CanaryFail: 1, AuditMismatch: 1}
	one := Tally{Total: 10, Corroborated: 4, Uncorroborated: 3, Disputed: 1,
		CanaryPass: 1, CanaryFail: 1, AuditMismatch: 0}
	rest := fleet.Without(one)
	require.Equal(t, 90, rest.Total)
	require.Equal(t, 56, rest.Corroborated)
	require.Equal(t, 27, rest.Uncorroborated)
	require.Equal(t, 4, rest.Disputed)
	require.Equal(t, 2, rest.CanaryPass)
	require.Equal(t, 0, rest.CanaryFail)
	require.Equal(t, 1, rest.AuditMismatch)
}

// A lone Tower removed from the fleet leaves an empty baseline - which is "unknown", so the
// evaluation falls back to the absolute margin rather than comparing it to itself.
func TestALoneTowerIsJudgedAgainstAnEmptyBaseline(t *testing.T) {
	fleet := Tally{Total: 25, Corroborated: 5, Uncorroborated: 20}
	rest := fleet.Without(fleet)
	_, known := rest.UncorroboratedRate()
	require.False(t, known, "the rest of a one-Tower fleet is nobody")
}
