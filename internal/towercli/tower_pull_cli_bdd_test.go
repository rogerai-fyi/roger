package towercli

// The operator half of features/tower/tower_pull_dispatch.feature (serve output, status and the
// design documents) and of features/tower/private_hub_placement.feature (what serve says about
// who a hub can serve). The broker half runs in cmd/rogerai-broker.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
)

type tpcState struct {
	t   *testing.T
	out string
	err error
}

// serveOut runs the joined serve's own flag handling and returns what it printed before it
// would start serving. A standalone directory is refused right after those lines, so nothing
// listens and nothing dials.
func (s *tpcState) serveOut(args ...string) {
	dir := filepath.Join(s.t.TempDir(), "tw")
	if err := run([]string{"init", "--dir", dir, "--mode", "standalone"}, &strings.Builder{}); err != nil {
		s.err = err
		return
	}
	s.out, s.err = serveJoinedCLI(s.t, append([]string{"--dir", dir}, args...)...)
}

func (s *tpcState) serveNoRelayPublic() error {
	prev := outboundIPFn
	outboundIPFn = func() (string, error) { return "192.168.1.20", nil }
	defer func() { outboundIPFn = prev }()
	s.serveOut("--hub", ":8444")
	if !strings.Contains(s.err.Error(), "standalone") {
		return fmt.Errorf("serve ended with %v", s.err)
	}
	return nil
}

func (s *tpcState) doesNotSayCarriesNothing() error {
	for _, bad := range []string{"will not route", "carries nothing", "carrying nothing"} {
		if strings.Contains(s.out, bad) {
			return fmt.Errorf("serve still says %q:\n%s", bad, s.out)
		}
	}
	return nil
}

func (s *tpcState) saysThisNetwork() error {
	for _, want := range []string{"Stations on this network will be placed here",
		"Stations elsewhere need a public address for now"} {
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("serve does not say %q:\n%s", want, s.out)
		}
	}
	return nil
}

func (s *tpcState) statusRuns() error {
	core := newCoreStub(s.t)
	dir := joinedRegisteredTower(s.t)
	core.reply["/tower/status"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"towers":[{"tower_id":"tw-1","state":"active","link_live":true,
			"carries_traffic":true,"work":"pulled over the link","hub_reach":"this network only"},
			{"tower_id":"tw-1","state":"active","link_live":true,"carries_traffic":true,
			"work":"pulled over the link","hub_reach":"public, at 203.0.113.7:8444"}]}`))
		return true
	}
	s.out, s.err = runCLI(s.t, "status", "--dir", dir)
	return s.err
}

func (s *tpcState) showsWork(want string) error {
	if !strings.Contains(strings.Join(strings.Fields(s.out), " "), want) {
		return fmt.Errorf("status does not show %q:\n%s", want, s.out)
	}
	return nil
}

func (s *tpcState) showsReach(a, b string) error {
	for _, want := range []string{a, strings.Replace(b, "<address>", "203.0.113.7:8444", 1)} {
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("status does not show %q:\n%s", want, s.out)
		}
	}
	return nil
}

func readDoc(name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
	return string(raw), err
}

func (s *tpcState) docsSayPull() error {
	for _, name := range []string{"tower-architecture.md", "tower-relay-link-design.md"} {
		doc, err := readDoc(name)
		if err != nil {
			return err
		}
		if !strings.Contains(doc, "pull") || !strings.Contains(doc, "tower_pull_dispatch.feature") {
			return fmt.Errorf("%s does not say work reaches a Tower by pull", name)
		}
	}
	return nil
}

func (s *tpcState) neitherRequiresPublic() error {
	for _, name := range []string{"tower-architecture.md", "tower-relay-link-design.md"} {
		doc, err := readDoc(name)
		if err != nil {
			return err
		}
		for _, bad := range []string{"a Tower must have a public address", "Its requirements are a stable address",
			"requires a public address", "a public address is required"} {
			if strings.Contains(doc, bad) {
				return fmt.Errorf("%s says %q", name, bad)
			}
		}
	}
	return nil
}

// --- private_hub_placement.feature: what serve says --------------------------------------

func (s *tpcState) serveLoopback() error {
	s.serveOut("--hub", ":8444", "--relay-public", "127.0.0.1:8444")
	return nil
}

func (s *tpcState) tellsAdvertiseLAN() error {
	if !strings.Contains(s.out, "Advertise this machine's LAN address instead") {
		return fmt.Errorf("serve does not say to advertise the LAN address:\n%s", s.out)
	}
	return nil
}

func (s *tpcState) serveNoHub() error {
	s.serveOut()
	return nil
}

func (s *tpcState) saysHubNeeded() error {
	if s.out != "" && strings.Contains(s.out, "Stations on this network") {
		return fmt.Errorf("serve without a hub promises Stations:\n%s", s.out)
	}
	return nil
}

func (s *tpcState) servePrivateNoTLS() error {
	s.serveOut("--hub", ":8444", "--relay-public", "192.168.1.20:8444")
	return nil
}

func (s *tpcState) tellsNeedsTLS() error {
	if !strings.Contains(s.out, "a private hub needs --hub-tls") {
		return fmt.Errorf("serve does not say a private hub needs TLS:\n%s", s.out)
	}
	return nil
}

func (s *tpcState) servePrivateTLS() error {
	s.serveOut("--hub", ":8444", "--hub-tls", "--relay-public", "192.168.1.20:8444")
	return nil
}

func (s *tpcState) saysElsewhereNeedsPublic() error {
	return s.saysThisNetwork()
}

func TestTowerPullCLIBDD(t *testing.T) {
	// Name resolution is never needed: every address in these scenarios is a literal.
	prevLookup := lookupIPFn
	lookupIPFn = func(context.Context, string, string) ([]net.IP, error) { return nil, fmt.Errorf("no DNS in this test") }
	t.Cleanup(func() { lookupIPFn = prevLookup })
	s := &tpcState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = tpcState{t: t}
				return ctx, nil
			})
			// The Backgrounds are the broker's; nothing here depends on them.
			sc.Step(`^Roger Core runs two instances sharing one store$`, func() error { return nil })
			sc.Step(`^a registered Tower with a live link session and a hub serving one attached Station$`, func() error { return nil })
			sc.Step(`^a Tower whose session carries the "pull" capability$`, func() error { return nil })

			sc.Step("^an operator runs `roger tower serve --hub :8444` with no --relay-public$", s.serveNoRelayPublic)
			sc.Step(`^it does not print that Roger Core will not route anyone to this Tower$`, s.doesNotSayCarriesNothing)
			sc.Step(`^it prints that Stations on this network will be placed here and remote Stations need a public address for now$`, s.saysThisNetwork)
			sc.Step("^an operator runs `roger tower status`$", s.statusRuns)
			sc.Step(`^it shows "([^"]+)"$`, s.showsWork)
			sc.Step(`^it shows who the hub can serve: "([^"]+)" or "([^"]+)"$`, s.showsReach)
			sc.Step(`^docs/tower-architecture.md and docs/tower-relay-link-design.md say jobs reach a Tower by pull over its link$`, s.docsSayPull)
			sc.Step(`^neither says a Tower must have a public address to carry traffic$`, s.neitherRequiresPublic)

			sc.Step(`^the Tower advertises a loopback hub address$`, s.serveLoopback)
			sc.Step(`^no Station is placed on it, because Core cannot tell the same machine from the same network$`, func() error { return nil })
			sc.Step("^`roger tower serve` tells the operator to advertise the machine's LAN address instead$", s.tellsAdvertiseLAN)
			sc.Step(`^the Tower advertises no hub address$`, s.serveNoHub)
			sc.Step("^no Station is placed on it, and `roger tower serve` says the hub must be running to carry Stations$", s.saysHubNeeded)
			sc.Step(`^a private-hub Tower that serves its hub without TLS$`, s.servePrivateNoTLS)
			sc.Step(`^no Station is placed on it$`, func() error { return nil })
			sc.Step("^`roger tower serve` tells the operator a private hub needs --hub-tls$", s.tellsNeedsTLS)
			sc.Step("^an operator runs `roger tower serve --hub :8444 --hub-tls --relay-public 192.168.1.20:8444`$", s.servePrivateTLS)
			sc.Step(`^it prints that Stations on this network will be placed here$`, s.saysThisNetwork)
			sc.Step(`^that Stations elsewhere need a public address for now$`, s.saysElsewhereNeedsPublic)
			sc.Step(`^it does not say the Tower carries nothing$`, s.doesNotSayCarriesNothing)
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: append(bddrun.Scenarios("../../features/tower/tower_pull_dispatch.feature", bddrun.Titled(false, "The serve command no longer says a Tower with no public address carries nothing", "Tower status says how work reaches the Tower", "The design documents describe what the code does")),
			bddrun.Scenarios("../../features/tower/private_hub_placement.feature", bddrun.Titled(false, "A loopback hub is never handed to any Station", "A Tower with no hub address at all carries no Stations", "A private hub is handed out only with a TLS pin", "Serve says plainly who this Tower can serve"))...)},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("tower_pull_dispatch.feature (operator half) failed")
	}
}
