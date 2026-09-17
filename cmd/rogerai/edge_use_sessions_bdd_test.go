package main

// Executable spec: features/edge/session_attribution.feature (@cli) - `roger use` records
// its turns as `roger use` sessions on this machine's Edge, through the shared mirror any
// Edge view on the machine merges. Isolated config dir; the real recorder and a real
// second ledger reading the mirror.

import (
	"context"
	"fmt"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
)

type useSessBDD struct {
	t   *testing.T
	rec func(protocol.UsageReceipt)
}

func (s *useSessBDD) reset() { useTempConfig(s.t); s.rec = nil }

func (s *useSessBDD) recorder() error { s.rec = edgeUseRecorder(); return nil }

func (s *useSessBDD) handed(band, station string) error {
	s.rec(protocol.UsageReceipt{RequestID: "req-use-1", NodeID: station, Model: band, CompletionTokens: 3})
	return nil
}

func (s *useSessBDD) handedNoID() error {
	s.rec(protocol.UsageReceipt{NodeID: "house-or-1", Model: "gpt-oss-120b"})
	return nil
}

// viewer is what any OTHER process on this machine sees: a fresh ledger of the same
// account, merging the mirror directory.
func (s *useSessBDD) viewer() *edge.Sessions {
	v := edge.NewSessions(edgeAccount())
	v.Mirror(edgeSessionsDir())
	return v
}

func (s *useSessBDD) mirrorHoldsOne(who, band, station string) error {
	live := s.viewer().Live()
	if len(live) != 1 {
		return fmt.Errorf("the mirror holds %d sessions: %+v", len(live), live)
	}
	x := live[0]
	if x.Attribution() != who || x.Band != band || x.Station != station {
		return fmt.Errorf("session = %+v", x)
	}
	return nil
}

func (s *useSessBDD) mirrorHoldsNone() error {
	if live := s.viewer().Live(); len(live) != 0 {
		return fmt.Errorf("the mirror holds %+v", live)
	}
	return nil
}

func TestEdgeUseSessionsFeature(t *testing.T) {
	st := &useSessBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step("^the `roger use` recorder for this machine$", st.recorder)
			sc.Step(`^it is handed a receipt for "([^"]*)" served by "([^"]*)"$`, st.handed)
			sc.Step(`^it is handed a receipt with no request id$`, st.handedNoID)
			sc.Step(`^this machine's session mirror holds one session attributed to "([^"]*)", band "([^"]*)", station "([^"]*)"$`, st.mirrorHoldsOne)
			sc.Step(`^this machine's session mirror holds no session$`, st.mirrorHoldsNone)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/session_attribution.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the roger use session scenarios failed")
	}
}
