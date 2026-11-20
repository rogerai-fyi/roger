package placement

import (
	"fmt"
	"strings"
	"time"
)

// Stage is a community Tower's trust stage (tower_trust_ramp.feature).
type Stage string

const (
	StageNew         Stage = "new"
	StageProving     Stage = "proving"
	StageEstablished Stage = "established"
)

var stageRank = map[Stage]int{StageNew: 0, StageProving: 1, StageEstablished: 2}

// ValidStage reports whether s names a stage.
func ValidStage(s Stage) bool { _, ok := stageRank[s]; return ok }

// MinStage is the lower of two stages; an empty ceiling is no ceiling.
func MinStage(a, ceiling Stage) Stage {
	if ceiling == "" || stageRank[a] <= stageRank[ceiling] {
		return a
	}
	return ceiling
}

// The founder-approved promotion numbers (2026-11-13).
const (
	provingDays     = 7
	provingChecks   = 50
	provingRate     = 0.95
	establishedDays = 30
	establishedRate = 0.98
)

// Evidence is what Core knows about a Tower when it computes the stage. Passed and Failed are
// health checks since EnteredAt; Disqualified is an unexcused disqualifier since EnteredAt.
type Evidence struct {
	Now, EnteredAt time.Time
	Current        Stage
	Passed, Failed int
	Disqualified   bool
	Ceiling        Stage
}

// NextStage is the stage after one sweep - a disqualifier drops to new, otherwise at most one
// step up, never above an administrator's ceiling - and, in plain words, what the next stage
// still needs ("" when there is none).
func NextStage(e Evidence) (Stage, string) {
	if e.Disqualified {
		return MinStage(StageNew, e.Ceiling), need(Evidence{Now: e.Now, EnteredAt: e.Now, Current: StageNew, Ceiling: e.Ceiling})
	}
	next := e.Current
	switch e.Current {
	case StageNew:
		if ready(e, provingDays, provingChecks, provingRate) {
			next = StageProving
		}
	case StageProving:
		if ready(e, establishedDays, 0, establishedRate) {
			next = StageEstablished
		}
	}
	next = MinStage(next, e.Ceiling)
	if next != e.Current {
		return next, need(Evidence{Now: e.Now, EnteredAt: e.Now, Current: next, Ceiling: e.Ceiling})
	}
	return next, need(e)
}

func rate(passed, failed int) float64 {
	if passed+failed == 0 {
		return 0
	}
	return float64(passed) / float64(passed+failed)
}

func ready(e Evidence, days, checks int, minRate float64) bool {
	return e.Now.Sub(e.EnteredAt) >= time.Duration(days)*24*time.Hour &&
		e.Passed >= checks && rate(e.Passed, e.Failed) >= minRate
}

func need(e Evidence) string {
	var days, checks int
	var minRate float64
	switch e.Current {
	case StageNew:
		days, checks, minRate = provingDays, provingChecks, provingRate
	case StageProving:
		days, minRate = establishedDays, establishedRate
	default:
		return ""
	}
	if e.Ceiling != "" && stageRank[e.Ceiling] <= stageRank[e.Current] {
		return "an administrator has held this Tower at " + string(e.Ceiling)
	}
	var parts []string
	left := time.Duration(days)*24*time.Hour - e.Now.Sub(e.EnteredAt)
	if left > 0 {
		parts = append(parts, fmt.Sprintf("%d more days", int((left+23*time.Hour+59*time.Minute)/(24*time.Hour))))
	}
	if e.Passed < checks {
		parts = append(parts, fmt.Sprintf("%d more passed health checks", checks-e.Passed))
	}
	if len(parts) == 0 && rate(e.Passed, e.Failed) < minRate {
		parts = append(parts, fmt.Sprintf("a pass rate of %g%% or better (now %.1f%%)", minRate*100, rate(e.Passed, e.Failed)*100))
	}
	return strings.Join(parts, ", ")
}
