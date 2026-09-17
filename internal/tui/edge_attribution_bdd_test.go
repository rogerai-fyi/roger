package tui

// Executable spec: features/edge/session_attribution.feature (@tui) - who opened a turn
// through the TUI's endpoint. The REAL model, the REAL live proxy options the endpoint
// serves from, a REAL guest handoff driven to exec and back through the operator seams,
// and a REAL session ledger.

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/operator"
	"rogerai.fm/roger/v6/internal/protocol"
)

type attribBDD struct {
	t    *testing.T
	m    model
	sess *edge.Sessions
	band string
	resp string
}

func (s *attribBDD) reset() {
	saveExec, saveRoot, saveDelay := operatorExec, operatorScratchRoot, operatorStageDelay
	operatorExec = func(c *exec.Cmd, _ func(error) tea.Msg) tea.Cmd { return nil }
	operatorScratchRoot = s.t.TempDir()
	operatorStageDelay = time.Millisecond
	s.t.Cleanup(func() { operatorExec, operatorScratchRoot, operatorStageDelay = saveExec, saveRoot, saveDelay })
	s.m = asModel(agentReady(s.t))
	s.sess = edge.NewSessions("acct-1")
	s.band, s.resp = "gpt-oss-120b", ""
}

// tune points the TUI's endpoint at a band the way bindChannel does: through liveProxyOpts,
// so whatever that wires is what the endpoint serves from.
func (s *attribBDD) tune(band string) {
	o := offer{Model: band, NodeID: "house-or-1"}
	if s.m.proxyKey == "" {
		s.m.proxyKey = client.NewSessionKey() // bindChannel mints it once per session
	}
	if s.m.proxyHolder == nil {
		s.m.proxyHolder = client.NewProxyOptionsHolder(s.m.liveProxyOpts(o, s.m.alert))
	} else {
		s.m.proxyHolder.SetBand(s.m.liveProxyOpts(o, s.m.alert))
	}
	s.m.endpoint = "http://127.0.0.1:1/v1"
	s.band = band
}

func (s *attribBDD) tunedNoGuest() error {
	s.m.hooks.EdgeSessions = s.sess
	s.tune("gpt-oss-120b")
	return nil
}

func (s *attribBDD) tunedNoLedger() error {
	s.m.hooks.EdgeSessions = nil
	s.tune("gpt-oss-120b")
	return nil
}

func (s *attribBDD) guestPatched(name string) error {
	if err := s.tunedNoGuest(); err != nil {
		return err
	}
	var g operator.Guest
	for _, x := range operator.Registry() {
		if x.Name == name {
			g = x
		}
	}
	if g.Name == "" {
		return fmt.Errorf("no guest %q in the registry", name)
	}
	s.m.operatorDetections = []operator.Detection{{Guest: g, Path: "/fake/" + name, Version: g.KnownGood}}
	var tm tea.Model
	tm, _ = s.m.runAgentCommand("/operator " + name)
	tm, _ = tm.Update(keyMsg("y")) // the pre-launch plate
	tm, _ = tm.Update(operatorExecMsg{})
	s.m = asModel(tm)
	if s.m.operatorHandoff == nil || !s.m.operatorHandoff.execing {
		return fmt.Errorf("the guest was not execed: %+v", s.m.operatorHandoff)
	}
	return nil
}

func (s *attribBDD) guestReturned() error {
	tm, _ := s.m.Update(operatorDoneMsg{})
	s.m = asModel(tm)
	return nil
}

func (s *attribBDD) retuned() error { s.tune("gpt-oss-20b"); return nil }

// relayReturnsReceipt is the endpoint's own seam: the proxy hands the relayed response's
// receipt to the options' OnReceipt, exactly as relayWithFailover does.
func (s *attribBDD) relayReturnsReceipt() error {
	opts := s.m.proxyHolder.Get()
	rec := protocol.UsageReceipt{RequestID: "req-" + s.band, NodeID: "house-or-1", Model: s.band, CompletionTokens: 5}
	if opts.OnReceipt != nil {
		opts.OnReceipt(rec)
	}
	s.resp = "pong"
	return nil
}

func (s *attribBDD) recordsAttributed(who string) error {
	live := s.sess.Live()
	if len(live) != 1 {
		return fmt.Errorf("sessions = %+v", live)
	}
	if got := live[0].Attribution(); got != who {
		return fmt.Errorf("attributed to %q, want %q (%+v)", got, who, live[0])
	}
	return nil
}

func (s *attribBDD) namesBandAndStation() error {
	x := s.sess.Live()[0]
	if x.Band != s.band || x.Station != "house-or-1" {
		return fmt.Errorf("session = %+v", x)
	}
	return nil
}

func (s *attribBDD) namesNewBand() error {
	if x := s.sess.Live()[0]; x.Band != "gpt-oss-20b" {
		return fmt.Errorf("band = %q", x.Band)
	}
	return nil
}

func (s *attribBDD) noSession() error {
	if s.sess.Len() != 0 {
		return fmt.Errorf("sessions = %+v", s.sess.Live())
	}
	return nil
}

func (s *attribBDD) replyReached() error {
	if s.resp != "pong" {
		return fmt.Errorf("no reply")
	}
	return nil
}

func TestEdgeAttributionFeatureTUI(t *testing.T) {
	st := &attribBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^a TUI with a tuned channel and no guest$`, st.tunedNoGuest)
			sc.Step(`^a TUI with a tuned channel and no Edge sessions wired$`, st.tunedNoLedger)
			sc.Step(`^a TUI with a tuned channel and the guest "([^"]*)" patched through$`, st.guestPatched)
			sc.Step(`^the guest has returned to the desk$`, st.guestReturned)
			sc.Step(`^the channel is re-tuned to another band$`, st.retuned)
			sc.Step(`^the endpoint relays a turn that returns a receipt$`, st.relayReturnsReceipt)
			sc.Step(`^the Edge records a session attributed to "([^"]*)"$`, st.recordsAttributed)
			sc.Step(`^it names the band and the station from the receipt$`, st.namesBandAndStation)
			sc.Step(`^it names the new band$`, st.namesNewBand)
			sc.Step(`^no session is recorded$`, st.noSession)
			sc.Step(`^the reply reached the program$`, st.replyReached)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/session_attribution.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the TUI attribution scenarios failed")
	}
}
