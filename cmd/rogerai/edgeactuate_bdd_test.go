package main

// Executable pins for the @cli half of features/edge/node_identity.feature: the ACTUATE CEREMONY
// at the terminal. The rule that actuate is owner-confirmed shipped with the fleet and no surface
// could perform it; these are the scenarios that say what performing it means.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// errf is how a step fails here.
func errf(f string, a ...any) error { return fmt.Errorf(f, a...) }

type actuateCLI struct {
	t    *testing.T
	out  string
	code int
	id   string
	was  store.EdgeCap
}

func (s *actuateCLI) pendingNode(name string) error {
	useTempConfig(s.t)
	s.id = "n_door"
	edgeSeed(s.t, actuateNode(s.id, name, edge.Claimed))
	return nil
}

func (s *actuateCLI) nodeWithoutActuate() error {
	useTempConfig(s.t)
	s.id = "n_nas"
	edgeSeed(s.t, edgeNodeFor(s.id, "nas", "host"))
	return nil
}

func (s *actuateCLI) alreadyConfirmed() error {
	if err := s.pendingNode("shed-door"); err != nil {
		return err
	}
	edgeStdin = strings.NewReader("y\n")
	if _, code := edgeRun(s.t, "edge", "actuate", "shed-door"); code != 0 {
		return errf("the first confirmation failed")
	}
	s.was = capOf(s.t, s.id)
	return nil
}

func (s *actuateCLI) runs(name string) error {
	edgeStdin = strings.NewReader("n\n") // the scenarios that answer yes say so in their own step
	s.out, s.code = edgeRun(s.t, "edge", "actuate", name)
	return nil
}

func (s *actuateCLI) runsOnIt() error { return s.runs("nas") }

func (s *actuateCLI) runsAgain() error {
	edgeStdin = strings.NewReader("y\n")
	s.out, s.code = edgeRun(s.t, "edge", "actuate", "shed-door")
	return nil
}

func (s *actuateCLI) answersYes() error {
	edgeStdin = strings.NewReader("y\n")
	s.out, s.code = edgeRun(s.t, "edge", "actuate", "shed-door")
	return nil
}

func (s *actuateCLI) saysWhatItMeans() error { return s.must("PHYSICAL WORLD") }
func (s *actuateCLI) asksFirst() error       { return s.must("[y/N]") }

func (s *actuateCLI) noLeavesItPending() error {
	if got := capOf(s.t, s.id).State; got != string(edge.Claimed) {
		return errf("answering no left the capability %q", got)
	}
	return nil
}

func (s *actuateCLI) isVerified() error {
	if got := capOf(s.t, s.id).State; got != string(edge.Verified) {
		return errf("actuate is %q, want VERIFIED", got)
	}
	return nil
}

func (s *actuateCLI) recordsWhoAndWhen() error {
	got := capOf(s.t, s.id)
	if got.ConfirmedBy == "" || got.ConfirmedAt == 0 {
		return errf("the confirmation recorded by=%q at=%d", got.ConfirmedBy, got.ConfirmedAt)
	}
	return nil
}

func (s *actuateCLI) historyGainsIt() error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	n, _, _ := st.fleet.Get(s.id)
	for _, e := range n.History {
		if e.What == "actuate-confirmed" {
			return nil
		}
	}
	return errf("the confirmation is not in the node's history")
}

func (s *actuateCLI) refusedNotDeclared() error {
	if s.code == 0 {
		return errf("confirming a node that declares nothing succeeded")
	}
	return s.must("does not declare actuate")
}

func (s *actuateCLI) nothingChanged() error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	n, _, _ := st.fleet.Get(s.id)
	if len(n.Caps) != 0 {
		return errf("the node gained a capability: %+v", n.Caps)
	}
	return nil
}

func (s *actuateCLI) saysAlreadyConfirmed() error { return s.must("already confirmed") }

func (s *actuateCLI) originalUntouched() error {
	got := capOf(s.t, s.id)
	if got.ConfirmedBy != s.was.ConfirmedBy || got.ConfirmedAt != s.was.ConfirmedAt {
		return errf("the original confirmation was overwritten")
	}
	return nil
}

func (s *actuateCLI) must(want string) error {
	if !strings.Contains(s.out, want) {
		return errf("the output does not say %q:\n%s", want, s.out)
	}
	return nil
}

func TestEdgeActuateCeremonyFeature(t *testing.T) {
	st := &actuateCLI{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st.out, st.code, st.was = "", 0, store.EdgeCap{}
			sc.Step(`^a node "([^"]*)" declaring "actuate", pending confirmation$`, st.pendingNode)
			sc.Step(`^a node that does not declare "actuate"$`, st.nodeWithoutActuate)
			sc.Step(`^a node whose actuate the owner already confirmed$`, st.alreadyConfirmed)
			sc.Step(`^the owner runs roger edge actuate ([^ ]+)$`, st.runs)
			sc.Step(`^the owner runs roger edge actuate on it$`, st.runsOnIt)
			sc.Step(`^the owner runs roger edge actuate on it again$`, st.runsAgain)
			sc.Step(`^the owner answers yes$`, st.answersYes)
			sc.Step(`^it says what confirming means: this node may change the physical world$`, st.saysWhatItMeans)
			sc.Step(`^it asks y/N before anything changes$`, st.asksFirst)
			sc.Step(`^answering no leaves the capability pending$`, st.noLeavesItPending)
			sc.Step(`^actuate is VERIFIED on that node$`, st.isVerified)
			sc.Step(`^the confirmation records who confirmed it and when$`, st.recordsWhoAndWhen)
			sc.Step(`^the node's history gains the confirmation$`, st.historyGainsIt)
			sc.Step(`^it is refused, saying the node does not declare actuate$`, st.refusedNotDeclared)
			sc.Step(`^nothing on the node changes$`, st.nothingChanged)
			sc.Step(`^it says it is already confirmed, and by whom$`, st.saysAlreadyConfirmed)
			sc.Step(`^the original confirmation's who and when are untouched$`, st.originalUntouched)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/node_identity.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the actuate ceremony scenarios failed")
	}
}
