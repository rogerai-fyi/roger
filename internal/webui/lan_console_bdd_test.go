package webui

// Executable spec: features/lan/ui_visibility.feature:97 and money_and_accounts.feature:109
// (@console). The REAL console Server answers /api/state for a controller in LAN mode, and the
// SHIPPED console-lan.js is EXECUTED with node over that very snapshot and a minimal document, so
// what the browser draws is what this proves. The shipped HTML and CSS are read for the rules the
// badge must keep (tokens only, never the brand red, no forced width on a phone).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/node"
)

type lanConsoleBDD struct {
	t        *testing.T
	snapshot string
	badge    string
	cost     string
	bands    []client.LANBand
	list     []map[string]any
	asked    bool // the recording broker saw a request
	baseURL  string
	token    string
}

// lanThreeBands are the merged-dial scenario's three sources, as the TUI's dial reads them.
func lanThreeBands() []client.LANBand {
	return []client.LANBand{
		{Model: "gpt-oss-20b", Sources: []client.LANSource{{Kind: client.LANEdge, ID: "jetson", Verified: true}}},
		{Model: "llama-3.3-70b", Sources: []client.LANSource{{Kind: client.LANTower, ID: "192.168.1.20:8787", Addr: "192.168.1.20:8787"}}},
		{Model: "qwen3-8b", Sources: []client.LANSource{{Kind: client.LANSelf, Verified: true}}},
	}
}

// runningWithSources stands up `roger --lan webui` with the LAN dial the TUI reads and a
// recording broker, so the band list is proven to come from the LAN and never the market.
func (s *lanConsoleBDD) runningWithSources() error {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.asked = true
		_, _ = w.Write([]byte(`[{"model":"market-band","node_id":"n1","online":true}]`))
	}))
	s.t.Cleanup(broker.Close)
	s.bands = lanThreeBands()
	c := testCtrl()
	c.SetLAN(func() []string { return nil })
	srv := New(c, Options{Broker: broker.URL, User: "u", LANDial: func() client.LANDial { return client.LANDial{Bands: s.bands} }})
	hs := httptest.NewServer(srv.Handler())
	s.t.Cleanup(hs.Close)
	s.baseURL, s.token = hs.URL, srv.Token()
	return nil
}

func (s *lanConsoleBDD) readBandList() error {
	resp, err := http.Get(s.baseURL + "/api/browse?t=" + s.token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("/api/browse answered %d: %s", resp.StatusCode, b)
	}
	s.list = nil
	return json.NewDecoder(resp.Body).Decode(&s.list)
}

func (s *lanConsoleBDD) sameAsTheDial() error {
	if s.asked {
		return fmt.Errorf("the console asked the broker for its band list in LAN mode")
	}
	if len(s.list) != len(s.bands) {
		return fmt.Errorf("the console lists %d bands, the dial %d: %v", len(s.list), len(s.bands), s.list)
	}
	for i, b := range s.bands {
		got := s.list[i]
		if got["model"] != b.Model || got["source"] != b.SourceLabel() || got["node_id"] != b.SourceLabel() {
			return fmt.Errorf("row %d is %v, the dial shows %q from %q", i, got, b.Model, b.SourceLabel())
		}
		if got["online"] != true || got["free_now"] != true {
			return fmt.Errorf("row %d is not an on-air free LAN band: %v", i, got)
		}
	}
	return nil
}

// running stands the console up for a controller in LAN mode, as `roger --lan webui` does.
func (s *lanConsoleBDD) running() error {
	c := testCtrl()
	c.SetLAN(func() []string { return nil })
	srv := New(c, Options{})
	hs := httptest.NewServer(srv.Handler())
	s.t.Cleanup(hs.Close)
	resp, err := http.Get(hs.URL + "/api/state?t=" + srv.Token())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s.snapshot = string(b)
	return nil
}

// renders runs the shipped console-lan.js over the snapshot with node: the badge it draws and the
// cost line it gives the SHARE totals.
func (s *lanConsoleBDD) renders() error {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is needed to execute the console's JavaScript: %v", err)
	}
	script := `
const api = require(process.argv[1]);
const snap = JSON.parse(process.argv[2]);
const badge = { hidden: true, textContent: "" };
api.renderLAN({ getElementById: (id) => (id === "top-lan" ? badge : null) }, snap);
process.stdout.write(JSON.stringify({ badge: badge.hidden ? "" : badge.textContent, cost: api.costLine(snap) }));
`
	out, err := exec.Command(nodeBin, "-e", script, "./assets/console-lan.js", s.snapshot).CombinedOutput()
	if err != nil {
		return fmt.Errorf("console-lan.js failed: %v: %s", err, out)
	}
	var r struct{ Badge, Cost string }
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("unexpected output %q: %v", out, err)
	}
	s.badge, s.cost = r.Badge, r.Cost
	return nil
}

func (s *lanConsoleBDD) topBarHas(want string) error {
	html, _ := os.ReadFile("assets/console.html")
	if !regexp.MustCompile(`<header class="topbar">[\s\S]*id="top-lan"[\s\S]*</header>`).Match(html) {
		return fmt.Errorf("the top bar has no LAN badge element")
	}
	if s.badge != want {
		return fmt.Errorf("the top bar badge reads %q, want %q (snapshot %s)", s.badge, want, s.snapshot)
	}
	return nil
}

// designChecks are the rules the badge keeps: colour from the design tokens only, never the brand
// red, and no fixed width that could push the bar past a phone's width.
func (s *lanConsoleBDD) designChecks() error {
	css, _ := os.ReadFile("assets/console.css")
	m := regexp.MustCompile(`(?s)\.lan-badge \{(.*?)\}`).FindSubmatch(css)
	if m == nil {
		return fmt.Errorf("console.css has no .lan-badge rule")
	}
	rule := string(m[1])
	if regexp.MustCompile(`#[0-9a-fA-F]{3,8}|rgb`).MatchString(rule) {
		return fmt.Errorf("the LAN badge uses a raw colour, not a token: %s", rule)
	}
	if strings.Contains(rule, "red") || strings.Contains(rule, "brand") {
		return fmt.Errorf("the LAN badge is drawn in the brand red: %s", rule)
	}
	if regexp.MustCompile(`(^|[^-])width\s*:`).MatchString(rule) {
		return fmt.Errorf("the LAN badge forces a width a phone may not have: %s", rule)
	}
	return nil
}

func TestLANConsoleBDD(t *testing.T) {
	s := &lanConsoleBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.snapshot, s.badge, s.cost = "", "", ""
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^"roger --lan webui" is running$`, s.running)
			sc.Step(`^"roger --lan webui" was run with the same three sources$`, s.runningWithSources)
			sc.Step(`^the console's band list is read$`, s.readBandList)
			sc.Step(`^it is the same three bands with the same sources the TUI dial shows$`, s.sameAsTheDial)
			sc.Step(`^the console renders$`, s.renders)
			sc.Step(`^the top bar contains "([^"]*)"$`, s.topBarHas)
			sc.Step(`^it passes the design-system checks and the phone-width check$`, s.designChecks)
			sc.Step(`^it does not say "([^"]*)"$`, func(no string) error {
				if strings.Contains(s.cost, no) {
					return fmt.Errorf("the cost line says %q", s.cost)
				}
				return nil
			})
			sc.Step(`^it says "([^"]*)"$`, func(want string) error {
				if s.cost != want {
					return fmt.Errorf("the cost line is %q, want %q", s.cost, want)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{
			"../../features/lan/ui_visibility.feature:97",
			"../../features/lan/money_and_accounts.feature:109",
			"../../features/lan/discovery_and_dial.feature:361",
		}},
	}
	if suite.Run() != 0 {
		t.Fatal("console LAN scenarios failed")
	}
}

var _ = node.Snapshot{}
