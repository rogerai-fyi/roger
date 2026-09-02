package main

// Executable spec: features/curated/curated_probes.feature - the curated slow lane.
// Every canary against a curated station is billed to the operator's METERED upstream,
// so verification rides a fixed cadence (default 6h) that neither the adaptive
// schedule nor a market-browse demand spike can pull in.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type curProbeState struct {
	t *testing.T
	b *broker
}

func (s *curProbeState) reset() {
	s.b, _, _, _ = newBandBroker(s.t)
	s.b.probe = loadProbe()
	s.b.trust = map[string]trustState{}
	s.b.inflight = map[string]int{}
}

func (s *curProbeState) bothOnAir() error {
	// The schedule is what the round consults; seed it as probeOnce would after a
	// first probe of each station a minute ago.
	s.b.metricsMu.Lock()
	sched := s.b.probeSchedLocked()
	now := time.Now()
	sched["gpu-1"] = &probeState{lastProbe: now.Add(-time.Minute), nextDue: now.Add(-time.Second)}
	sched["cur-1"] = &probeState{curated: true, lastProbe: now.Add(-time.Minute), nextDue: now.Add(-time.Second)}
	// The curated station PASSED its first canary - the slow lane engages only on a
	// verified station (a failed first probe keeps the adaptive retry).
	s.b.trust["cur-1"] = trustState{probed: true, probeOK: true, probeCompleted: true}
	s.b.metricsMu.Unlock()
	return nil
}

func (s *curProbeState) humanAdaptive() error {
	// The human station's due gate is nextDue alone: a due human is probed this round.
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	st := s.b.probeSchedLocked()["gpu-1"]
	if st.nextDue.After(time.Now()) {
		return fmt.Errorf("the human station should be due on the adaptive lane")
	}
	return nil
}

func (s *curProbeState) curatedNotDueEarly() error {
	// The REAL gate decision, exercised directly (curatedHold is what probeOnce and
	// the demand hook both consult): a VERIFIED curated station probed a minute ago
	// is held; an UNVERIFIED one (failed first canary) is NOT held - it keeps the
	// adaptive retry instead of being stranded for a week.
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	st := s.b.probeSchedLocked()["cur-1"]
	if s.b.probe.curatedEvery <= 0 {
		return fmt.Errorf("the curated lane must have a cadence by default")
	}
	if !s.b.probe.curatedHold(st, true, time.Now()) {
		return fmt.Errorf("a verified curated station probed a minute ago must be held to the %s cadence", s.b.probe.curatedEvery)
	}
	if s.b.probe.curatedHold(st, false, time.Now()) {
		return fmt.Errorf("an UNVERIFIED curated station must keep the adaptive retry, not be stranded by the slow lane")
	}
	return nil
}

func (s *curProbeState) probedMomentsAgo() error { return s.bothOnAir() }

func (s *curProbeState) browsedRepeatedly() error {
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	for i := 0; i < 50; i++ {
		s.b.demandProbeSoonLocked("cur-1", time.Now())
	}
	return nil
}

func (s *curProbeState) noEarlierProbeDue() error {
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	st := s.b.probeSchedLocked()["cur-1"]
	earliest := st.lastProbe.Add(s.b.probe.curatedEvery)
	if st.nextDue.Before(earliest) {
		return fmt.Errorf("50 browses pulled the curated probe in to %v, before the cadence point %v - that is the operator's money", st.nextDue, earliest)
	}
	return nil
}

func (s *curProbeState) helpDisclosesOverhead() error {
	// The CLI's --curated flag help is where an operator signs up; read the source of
	// truth (the same read-the-Go move the web calculator test makes).
	src, err := os.ReadFile(filepath.Join("..", "rogerai", "main.go"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(src), "canary") || !strings.Contains(string(src), "billed to your upstream") || !strings.Contains(string(src), "weekly recheck") {
		return fmt.Errorf("roger share --curated help does not disclose the verification canaries billed to the upstream")
	}
	return nil
}

func TestCuratedProbesFeature(t *testing.T) {
	st := &curProbeState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a curated station and a human station on the air$`, st.bothOnAir)
			sc.Step(`^the human station follows the adaptive probe schedule$`, st.humanAdaptive)
			sc.Step(`^the curated station is not due again before the curated probe interval$`, st.curatedNotDueEarly)
			sc.Step(`^a curated station probed moments ago$`, st.probedMomentsAgo)
			sc.Step(`^consumers browse the market repeatedly$`, st.browsedRepeatedly)
			sc.Step(`^no earlier probe becomes due for the curated station$`, st.noEarlierProbeDue)
			sc.Step(`^the curated share help says one sign-up canary plus a weekly recheck is billed to the upstream$`, st.helpDisclosesOverhead)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/curated/curated_probes.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("curated probe-economics scenarios failed")
	}
}
