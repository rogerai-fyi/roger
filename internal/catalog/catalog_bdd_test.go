package catalog

// Makes features/share/model_catalog.feature EXECUTABLE, per the repo's
// spec-first workflow, for the half this package can honestly enforce today.
//
// Only @data scenarios run. The rest of the feature describes acquisition and
// serving surfaces (internal/provision, internal/runtime) that do not exist yet,
// and binding them to stubs would mark unbuilt behaviour as covered - the same
// dishonesty this release spent its time removing. They stay unbound and visible
// until their packages land.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type catState struct {
	detected []string
	entries  []Entry
	list     []Shareable

	availableBytes int64
	needBytes      int64
	fit            Fit
}

func (s *catState) reset() { *s = catState{} }

func (s *catState) openShare() error { s.reset(); return nil }

func (s *catState) localServerServing(id string) error {
	s.detected = append(s.detected, id)
	return nil
}

func (s *catState) catalogueOffersAbsentModel() error {
	e := Entry{
		ID: "wave-micro", Repo: "https://huggingface.co/rogerai-fyi/wave-micro",
		File: "wave-micro.gguf", Bytes: 1 << 28, SHA256: strings.Repeat("b", 64),
		ServeMem: 1 << 29, License: "Apache-2.0", Runtime: "llama.cpp",
	}
	if err := e.Validate(); err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	return nil
}

func (s *catState) build() { s.list = Merge(s.detected, s.entries) }

func (s *catState) bothAppear() error {
	s.build()
	if len(s.list) != len(s.detected)+len(s.entries) {
		return fmt.Errorf("list has %d rows, want %d", len(s.list), len(s.detected)+len(s.entries))
	}
	return nil
}

func (s *catState) rowInState(want State) (Shareable, error) {
	for _, r := range s.list {
		if r.State == want {
			return r, nil
		}
	}
	return Shareable{}, fmt.Errorf("no row in state %v", want)
}

func (s *catState) detectedIsReady() error {
	r, err := s.rowInState(StateDetected)
	if err != nil {
		return err
	}
	if !r.ReadyToBroadcast() {
		return fmt.Errorf("detected row %q is not ready to broadcast", r.ID)
	}
	return nil
}

func (s *catState) offerableIsNotInstalled() error {
	r, err := s.rowInState(StateOffered)
	if err != nil {
		return err
	}
	if r.ReadyToBroadcast() {
		return fmt.Errorf("offered row %q claims it can broadcast", r.ID)
	}
	return nil
}

// The two states must be carried by DATA the renderer can key on, not by a colour
// the renderer happens to choose - otherwise the distinction dies in a mono theme.
func (s *catState) statesDistinguishableWithoutColour() error {
	ready, err := s.rowInState(StateDetected)
	if err != nil {
		return err
	}
	offered, err := s.rowInState(StateOffered)
	if err != nil {
		return err
	}
	if ready.State == offered.State || ready.ReadyToBroadcast() == offered.ReadyToBroadcast() {
		return fmt.Errorf("the two rows are not distinguishable from their data alone")
	}
	return nil
}

func (s *catState) selectsOfferable() error {
	if err := s.catalogueOffersAbsentModel(); err != nil {
		return err
	}
	s.build()
	return nil
}

func (s *catState) notRegisteredWithBroker() error { return s.offerableIsNotInstalled() }

func (s *catState) offeredAcquisitionInstead() error {
	r, err := s.rowInState(StateOffered)
	if err != nil {
		return err
	}
	// Acquisition is only describable when the row carries what a consent prompt
	// needs: a source and a byte count.
	if r.Entry == nil || r.Entry.Bytes <= 0 || r.Entry.Repo == "" {
		return fmt.Errorf("offered row %q cannot describe an acquisition", r.ID)
	}
	return nil
}

// "32 GB" -> bytes. Kept strict: an unparseable size is a broken scenario, not a zero.
func parseSize(s string) (int64, error) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) != 2 {
		return 0, fmt.Errorf("cannot parse size %q", s)
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return 0, err
	}
	switch strings.ToUpper(f[1]) {
	case "GB":
		return n << 30, nil
	case "MB":
		return n << 20, nil
	}
	return 0, fmt.Errorf("unknown unit in %q", s)
}

func (s *catState) machineHasMemory(size string) error {
	n, err := parseSize(size)
	s.availableBytes = n
	return err
}

func (s *catState) modelNeedsMemory(size string) error {
	n, err := parseSize(size)
	s.needBytes = n
	return err
}

func (s *catState) entryMarked(verdict string) error {
	s.fit = AssessFit(s.availableBytes, s.needBytes)
	if got := s.fit.String(); got != verdict {
		return fmt.Errorf("fit is %q, want %q", got, verdict)
	}
	return nil
}

func TestModelCatalogueDataScenarios(t *testing.T) {
	st := &catState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an operator opens the SHARE screen$`, st.openShare)
			sc.Step(`^a local server is already serving "([^"]*)"$`, st.localServerServing)
			sc.Step(`^the catalogue offers a model that is not on this machine$`, st.catalogueOffersAbsentModel)
			sc.Step(`^both appear in the SHARE model list$`, st.bothAppear)
			sc.Step(`^the detected model is shown as ready to go on air$`, st.detectedIsReady)
			sc.Step(`^the offerable model is shown as not installed$`, st.offerableIsNotInstalled)
			sc.Step(`^the two states are distinguishable without colour alone$`, st.statesDistinguishableWithoutColour)
			sc.Step(`^the operator selects an offerable model$`, st.selectsOfferable)
			sc.Step(`^RogerAI does not register it with the broker$`, st.notRegisteredWithBroker)
			sc.Step(`^the operator is offered acquisition instead of a broadcast$`, st.offeredAcquisitionInstead)
			sc.Step(`^the machine has (.+) of usable memory$`, st.machineHasMemory)
			sc.Step(`^an offerable model needs (.+) to serve$`, st.modelNeedsMemory)
			sc.Step(`^the entry is marked "([^"]*)"$`, st.entryMarked)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/share/model_catalog.feature"},
			Tags:     "@data",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("model catalogue @data scenarios failed (see godog output above)")
	}
}
