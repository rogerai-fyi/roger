package main

// Executable spec: features/trust/probe_min.feature - the operator-declared minimum probe
// interval and the verification it costs. Steps drive the real probeOnce, demand hook,
// pickFor, market/discover reads and markMeasured; the shared verdict store is miniredis.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
)

type probeMinState struct {
	t    *testing.T
	b    *broker
	mr   *miniredis.Miniredis
	last time.Time
	logs *bytes.Buffer
	mu   sync.Mutex
	old  string // the shared tools mark's timestamp before the wait
}

func (s *probeMinState) reset() {
	s.b = probeMinBroker()
	s.mr, s.logs, s.old = nil, nil, ""
}

func (s *probeMinState) probedAnHourAgo() error {
	addNode(s.b, "n", sixHours)
	s.last = time.Now().Add(-time.Hour)
	s.b.probeSched["n"] = &probeState{lastProbe: s.last, lastMeasured: s.last, nextDue: time.Now().Add(-time.Second)}
	return nil
}

func (s *probeMinState) browseAndPick() error {
	for i := 0; i < 20; i++ {
		_ = s.b.computeMarket()
		s.b.mu.Lock()
		_ = s.b.enrichOffersForNode(nil, s.b.nodes["n"], time.Now(), nil, true)
		if _, _, ok := s.b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{}); !ok {
			s.b.mu.Unlock()
			return fmt.Errorf("the pick did not route to the station")
		}
		s.b.mu.Unlock()
		s.b.probeOnce()
	}
	return nil
}

func (s *probeMinState) noProbeBeforeMinimum() error {
	if got := lastProbeOf(s.b, "n"); !got.Equal(s.last) {
		return fmt.Errorf("probed %s after the last probe against a declared 6h minimum", got.Sub(s.last))
	}
	return nil
}

func (s *probeMinState) probedAgainAfterMinimum() error {
	s.b.metricsMu.Lock()
	// Let six hours pass: shift the whole schedule back, as the clock would have.
	st := s.b.probeSched["n"]
	shift := 6*time.Hour + time.Second
	s.last = st.lastProbe.Add(-shift)
	st.lastProbe, st.nextDue = s.last, st.nextDue.Add(-shift)
	s.b.metricsMu.Unlock()
	s.b.probeOnce()
	if !lastProbeOf(s.b, "n").After(s.last) {
		return fmt.Errorf("the station was not probed once its declared minimum had passed")
	}
	return nil
}

func (s *probeMinState) neverProbed() error {
	addNode(s.b, "n", sixHours)
	s.b.trust["n"] = trustState{}
	return nil
}

func (s *probeMinState) roundsRun() error {
	s.last = time.Time{}
	for i := 0; i < 5; i++ {
		s.b.probeOnce()
		if i == 0 {
			s.last = lastProbeOf(s.b, "n")
		}
	}
	return nil
}

func (s *probeMinState) probedExactlyOnce() error {
	if s.last.IsZero() {
		return fmt.Errorf("the first-sight probe never fired")
	}
	if got := lastProbeOf(s.b, "n"); !got.Equal(s.last) {
		return fmt.Errorf("probed again %s after the first-sight probe", got.Sub(s.last))
	}
	return nil
}

func (s *probeMinState) declaredThirtyDays() error {
	s.logs = &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(&lockedWriter{w: s.logs, mu: &s.mu})
	s.t.Cleanup(func() { log.SetOutput(prev) })
	addNode(s.b, "n", 30*86400)
	return nil
}

func (s *probeMinState) honoursAtMostADay() error {
	s.b.probeSched["n"] = &probeState{lastProbe: time.Now().Add(-24*time.Hour - time.Second)}
	s.b.probeOnce()
	if since := time.Since(lastProbeOf(s.b, "n")); since > time.Minute {
		return fmt.Errorf("a 30-day declaration held the station past the 24h cap")
	}
	s.b.metricsMu.Lock()
	got := s.b.probeSched["n"].probeMin
	s.b.metricsMu.Unlock()
	if got != 24*time.Hour {
		return fmt.Errorf("effective minimum = %s, want the 24h cap", got)
	}
	return nil
}

func (s *probeMinState) clampLoggedOnce() error {
	s.b.probeOnce()
	s.b.probeOnce()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := strings.Count(s.logs.String(), "node=n probe_min_s=2592000 clamped"); n != 1 {
		return fmt.Errorf("clamp logged %d times, want once:\n%s", n, s.logs.String())
	}
	return nil
}

func (s *probeMinState) declaredOnly() error {
	s.mr = miniredis.RunT(s.t)
	vs, err := newValkeyStore("redis://" + s.mr.Addr())
	if err != nil {
		return err
	}
	s.b.shared = vs
	addNode(s.b, "n", sixHours)
	s.b.toolsOK = map[string]bool{toolKey("n", "m"): true}
	s.b.toolsMerged = map[string]bool{toolKey("n", "m"): true}
	return nil
}

func (s *probeMinState) lastPassOlderThanWindow() error {
	s.last = time.Now().Add(-time.Hour) // past the 15m ceiling, inside the 6h minimum
	s.b.probeSched["n"] = &probeState{lastProbe: s.last, lastMeasured: s.last}
	s.old = strconv.FormatInt(time.Now().Add(-40*time.Minute).UnixMilli(), 10)
	s.mr.HSet(toolsKey(), toolKey("n", "m"), s.old)
	s.b.probeOnce()
	return nil
}

func (s *probeMinState) verifiedMarks() (discover, market bool, err error) {
	s.b.mu.Lock()
	views := s.b.enrichOffersForNode(nil, s.b.nodes["n"], time.Now(), nil, false)
	s.b.mu.Unlock()
	mkt := s.b.computeMarket().(map[string]any)["market"].([]marketView)
	if len(views) != 1 || len(mkt) != 1 {
		return false, false, fmt.Errorf("want one discover offer and one market row, got %d and %d", len(views), len(mkt))
	}
	return views[0].Verified, mkt[0].Verified, nil
}

func (s *probeMinState) shownUnverified() error {
	d, m, err := s.verifiedMarks()
	if err != nil {
		return err
	}
	if d || m {
		return fmt.Errorf("an hour without a probe still shows verified (discover=%v market=%v)", d, m)
	}
	return nil
}

func (s *probeMinState) toolsNotReasserted() error {
	if got := s.mr.HGet(toolsKey(), toolKey("n", "m")); got != s.old {
		return fmt.Errorf("the held station's shared tools mark was re-asserted (%s -> %s)", s.old, got)
	}
	return nil
}

func (s *probeMinState) trafficRefreshesAsToday() error {
	s.b.markMeasured("n")
	d, m, err := s.verifiedMarks()
	if err != nil {
		return err
	}
	if !d || !m {
		return fmt.Errorf("a real served request did not refresh the verified mark (discover=%v market=%v)", d, m)
	}
	if got := lastProbeOf(s.b, "n"); !got.Equal(s.last) {
		return fmt.Errorf("real traffic triggered a probe inside the declared minimum")
	}
	return nil
}

func (s *probeMinState) rareAndFresh() error {
	addNode(s.b, "rare", sixHours)
	addNode(s.b, "fresh", 0)
	now := time.Now()
	s.b.probeSched["rare"] = &probeState{probeMin: 6 * time.Hour, lastProbe: now.Add(-2 * time.Hour), lastMeasured: now.Add(-2 * time.Hour)}
	s.b.probeSched["fresh"] = &probeState{lastProbe: now.Add(-time.Minute), lastMeasured: now.Add(-time.Minute)}
	s.b.tps["rare"], s.b.tps["fresh"] = 50, 50
	return nil
}

func (s *probeMinState) rareRanksBelow() error {
	now := time.Now()
	s.b.metricsMu.Lock()
	f := s.b.measurementStalenessLocked("rare", now)
	s.b.metricsMu.Unlock()
	if f >= 1.0 {
		return fmt.Errorf("the rarely probed station took no staleness discount (%.2f)", f)
	}
	s.b.mu.Lock()
	views := s.b.enrichOffersForNode(nil, s.b.nodes["rare"], now, nil, false)
	views = s.b.enrichOffersForNode(views, s.b.nodes["fresh"], now, nil, false)
	s.b.mu.Unlock()
	if views[0].Signal >= views[1].Signal {
		return fmt.Errorf("rare signal %d is not below fresh %d", views[0].Signal, views[1].Signal)
	}
	return nil
}

func (s *probeMinState) helpDisclosesTrade() error {
	src, err := os.ReadFile(filepath.Join("..", "rogerai", "main.go"))
	if err != nil {
		return err
	}
	for _, want := range []string{"--probe-min", "capped at 24h", "verification lapses between probes"} {
		if !strings.Contains(string(src), want) {
			return fmt.Errorf("roger share help does not say %q", want)
		}
	}
	return nil
}

func TestProbeMinFeature(t *testing.T) {
	st := &probeMinState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a station that declared a 6 hour minimum probe interval and was probed an hour ago$`, st.probedAnHourAgo)
			sc.Step(`^consumers browse the market repeatedly and a pick routes to it on a stale reading$`, st.browseAndPick)
			sc.Step(`^no probe fires before the declared minimum$`, st.noProbeBeforeMinimum)
			sc.Step(`^once the declared minimum has passed the station is probed again$`, st.probedAgainAfterMinimum)
			sc.Step(`^a station that declared a 6 hour minimum probe interval and was never probed$`, st.neverProbed)
			sc.Step(`^probe rounds run$`, st.roundsRun)
			sc.Step(`^the station is probed exactly once$`, st.probedExactlyOnce)
			sc.Step(`^a station that declared a minimum probe interval of 30 days$`, st.declaredThirtyDays)
			sc.Step(`^the broker honours at most 24 hours$`, st.honoursAtMostADay)
			sc.Step(`^the clamp is logged once for that station$`, st.clampLoggedOnce)
			sc.Step(`^a station that declared a 6 hour minimum probe interval$`, st.declaredOnly)
			sc.Step(`^its last passed probe is older than the normal verification window$`, st.lastPassOlderThanWindow)
			sc.Step(`^the market and discover show it as not currently verified$`, st.shownUnverified)
			sc.Step(`^its verified tools mark is not re-asserted while it waits$`, st.toolsNotReasserted)
			sc.Step(`^real served traffic refreshes verification exactly as it does for any station$`, st.trafficRefreshesAsToday)
			sc.Step(`^a rarely probed station and a freshly probed station for the same model at the same speed$`, st.rareAndFresh)
			sc.Step(`^the rarely probed station takes the staleness discount and ranks below the fresh one$`, st.rareRanksBelow)
			sc.Step(`^the share help says the minimum is capped at 24h and that verification lapses between probes$`, st.helpDisclosesTrade)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/trust/probe_min.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("probe-min scenarios failed")
	}
}
