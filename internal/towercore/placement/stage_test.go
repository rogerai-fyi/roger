package placement

// Contract: features/tower/tower_trust_ramp.feature ("computing the stage", "excusals and
// admin actions").

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func TestNewToProving(t *testing.T) {
	e := Evidence{Now: t0.Add(7 * day), Current: StageNew, EnteredAt: t0, Passed: 50, Failed: 2}
	got, _ := NextStage(e)
	require.Equal(t, StageProving, got)
}

func TestMissingAnyPromotionConditionStays(t *testing.T) {
	base := Evidence{Now: t0.Add(7 * day), Current: StageNew, EnteredAt: t0, Passed: 50, Failed: 2}
	for name, e := range map[string]Evidence{
		"6d23h":        func() Evidence { x := base; x.Now = t0.Add(7*day - time.Hour); return x }(),
		"49 passed":    func() Evidence { x := base; x.Passed = 49; x.Failed = 0; return x }(),
		"94%":          func() Evidence { x := base; x.Passed = 94; x.Failed = 6; return x }(),
		"disqualified": func() Evidence { x := base; x.Disqualified = true; return x }(),
	} {
		got, _ := NextStage(e)
		require.Equal(t, StageNew, got, name)
	}
}

func TestProvingToEstablished(t *testing.T) {
	e := Evidence{Now: t0.Add(30 * day), Current: StageProving, EnteredAt: t0, Passed: 985, Failed: 15}
	got, _ := NextStage(e)
	require.Equal(t, StageEstablished, got)
	e.Passed, e.Failed = 979, 21 // 97.9%
	got, _ = NextStage(e)
	require.Equal(t, StageProving, got)
	e.Passed, e.Failed, e.Now = 985, 15, t0.Add(30*day-time.Minute)
	got, _ = NextStage(e)
	require.Equal(t, StageProving, got)
}

func TestAStageRisesAtMostOneStep(t *testing.T) {
	e := Evidence{Now: t0.Add(90 * day), Current: StageNew, EnteredAt: t0, Passed: 10000, Failed: 0}
	got, _ := NextStage(e)
	require.Equal(t, StageProving, got)
}

func TestADisqualifierDemotesToNew(t *testing.T) {
	for _, cur := range []Stage{StageProving, StageEstablished, StageNew} {
		got, _ := NextStage(Evidence{Now: t0.Add(100 * day), Current: cur, EnteredAt: t0, Passed: 1e4, Disqualified: true})
		require.Equal(t, StageNew, got, cur)
	}
}

func TestEstablishedStaysEstablished(t *testing.T) {
	got, need := NextStage(Evidence{Now: t0.Add(100 * day), Current: StageEstablished, EnteredAt: t0, Passed: 1, Failed: 99})
	require.Equal(t, StageEstablished, got, "the pass rate gates promotion; failures demote through disqualifiers")
	require.Empty(t, need)
}

func TestACeilingHoldsButNeverRaises(t *testing.T) {
	e := Evidence{Now: t0.Add(7 * day), Current: StageNew, EnteredAt: t0, Passed: 50, Ceiling: StageNew}
	got, _ := NextStage(e)
	require.Equal(t, StageNew, got)
	e = Evidence{Now: t0.Add(100 * day), Current: StageEstablished, EnteredAt: t0, Passed: 1e4, Ceiling: StageProving}
	got, _ = NextStage(e)
	require.Equal(t, StageProving, got)
	e = Evidence{Now: t0.Add(1 * day), Current: StageNew, EnteredAt: t0, Ceiling: StageEstablished}
	got, _ = NextStage(e)
	require.Equal(t, StageNew, got, "a ceiling above the evidence raises nothing")
}

func TestWhatTheNextStageStillNeeds(t *testing.T) {
	_, need := NextStage(Evidence{Now: t0.Add(3 * day), Current: StageNew, EnteredAt: t0, Passed: 20, Failed: 2})
	require.Equal(t, "4 more days, 30 more passed health checks", need)
	_, need = NextStage(Evidence{Now: t0.Add(8 * day), Current: StageNew, EnteredAt: t0, Passed: 60, Failed: 10})
	require.Equal(t, "a pass rate of 95% or better (now 85.7%)", need)
	_, need = NextStage(Evidence{Now: t0.Add(10 * day), Current: StageProving, EnteredAt: t0, Passed: 100})
	require.Equal(t, "20 more days", need)
	_, need = NextStage(Evidence{Now: t0.Add(1 * day), Current: StageNew, EnteredAt: t0, Disqualified: true})
	require.Equal(t, "7 more days, 50 more passed health checks", need)
	_, need = NextStage(Evidence{Now: t0.Add(1 * day), Current: StageNew, EnteredAt: t0.Add(day), Passed: 0, Failed: 0, Ceiling: StageNew})
	require.Equal(t, "an administrator has held this Tower at new", need)
}

func TestStageOrder(t *testing.T) {
	require.Equal(t, StageNew, MinStage(StageNew, StageEstablished))
	require.Equal(t, StageProving, MinStage(StageEstablished, StageProving))
	require.Equal(t, StageEstablished, MinStage(StageEstablished, ""))
	require.True(t, ValidStage(StageProving))
	require.False(t, ValidStage("gold"))
}
