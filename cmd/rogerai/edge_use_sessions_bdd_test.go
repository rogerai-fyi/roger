package main

// Executable spec: features/edge/session_attribution.feature (@cli) - `roger use` records
// its turns as `roger use` sessions on this machine's Edge, through the shared mirror any
// Edge view on the machine merges. Isolated config dir; the real recorder and a real
// second ledger reading the mirror.

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

type useSessBDD struct {
	t    *testing.T
	rec  func(protocol.UsageReceipt)
	out  string
	err  error
	code int
}

func (s *useSessBDD) reset() { useTempConfig(s.t); s.rec, s.out, s.err, s.code = nil, "", nil, 0 }

func (s *useSessBDD) run(line string) error {
	args := strings.Fields(line)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("a command line starts with `roger`, got %q", line)
	}
	edgeStdin = strings.NewReader("")
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err, s.code = out, err, exitCode(err)
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

func (s *useSessBDD) printsOneRow(who, band, station, outcome string) error {
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, who) && strings.Contains(ln, band) && strings.Contains(ln, station) && strings.Contains(ln, outcome) {
			return nil
		}
	}
	return fmt.Errorf("no row with %q %q %q %q:\n%s", who, band, station, outcome, s.out)
}

func (s *useSessBDD) columns() error {
	for _, c := range []string{"WHO", "BAND", "PATH", "OUTCOME"} {
		if !strings.Contains(s.out, c) {
			return fmt.Errorf("no %s column:\n%s", c, s.out)
		}
	}
	return nil
}

func (s *useSessBDD) saysQuiet() error {
	if !strings.Contains(s.out, "quiet") || !strings.Contains(s.out, "90") {
		return fmt.Errorf("a quiet Edge is not said:\n%s", s.out)
	}
	return nil
}

func (s *useSessBDD) exits0() error {
	if s.code != 0 || s.err != nil {
		return fmt.Errorf("exit %d err %v", s.code, s.err)
	}
	return nil
}

func (s *useSessBDD) printsJSONRow() error {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.out)), &rows); err != nil {
		return fmt.Errorf("not a JSON list: %v\n%s", err, s.out)
	}
	if len(rows) != 1 {
		return fmt.Errorf("%d rows: %s", len(rows), s.out)
	}
	for _, k := range []string{"request", "who", "band", "station", "outcome", "count"} {
		if _, ok := rows[0][k]; !ok {
			return fmt.Errorf("row lacks %q: %s", k, s.out)
		}
	}
	return nil
}

func (s *useSessBDD) describes(purpose string) error {
	if !strings.Contains(s.out, purpose) {
		return fmt.Errorf("help does not describe %q:\n%s", purpose, s.out)
	}
	return nil
}

func (s *useSessBDD) usageError() error {
	if s.code != 2 {
		return fmt.Errorf("exit %d, want 2 (usage):\n%s", s.code, s.out)
	}
	return nil
}

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
			sc.Step(`^they run "([^"]*)"$`, st.run)
			sc.Step(`^it prints one session row attributed to "([^"]*)", band "([^"]*)", station "([^"]*)", outcome "([^"]*)"$`, st.printsOneRow)
			sc.Step(`^the columns are WHO, BAND, PATH and OUTCOME, as on the screens$`, st.columns)
			sc.Step(`^it says the Edge is quiet and names the 90 second window$`, st.saysQuiet)
			sc.Step(`^it exits 0$`, st.exits0)
			sc.Step(`^it prints a JSON list with one row carrying request, who, band, station, outcome and count$`, st.printsJSONRow)
			sc.Step(`^it describes "([^"]*)"$`, st.describes)
			sc.Step(`^it is a usage error$`, st.usageError)
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
