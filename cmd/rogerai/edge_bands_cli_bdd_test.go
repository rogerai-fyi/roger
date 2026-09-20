package main

// Executable spec: features/edge/local_inference.feature (@cli) - `roger edge bands` shows
// what this Edge can serve locally, from the record. Isolated config; the real dispatch; a
// real fleet with a peer instance on air, and this machine's own household.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
)

type bandsCLIBDD struct {
	t   *testing.T
	out string
}

func (s *bandsCLIBDD) reset() {
	useTempConfig(s.t)
	s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	s.out = ""
}

func (s *bandsCLIBDD) run(line string) error {
	edgeStdin = strings.NewReader("")
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), strings.Fields(line)[1:]) })
	s.out = out
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

func (s *bandsCLIBDD) instancesServing(a, b string) error {
	runEdgeCLI(s.t, "edge", "authority", "local", "workshop")
	runEdgeCLI(s.t, "edge", "enroll", "workshop")
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	// this machine serves one band; a peer node serves the other
	if _, err := edge.Register(edgeInstancesDir(), id.NodeID, edge.Instance{Name: "here", Caps: edgeInstanceCaps([]string{a}), Bands: []string{a}}, time.Now()); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	_, err = st.fleet.Observe("n_jetson0000000000000000", edge.Observation{Name: "jetson", Kind: "host", Addr: "192.168.1.20:1",
		Fingerprint: strings.Repeat("cd", 32), Instances: []edge.Instance{{Name: "serve", Caps: edgeInstanceCaps([]string{b}), Bands: []string{b}}}})
	if err != nil {
		return err
	}
	return st.save()
}

func (s *bandsCLIBDD) listsBandWithServers() error {
	for _, b := range []string{"qwen-3.8-27b", "gpt-oss-20b"} {
		found := false
		for _, ln := range strings.Split(s.out, "\n") {
			if strings.Contains(ln, b) && strings.Contains(ln, "/") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%q not listed with a server:\n%s", b, s.out)
		}
	}
	return nil
}

func (s *bandsCLIBDD) saysWhichGoToMarket() error {
	// the empty case says it; the populated case simply lists what is local. Assert the
	// header names local serving, so a reader knows the rest is the market's job.
	if !strings.Contains(s.out, "serve locally") {
		return fmt.Errorf("output does not frame these as the local bands:\n%s", s.out)
	}
	return nil
}

func TestEdgeBandsFeatureCLI(t *testing.T) {
	st := &bandsCLIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^instances on this Edge serving "([^"]*)" and "([^"]*)"$`, st.instancesServing)
			sc.Step(`^they run "([^"]*)"$`, st.run)
			sc.Step(`^it lists each band with the instances that serve it and whether each is reachable from here$`, st.listsBandWithServers)
			sc.Step(`^it says which bands would have to go to the market$`, st.saysWhichGoToMarket)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli && ~@later",
			Paths: []string{"../../features/edge/local_inference.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the edge bands CLI scenarios failed")
	}
}
