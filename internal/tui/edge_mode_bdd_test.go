package tui

// Executable spec: features/edge/mode.feature (@tui) - the root badge in the header, the
// route on every session row, the mix in the header, local-only visible. The real model and
// renderer, a real ledger fed real receipts, the status hook filled the way the host fills it.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type modeTUIBDD struct {
	t       *testing.T
	now     time.Time
	status  edge.SelfStatus
	sess    *edge.Sessions
	fleet   *edge.Fleet
	m       model
	built   bool
	out     string
	nocolor bool
}

func (s *modeTUIBDD) reset() {
	s.now = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s.status = edge.SelfStatus{Root: edge.RootCore, Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	s.sess = edge.NewSessions("acct-1")
	s.sess.SetClock(func() time.Time { return s.now })
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.built, s.out, s.nocolor = false, "", false
}

func (s *modeTUIBDD) build() {
	if s.built {
		return
	}
	hooks := Hooks{Station: "gentle-mongoose-93", EdgeFleet: s.fleet, EdgeSessions: s.sess,
		EdgeCandidates: func() []store.EdgeNode { return nil },
		EdgeStatus:     func() edge.SelfStatus { return s.status }}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	s.m, s.built = m, true
}

func (s *modeTUIBDD) render(w int) string {
	s.build()
	if s.m.mode != modeEdge {
		s.m.enterEdge()
	} else {
		s.m.refreshEdge()
	}
	raw := s.m.edgeView(w)
	if s.nocolor && strings.Contains(raw, "\x1b[") {
		s.t.Errorf("ANSI under NO_COLOR:\n%q", raw)
	}
	s.out = stripANSI(raw)
	return s.out
}

func (s *modeTUIBDD) record(req string, local bool) error {
	rec := protocol.UsageReceipt{RequestID: req, NodeID: "house-or-1", Model: "gpt-oss-20b"}
	if local {
		rec.NodeID, rec.Instance, rec.Local = "jetson", "serve", true
	}
	_, err := s.sess.Record(edge.Traffic{Account: "acct-1", Kind: edge.FromUse, Request: req, Receipts: []protocol.UsageReceipt{rec}})
	return err
}

func (s *modeTUIBDD) rootedLocal(where string) error {
	s.status.Root, s.status.Authority, s.status.AuthorityLocal = edge.RootLocal, where, true
	return nil
}
func (s *modeTUIBDD) rootedCoreWithPrefer(p string) error {
	s.status.Root, s.status.Authority, s.status.Prefer = edge.RootCore, "Core", p
	return nil
}
func (s *modeTUIBDD) rendersAt(cols int) error { s.render(cols); return nil }
func (s *modeTUIBDD) headerBadge(word string) error {
	first := strings.SplitN(s.out, "\n", 2)[0]
	if !strings.Contains(first, word) {
		return fmt.Errorf("header %q lacks %q", first, word)
	}
	return nil
}
func (s *modeTUIBDD) badgeEmptyOrNot() error {
	// empty fleet: the badge is already on the rendered header; now with a node too
	pubNode := store.EdgeNode{ID: "n_" + strings.Repeat("ab", 24), Name: "bench", Kind: "host",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.11:1", Fingerprint: strings.Repeat("ab", 32)}}}
	if _, err := s.fleet.Enroll(pubNode); err != nil {
		return err
	}
	s.render(100)
	return s.headerBadge("LOCAL")
}
func (s *modeTUIBDD) mixedSessions() error {
	if err := s.record("l1", true); err != nil {
		return err
	}
	return s.record("m1", false)
}
func (s *modeTUIBDD) noColor() error {
	s.t.Setenv("NO_COLOR", "1")
	s.built, s.nocolor = false, true
	return nil
}
func (s *modeTUIBDD) eachRowSaysRoute() error {
	rows := 0
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, "gpt-oss-20b") {
			rows++
			if !strings.Contains(ln, "local:") && !strings.Contains(ln, "market:") {
				return fmt.Errorf("row lacks a route word: %q", ln)
			}
		}
	}
	if rows < 2 {
		return fmt.Errorf("%d session rows drawn:\n%s", rows, s.out)
	}
	return nil
}
func (s *modeTUIBDD) distinguishableByText() error {
	if !strings.Contains(s.out, "local:") || !strings.Contains(s.out, "market:") {
		return fmt.Errorf("both routes are not readable as words:\n%s", s.out)
	}
	return nil
}
func (s *modeTUIBDD) nLocalMMarket(n, m int) error {
	for i := 0; i < n; i++ {
		if err := s.record(fmt.Sprintf("l%d", i), true); err != nil {
			return err
		}
	}
	for i := 0; i < m; i++ {
		if err := s.record(fmt.Sprintf("m%d", i), false); err != nil {
			return err
		}
	}
	return nil
}
func (s *modeTUIBDD) headerReportsMix(n, m int) error {
	return s.headerBadge(fmt.Sprintf("%d local · %d market", n, m))
}
func (s *modeTUIBDD) preferenceIs(p string) error { s.status.Prefer = p; return nil }
func (s *modeTUIBDD) headerSaysLocalOnly() error  { return s.headerBadge("LOCAL-ONLY") }
func (s *modeTUIBDD) distinctFromLocallyRooted() error {
	first := strings.SplitN(s.out, "\n", 2)[0]
	// the two labels are both there and both distinct: CORE (root) beside LOCAL-ONLY (preference)
	if !regexp.MustCompile(`CORE.*LOCAL-ONLY|LOCAL ROOT.*LOCAL-ONLY`).MatchString(first) {
		return fmt.Errorf("header does not keep root and preference distinct: %q", first)
	}
	return nil
}
func (s *modeTUIBDD) bothReadable() error {
	first := strings.SplitN(s.out, "\n", 2)[0]
	if !strings.Contains(first, "CORE") || !strings.Contains(first, "prefers local") {
		return fmt.Errorf("header = %q", first)
	}
	return nil
}
func (s *modeTUIBDD) neitherPresentedAsOther() error {
	first := strings.SplitN(s.out, "\n", 2)[0]
	if strings.Contains(first, "LOCAL ROOT") {
		return fmt.Errorf("a Core-rooted Edge shows a LOCAL ROOT badge: %q", first)
	}
	return nil
}

func TestModeFeatureTUI(t *testing.T) {
	st := &modeTUIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^an Edge rooted at the designated machine "([^"]*)"$`, st.rootedLocal)
			sc.Step(`^an Edge rooted at Core with the preference (local|market|local-only)$`, st.rootedCoreWithPrefer)
			sc.Step(`^the Edge screen renders at (\d+) columns$`, st.rendersAt)
			sc.Step(`^the header carries a mode badge reading LOCAL$`, func() error { return st.headerBadge("LOCAL ROOT") })
			sc.Step(`^the badge is present whether or not the fleet is empty$`, st.badgeEmptyOrNot)
			sc.Step(`^sessions that went local and sessions that went to the market$`, st.mixedSessions)
			sc.Step(`^NO_COLOR is set$`, st.noColor)
			sc.Step(`^each row says which route it took$`, st.eachRowSaysRoute)
			sc.Step(`^the two are distinguishable by text alone$`, st.distinguishableByText)
			sc.Step(`^(\d+) sessions that went local and (\d+) that went to the market$`, st.nLocalMMarket)
			sc.Step(`^the header reports (\d+) local and (\d+) market$`, st.headerReportsMix)
			sc.Step(`^the preference is (local|market|local-only)$`, st.preferenceIs)
			sc.Step(`^the header says LOCAL-ONLY$`, st.headerSaysLocalOnly)
			sc.Step(`^it is distinguishable from a merely locally-rooted Edge$`, st.distinctFromLocallyRooted)
			sc.Step(`^the root and the route mix are both readable$`, st.bothReadable)
			sc.Step(`^neither is presented as the other$`, st.neitherPresentedAsOther)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/mode.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the mode TUI scenarios failed")
	}
}
