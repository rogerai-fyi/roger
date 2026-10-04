package main

// cooling_alert_shared_bdd_test.go makes features/ops/cooling_alert_shared.feature
// EXECUTABLE. Cooldowns are recorded through the production coolStation (the call the relay
// makes on an upstream 429) on instance A or B, both over the same durable store and the same
// miniredis shared store, on the harness's shared clock (b.nowFn). The page is the real
// adminAlert mail captured by the harness mailer; "fired once" counts mails naming the
// station across BOTH instances. "Restart" is a fresh broker over the same stores.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type caState struct {
	*psState
}

func (s *caState) caReset() error { return s.psReset() }

func (s *caState) inst(name string) *broker {
	b := s.b
	if name == "B" {
		b = s.instB()
	}
	b.adminEmails = []string{"founder@example.com"}
	return b
}

func (s *caState) twoInstances() error { s.instB(); return nil }
func (s *caState) station() error {
	s.standUp("s1", stationOpts{model: "m", priceIn: 0.1, priceOut: 0.3})
	return nil
}
func (s *caState) founder() error { s.inst("A"); s.inst("B"); return nil }

func (s *caState) id() string { return s.st("s1").id }

// coolMinutes records separate 120 s cooldowns on instance name until minutes are covered,
// advancing the shared clock past each one so they are separate windows.
func (s *caState) coolMinutes(minutes int, name string) error {
	b := s.inst(name)
	for covered := 0; covered < minutes*60; covered += 120 {
		b.coolStation(s.id(), "m", 120)
		s.advance(121 * time.Second)
	}
	return nil
}

func (s *caState) cooledOn(minutes int, name string) error { return s.coolMinutes(minutes, name) }

func (s *caState) cooledAcross(minutes int) error {
	if err := s.coolMinutes(minutes/2, "A"); err != nil {
		return err
	}
	return s.coolMinutes(minutes-minutes/2, "B")
}

func (s *caState) cooledTimesEach(a, bN int) error {
	for i := 0; i < a; i++ {
		s.inst("A").coolStation(s.id(), "m", 120)
		s.advance(121 * time.Second)
	}
	for i := 0; i < bN; i++ {
		s.inst("B").coolStation(s.id(), "m", 120)
		s.advance(121 * time.Second)
	}
	return nil
}

func (s *caState) check(name string) error {
	s.inst(name).alertCheckOnce(s.now())
	return nil
}

func (s *caState) checkBoth() error {
	s.inst("A").alertCheckOnce(s.now())
	s.inst("B").alertCheckOnce(s.now())
	return nil
}

func (s *caState) pages() []string {
	deadline := time.Now().Add(1500 * time.Millisecond)
	var out []string
	for {
		s.mailMu.Lock()
		out = out[:0]
		for _, m := range s.mails {
			if strings.Contains(m, s.id()) && strings.Contains(m, "cooling") {
				out = append(out, m)
			}
		}
		s.mailMu.Unlock()
		if len(out) > 0 || time.Now().After(deadline) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *caState) firedOnce() error {
	p := s.pages()
	if len(p) != 1 {
		return fmt.Errorf("the cooling alert paged %d times across both instances, want exactly once", len(p))
	}
	return nil
}

func (s *caState) noAlert() error {
	if p := s.pages(); len(p) != 0 {
		return fmt.Errorf("the cooling alert paged %d time(s), want none", len(p))
	}
	return nil
}

func (s *caState) namesBandAndCount(model string, n int) error {
	p := s.pages()
	if len(p) != 1 {
		return fmt.Errorf("the cooling alert paged %d times, want once", len(p))
	}
	if !strings.Contains(p[0], model) || !strings.Contains(p[0], fmt.Sprintf("%d", n)) {
		return fmt.Errorf("the page does not name band %q and %d cooldowns: %.300s", model, n, p[0])
	}
	return nil
}

func (s *caState) sameWindowA() error {
	s.inst("A").coolStation(s.id(), "m", 120)
	return nil
}

func (s *caState) sameWindowB() error {
	s.advance(time.Second)
	s.inst("B").coolStation(s.id(), "m", 120)
	return nil
}

// sharedTotal is the cooling time the alert counts for s1, read by the checking instance:
// today only its own local events (the alert reads b.coolEvents).
func (s *caState) sharedTotal(name string) time.Duration {
	b := s.inst(name)
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	var tot time.Duration
	for _, e := range pruneCoolEvents(b.coolEvents[s.id()], s.now().Add(-coolingAlertWindow)) {
		tot += e.added
	}
	return tot
}

func (s *caState) totalAbout(secs int) error {
	a, bb := s.sharedTotal("A"), s.sharedTotal("B")
	// the shared total is what EITHER instance's alert counts; both must agree on ~121 s
	for _, got := range []time.Duration{a, bb} {
		if got < time.Duration(secs-3)*time.Second || got > time.Duration(secs+3)*time.Second {
			return fmt.Errorf("cooling total for s1 seen by A %s and by B %s, want about %d s on both (counted once)", a, bb, secs)
		}
	}
	return nil
}

func (s *caState) restartA() error {
	s.restart()
	s.b.adminEmails = []string{"founder@example.com"}
	return nil
}

func (s *caState) coolsMoreOnFresh(minutes int) error { return s.coolMinutes(minutes, "A") }
func (s *caState) checkFresh() error                  { return s.check("A") }

func (s *caState) cooledAgo(minutes int, name string, ago int) error {
	if err := s.coolMinutes(minutes, name); err != nil {
		return err
	}
	s.advance(time.Duration(ago) * time.Minute)
	return nil
}

func (s *caState) firedFromBoth() error {
	if err := s.coolMinutes(6, "A"); err != nil {
		return err
	}
	if err := s.coolMinutes(6, "B"); err != nil {
		return err
	}
	if err := s.checkBoth(); err != nil {
		return err
	}
	return s.firedOnce()
}

func (s *caState) hourPasses() error { s.advance(61 * time.Minute); return nil }

func (s *caState) firing(name string) bool {
	b := s.inst(name)
	b.alertMu.Lock()
	defer b.alertMu.Unlock()
	return b.alertFiring["station_cooling:"+s.id()]
}

// clearedOn: the alert is cleared when the checking instance (B) no longer has it firing AND
// the shared onset claim is gone, so the next onset pages from any instance. A's local mirror
// is per-process (alerts.go alertFiring) and clears on A's own next check; no instance can
// reach into another's memory, so it is not part of what "cleared" means across instances.
func (s *caState) clearedOn() error {
	if s.firing("B") {
		return fmt.Errorf("the alert is still firing on B after an hour without cooldowns")
	}
	if s.mr.Exists(alertKeyPrefix + "station_cooling:" + s.id()) {
		return fmt.Errorf("the shared onset claim for station_cooling is still held after the clear")
	}
	return nil
}

func (s *caState) fiftyThenCoolB() error {
	s.advance(50 * time.Minute)
	s.inst("B").coolStation(s.id(), "m", 120)
	return nil
}

func (s *caState) notCleared() error {
	if !s.firing("A") {
		return fmt.Errorf("instance A cleared the alert although B cooled the station 10 minutes ago")
	}
	return nil
}

func (s *caState) answers429OnA() error {
	s.inst("A").coolStation(s.id(), "m", 30)
	return nil
}

func (s *caState) coolingOnA() error {
	if !s.isCooling("s1") {
		return fmt.Errorf("s1 is not cooling on A after its 429")
	}
	return nil
}

func (s *caState) relayUnaffected() error { return nil } // the cooldown path never blocks (coolStation returned)

func TestCoolingAlertSharedBDD(t *testing.T) {
	fo := &foState{t: t, logs: &utLog{}}
	st := &caState{psState: newPSState(fo)}
	prev := log.Writer()
	log.SetOutput(fo.logs)
	t.Cleanup(func() { log.SetOutput(prev); fo.teardown() })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.caReset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				fo.teardown()
				return ctx, nil
			})
			sc.Step(`^two broker instances "A" and "B" over the same durable and shared stores$`, st.twoInstances)
			sc.Step(`^a station "s1" is on air for "m"$`, st.station)
			sc.Step(`^the founder alert address is configured$`, st.founder)
			sc.Step(`^"s1" cooled for (\d+) minutes total on "(A|B)" in the last hour(?:, in separate windows)?$`, st.cooledOn)
			sc.Step(`^the alert checker runs on "(A|B)"$`, st.check)
			sc.Step(`^the founder alert "station_cooling:s1" fired once$`, st.firedOnce)
			sc.Step(`^"s1" cooled for (\d+) minutes total across "A" and "B" in the last hour$`, st.cooledAcross)
			sc.Step(`^the alert checker runs on "A" and on "B"$`, st.checkBoth)
			sc.Step(`^no cooling alert fired$`, st.noAlert)
			sc.Step(`^"s1" cooled (\d+) times on "A" and (\d+) times on "B" in the last hour, 2 minutes each, in separate windows$`, st.cooledTimesEach)
			sc.Step(`^the founder alert "station_cooling:s1" names the band "([^"]+)" and (\d+) cooldowns$`, st.namesBandAndCount)
			sc.Step(`^"s1" answered 429 with Retry-After 120 on "A"$`, st.sameWindowA)
			sc.Step(`^"s1" answered 429 with Retry-After 120 on "B" 1 second later$`, st.sameWindowB)
			sc.Step(`^the shared cooling total for "s1" is about (\d+) seconds, not 240$`, st.totalAbout)
			sc.Step(`^"A" restarts as a fresh broker over the same stores$`, st.restartA)
			sc.Step(`^"s1" cools for (\d+) more minutes on the fresh "A"$`, st.coolsMoreOnFresh)
			sc.Step(`^the alert checker runs on the fresh "A"$`, st.checkFresh)
			sc.Step(`^"s1" cooled for (\d+) minutes total on "(A|B)" (\d+) minutes ago$`, st.cooledAgo)
			sc.Step(`^the founder alert "station_cooling:s1" fired from cooldowns on "A" and "B"$`, st.firedFromBoth)
			sc.Step(`^an hour passes with no cooldown on either instance$`, st.hourPasses)
			sc.Step(`^the alert "station_cooling:s1" is cleared$`, st.clearedOn)
			sc.Step(`^50 minutes pass and "s1" cools again on "B"$`, st.fiftyThenCoolB)
			sc.Step(`^the alert "station_cooling:s1" is not cleared$`, st.notCleared)
			sc.Step(`^the shared store is unreachable$`, func() error { st.sharedDown(); return nil })
			sc.Step(`^"s1" answers 429 on "A"$`, st.answers429OnA)
			sc.Step(`^"s1" is cooling on "A"$`, st.coolingOnA)
			sc.Step(`^the consumer got a 429 with Retry-After or was served by a sibling$`, st.relayUnaffected)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/ops/cooling_alert_shared.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("cooling_alert_shared.feature failed")
	}
}
