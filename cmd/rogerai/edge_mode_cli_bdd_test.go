package main

// Executable spec: features/edge/mode.feature (@cli) - the root on the fleet listing, the
// route on `roger edge sessions`, and `roger edge prefer`. Isolated config; real dispatch;
// a real mirrored ledger fed real receipts for the sessions.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
)

type modeCLIBDD struct {
	t    *testing.T
	out  string
	err  error
	code int
}

func (s *modeCLIBDD) reset() {
	useTempConfig(s.t)
	s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	s.out, s.err, s.code = "", nil, 0
}

func (s *modeCLIBDD) run(line string) error {
	args := strings.Fields(line)
	edgeStdin = strings.NewReader("")
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err, s.code = out, err, exitCode(err)
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

func (s *modeCLIBDD) rootedLocal(where string) error {
	if err := s.run("roger edge authority local " + where); err != nil || s.err != nil {
		return fmt.Errorf("%v %v", err, s.err)
	}
	if err := s.run("roger edge enroll " + where); err != nil || s.err != nil {
		return fmt.Errorf("%v %v", err, s.err)
	}
	return nil
}

func (s *modeCLIBDD) statesRootLocal(where string) error {
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.HasPrefix(ln, "EDGE ·") && strings.Contains(ln, "LOCAL ROOT") && strings.Contains(ln, where) {
			return nil
		}
	}
	return fmt.Errorf("no root line naming LOCAL and %q:\n%s", where, s.out)
}

func (s *modeCLIBDD) localAndMarketTurn() error {
	led := edge.NewSessions(edgeAccount())
	led.Mirror(edgeSessionsDir())
	if _, err := led.Record(edge.Traffic{Account: edgeAccount(), Kind: edge.FromUse, Request: "l1",
		Receipts: []protocol.UsageReceipt{{RequestID: "l1", NodeID: "jetson", Instance: "serve", Model: "gpt-oss-20b", Local: true}}}); err != nil {
		return err
	}
	_, err := led.Record(edge.Traffic{Account: edgeAccount(), Kind: edge.FromUse, Request: "m1",
		Receipts: []protocol.UsageReceipt{{RequestID: "m1", NodeID: "house-or-1", Model: "gpt-oss-120b"}}})
	return err
}

func (s *modeCLIBDD) eachRowSaysRoute() error {
	local, market := false, false
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, "gpt-oss-20b") && strings.Contains(ln, " local ") {
			local = true
		}
		if strings.Contains(ln, "gpt-oss-120b") && strings.Contains(ln, " market ") {
			market = true
		}
	}
	if !local || !market {
		return fmt.Errorf("rows lack their route:\n%s", s.out)
	}
	return nil
}

func (s *modeCLIBDD) eachRowCarriesRouteField() error {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.out)), &rows); err != nil {
		return fmt.Errorf("not JSON: %v\n%s", err, s.out)
	}
	if len(rows) != 2 {
		return fmt.Errorf("%d rows", len(rows))
	}
	for _, r := range rows {
		if r["route"] != "local" && r["route"] != "market" {
			return fmt.Errorf("row route = %v", r["route"])
		}
	}
	return nil
}

func (s *modeCLIBDD) preferenceIs(want string) error {
	if got := loadConfig().EdgePrefer; got != want {
		return fmt.Errorf("prefer = %q, want %q", got, want)
	}
	return nil
}

func (s *modeCLIBDD) preferAloneReports(want string) error {
	if err := s.run("roger edge prefer"); err != nil {
		return err
	}
	if !strings.HasPrefix(s.out, want) || !strings.Contains(s.out, edge.PreferMeaning(want)) {
		return fmt.Errorf("out = %q", s.out)
	}
	return nil
}

func (s *modeCLIBDD) usageErrorNamingThree() error {
	if s.code != 2 || !strings.Contains(s.out, "local-only") || !strings.Contains(s.out, "market") {
		return fmt.Errorf("exit %d:\n%s", s.code, s.out)
	}
	return nil
}

func (s *modeCLIBDD) preferenceUnchanged() error {
	if loadConfig().EdgePrefer != "" {
		return fmt.Errorf("prefer changed to %q", loadConfig().EdgePrefer)
	}
	return nil
}

func TestModeFeatureCLI(t *testing.T) {
	st := &modeCLIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^an Edge rooted at the designated machine "([^"]*)"$`, st.rootedLocal)
			sc.Step(`^they run "([^"]*)"$`, st.run)
			sc.Step(`^the output states the root is LOCAL and names "([^"]*)"$`, st.statesRootLocal)
			sc.Step(`^a local turn and a market turn$`, st.localAndMarketTurn)
			sc.Step(`^each row says its route$`, st.eachRowSaysRoute)
			sc.Step(`^each row carries a route field reading local or market$`, st.eachRowCarriesRouteField)
			sc.Step(`^the preference is "([^"]*)"$`, st.preferenceIs)
			sc.Step(`^"roger edge prefer" alone reports "([^"]*)" and what it means$`, st.preferAloneReports)
			sc.Step(`^it is a usage error naming the three values$`, st.usageErrorNamingThree)
			sc.Step(`^the preference is unchanged$`, st.preferenceUnchanged)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/mode.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the mode CLI scenarios failed")
	}
}
