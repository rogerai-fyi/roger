package edge

// Executable spec: features/edge/mode.feature (@edge) - the ROOT, the ROUTE and the
// PREFERENCE at the source: a real status record, a real session ledger, real receipts.
// The three scenarios that need the dispatch ladder are tagged @later and run once it lands.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
)

type modeBDD struct {
	t    *testing.T
	st   SelfStatus
	sess *Sessions
	last Session
}

func (s *modeBDD) reset() {
	s.st = SelfStatus{}
	s.sess = NewSessions("acct-1")
	s.sess.SetClock(func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) })
	s.last = Session{}
}

func (s *modeBDD) rootedLocal(where string) error {
	s.st = SelfStatus{Root: RootLocal, Authority: where, AuthorityLocal: true}
	return nil
}
func (s *modeBDD) rootedCore() error {
	s.st = SelfStatus{Root: RootCore, Authority: "Core"}
	return nil
}
func (s *modeBDD) unreadable() error {
	s.st = SelfStatus{Err: "edge record: permission denied"}
	return nil
}

func (s *modeBDD) reportsRoot(want string) error {
	if s.st.Root != want || !strings.HasPrefix(s.st.RootBadge(), want) {
		return fmt.Errorf("root = %q badge %q", s.st.Root, s.st.RootBadge())
	}
	return nil
}

func (s *modeBDD) names(where string) error {
	if !strings.Contains(s.st.RootBadge(), where) {
		return fmt.Errorf("badge %q does not name %q", s.st.RootBadge(), where)
	}
	return nil
}

func (s *modeBDD) saysNoNetwork() error {
	if !strings.Contains(strings.Join(s.st.AuthorityLines(), " "), "no network beyond this LAN") {
		return fmt.Errorf("lines = %v", s.st.AuthorityLines())
	}
	return nil
}

func (s *modeBDD) saysNeedsNetwork() error {
	if !strings.Contains(strings.Join(s.st.AuthorityLines(), " "), "needs the network") {
		return fmt.Errorf("lines = %v", s.st.AuthorityLines())
	}
	return nil
}

func (s *modeBDD) rootUnknown() error {
	if s.st.RootBadge() != "ROOT UNKNOWN" || s.st.Err == "" {
		return fmt.Errorf("badge %q err %q", s.st.RootBadge(), s.st.Err)
	}
	return nil
}

func (s *modeBDD) doesNotClaim(word string) error {
	if strings.Contains(s.st.RootBadge(), word+" ROOT") || s.st.RootBadge() == word {
		return fmt.Errorf("badge claims %s: %q", word, s.st.RootBadge())
	}
	return nil
}

func (s *modeBDD) record(recs ...protocol.UsageReceipt) error {
	ses, err := s.sess.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: recs[0].RequestID, Receipts: recs})
	s.last = ses
	return err
}

func (s *modeBDD) servedByInstance(addr string) error {
	node, inst := SplitAddress(addr)
	return s.record(protocol.UsageReceipt{RequestID: "r1", NodeID: node, Instance: inst, Model: "gpt-oss-20b", Local: true})
}
func (s *modeBDD) servedByBroker(station string) error {
	return s.record(protocol.UsageReceipt{RequestID: "r1", NodeID: station, Model: "gpt-oss-20b"})
}
func (s *modeBDD) servedByOwnModel() error {
	return s.record(protocol.UsageReceipt{RequestID: "r1", NodeID: "workshop", Instance: "desk", Model: "gpt-oss-20b", Local: true})
}
func (s *modeBDD) preferenceIs(p string) error { s.st.Prefer = p; return nil }
func (s *modeBDD) fellOutToMarket() error {
	return s.record(protocol.UsageReceipt{RequestID: "r1", NodeID: "house-or-1", Model: "gpt-oss-120b"})
}
func (s *modeBDD) refusedOnMarket(reason string) error {
	return s.record(protocol.UsageReceipt{RequestID: "r1", NodeID: "house-or-1", Model: "gpt-oss-120b", VoidReason: reason})
}
func (s *modeBDD) sessionRead() error {
	got, ok := s.sess.Get("r1")
	if !ok {
		return fmt.Errorf("no session")
	}
	s.last = got
	return nil
}
func (s *modeBDD) routeIs(want string) error {
	if s.last.Where != want {
		return fmt.Errorf("route = %q, want %q (%+v)", s.last.Where, want, s.last)
	}
	return nil
}
func (s *modeBDD) namesInstance() error {
	if s.last.Station == "" {
		return fmt.Errorf("no station on %+v", s.last)
	}
	return nil
}
func (s *modeBDD) namesStation(station string) error {
	if s.last.Station != station {
		return fmt.Errorf("station = %q", s.last.Station)
	}
	return nil
}
func (s *modeBDD) namesThisInstance() error { return s.namesInstance() }
func (s *modeBDD) preferenceUnchanged() error {
	if s.st.Prefer != PreferLocal {
		return fmt.Errorf("prefer = %q", s.st.Prefer)
	}
	return nil
}
func (s *modeBDD) outcomeRefusal(reason string) error {
	if s.last.Outcome != OutcomeRefused || s.last.Reason != reason {
		return fmt.Errorf("outcome = %+v", s.last)
	}
	return nil
}
func (s *modeBDD) noPreferenceSet() error { s.st = SelfStatus{}; return nil }
func (s *modeBDD) preferenceIsLocal() error {
	if s.st.EffectivePrefer() != PreferLocal {
		return fmt.Errorf("prefer = %q", s.st.EffectivePrefer())
	}
	return nil
}
func (s *modeBDD) modeSaysSo() error {
	if s.st.PreferBadge() != "prefers local" {
		return fmt.Errorf("badge = %q", s.st.PreferBadge())
	}
	return nil
}

func TestModeFeature(t *testing.T) {
	st := &modeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^an Edge rooted at the designated machine "([^"]*)"$`, st.rootedLocal)
			sc.Step(`^an Edge rooted at Core$`, st.rootedCore)
			sc.Step(`^the machine's Edge record cannot be read$`, st.unreadable)
			sc.Step(`^its mode reports the root as (LOCAL|CORE)$`, st.reportsRoot)
			sc.Step(`^it names "([^"]*)"$`, st.names)
			sc.Step(`^it says enrolling a new node needs no network beyond this LAN$`, st.saysNoNetwork)
			sc.Step(`^it says enrolling a new node needs the network$`, st.saysNeedsNetwork)
			sc.Step(`^its mode reports the root as unknown, with the reason$`, st.rootUnknown)
			sc.Step(`^it does not claim (LOCAL|CORE)$`, st.doesNotClaim)
			sc.Step(`^a receipted turn served by the instance "([^"]*)" on this Edge$`, st.servedByInstance)
			sc.Step(`^a receipted turn served through the broker by the station "([^"]*)"$`, st.servedByBroker)
			sc.Step(`^a receipted turn served by this instance's own model$`, st.servedByOwnModel)
			sc.Step(`^the preference is (local|market|local-only)$`, st.preferenceIs)
			sc.Step(`^a turn that fell out to the market because no instance served the band$`, st.fellOutToMarket)
			sc.Step(`^a turn refused for "([^"]*)" by a station on the market$`, st.refusedOnMarket)
			sc.Step(`^the session is read$`, st.sessionRead)
			sc.Step(`^its route is (local|market)$`, st.routeIs)
			sc.Step(`^it names the instance that served it$`, st.namesInstance)
			sc.Step(`^it names the station that served it$`, func() error { return st.namesStation("house-or-1") })
			sc.Step(`^it names this instance$`, st.namesThisInstance)
			sc.Step(`^nothing about the preference changed what the row says$`, st.preferenceUnchanged)
			sc.Step(`^its outcome is the refusal, with the reason$`, func() error { return st.outcomeRefusal("over-limit") })
			sc.Step(`^a machine with no preference set$`, st.noPreferenceSet)
			sc.Step(`^its preference is local$`, st.preferenceIsLocal)
			sc.Step(`^the mode says so$`, st.modeSaysSo)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@edge && ~@later",
			Paths: []string{"../../features/edge/mode.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the mode scenarios failed")
	}
}
