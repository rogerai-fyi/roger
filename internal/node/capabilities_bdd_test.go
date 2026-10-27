package node

// capabilities_bdd_test.go makes features/share/share_capabilities.feature EXECUTABLE: a
// capability detection found must survive the REAL Controller (LoadRows -> ShareRow ->
// agent.Config) onto the offer, on the path the TUI and the web console share through. The
// on-air step captures agent.Config at the startAgent process-edge seam (the same seam
// private_restore_test.go uses) - that Config is what agent.Start turns into the offer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
)

type shareCapsState struct {
	c     *Controller
	found []detect.Found
	cfgs  map[string]agent.Config
}

func capsList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func (s *shareCapsState) controller(station string) error {
	s.c = New(Config{Broker: "http://broker.local", Station: station})
	s.cfgs = map[string]agent.Config{}
	return nil
}

func (s *shareCapsState) detected(model, url, caps string) error {
	s.found = append(s.found, detect.Found{
		Name: "srv", BaseURL: url + "/v1", Chat: url + "/v1/chat/completions",
		Models:       []string{model},
		Capabilities: map[string][]string{model: capsList(caps)},
	})
	return nil
}
func (s *shareCapsState) detectedNone(model, url string) error { return s.detected(model, url, "") }

func (s *shareCapsState) load() error { s.c.LoadRowsNoPersist(s.found); return nil }

func (s *shareCapsState) onAir(model string) error {
	real := startAgent
	defer func() { startAgent = real }()
	startAgent = func(cfg agent.Config) (*agent.Session, error) {
		s.cfgs[cfg.Model] = cfg
		return nil, errors.New("captured at the process edge")
	}
	s.c.ToggleOnAir(model)
	if _, ok := s.cfgs[model]; !ok {
		return fmt.Errorf("putting %q on air never reached the agent", model)
	}
	return nil
}

func (s *shareCapsState) overwrite(model, cap string) error {
	for _, f := range s.found {
		if c := f.Capabilities[model]; len(c) > 0 {
			c[0] = cap
		}
	}
	return nil
}

func wantCaps(what string, got []string, want string) error {
	if g := strings.Join(got, ","); g != want {
		return fmt.Errorf("%s: expected capabilities %q, got %q", what, want, g)
	}
	return nil
}

func (s *shareCapsState) offerHas(model, caps string) error {
	return wantCaps("on-air offer "+model, s.cfgs[model].Capabilities, caps)
}
func (s *shareCapsState) offerNone(model string) error { return s.offerHas(model, "") }

func (s *shareCapsState) row(model string) (ShareRow, int) {
	var r ShareRow
	n := 0
	for _, x := range s.c.Rows() {
		if x.Model == model {
			r, n = x, n+1
		}
	}
	return r, n
}
func (s *shareCapsState) rowHas(model, caps string) error {
	r, _ := s.row(model)
	return wantCaps("row "+model, r.Capabilities, caps)
}
func (s *shareCapsState) rowNone(model string) error { return s.rowHas(model, "") }
func (s *shareCapsState) oneRow(model string) error {
	if _, n := s.row(model); n != 1 {
		return fmt.Errorf("expected exactly one row for %q, got %d", model, n)
	}
	return nil
}

func TestShareCapabilitiesBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &shareCapsState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = shareCapsState{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share controller for station `+q+`$`, st.controller)
			sc.Step(`^detection found model `+q+` on `+q+` with capabilities `+q+`$`, st.detected)
			sc.Step(`^detection found model `+q+` on `+q+` with no capabilities$`, st.detectedNone)
			sc.Step(`^the controller loads the detected rows$`, st.load)
			sc.Step(`^the operator puts `+q+` on air$`, st.onAir)
			sc.Step(`^the detector's capability list for `+q+` is later overwritten with `+q+`$`, st.overwrite)
			sc.Step(`^the on-air offer for `+q+` has capabilities `+q+`$`, st.offerHas)
			sc.Step(`^the on-air offer for `+q+` has no capabilities$`, st.offerNone)
			sc.Step(`^the row for `+q+` has capabilities `+q+`$`, st.rowHas)
			sc.Step(`^the row for `+q+` has no capabilities$`, st.rowNone)
			sc.Step(`^there is exactly one row for `+q+`$`, st.oneRow)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/share/share_capabilities.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("share/share_capabilities behavior scenarios failed (see godog output above)")
	}
}
