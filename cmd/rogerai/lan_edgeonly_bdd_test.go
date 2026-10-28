package main

// Executable spec: features/lan/edge_only.feature (@cli, @client, and the @tui @console SHARE
// scenario). `roger --lan --edge-only`: Edge members and this machine, never a Tower. The real
// run() path, the REAL loopback proxy built with the CLI's own ladder, REAL standalone Towers
// (localplane over a real tower.State) reached only through a dial map that records every
// connection attempted, REAL Edge member faces over mTLS, and a recording broker. "Never dialed"
// is read off the dial map and the Tower's own handler, so it fails the moment anything tries.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge/wire"
	"rogerai.fm/roger/v7/internal/harness"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/node"
	"rogerai.fm/roger/v7/internal/tui"
	"rogerai.fm/roger/v7/internal/webui"
)

const edgeOnlyFeature = "../../features/lan/edge_only.feature"

func edgeOnlyPaths(lines ...int) []string {
	var out []string
	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s:%d", edgeOnlyFeature, l))
	}
	return out
}

// ---- section 1 and the one-line note: the mode decided by run() ------------------------------

type edgeOnlyLaunchBDD struct {
	t       *testing.T
	out     string
	note    bytes.Buffer
	err     error
	decided *lanmode.Mode
	extra   []string // flags a scenario's setup puts on the command line
}

func (s *edgeOnlyLaunchBDD) reset() {
	useTempConfig(s.t)
	s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	os.Unsetenv("ROGER_LAN")
	os.Unsetenv("ROGER_LAN_EDGE_ONLY")
	s.out, s.err, s.decided, s.extra = "", nil, nil, nil
	s.note.Reset()
	lanNoteOut = &s.note
	runDispatch = func(cfg config, args []string) error {
		m := cfg.lan
		s.decided = &m
		switch args[0] {
		case "status", "config", "lan":
			return dispatch(cfg, args)
		}
		return nil // any other command would have dialed; the runner stops here
	}
}

func (s *edgeOnlyLaunchBDD) restore() {
	runDispatch, lanNoteOut = dispatch, os.Stderr
	tui.SetLANDial(nil)
	client.SetLANRelay(nil)
}

func (s *edgeOnlyLaunchBDD) run(line string) error {
	args := append(append([]string(nil), s.extra...), strings.Fields(line)[1:]...)
	s.decided = nil
	s.out, s.err = captureEdgeStdout(func() error { return run(args, loadConfig()) })
	return nil
}

func (s *edgeOnlyLaunchBDD) mode(lan, edgeOnly bool, src lanmode.Source) error {
	if s.err != nil {
		return fmt.Errorf("the command failed: %v", s.err)
	}
	if s.decided == nil {
		return fmt.Errorf("run() never reached dispatch")
	}
	if s.decided.LAN != lan || s.decided.EdgeOnly != edgeOnly {
		return fmt.Errorf("decided lan=%v edge-only=%v, want lan=%v edge-only=%v", s.decided.LAN, s.decided.EdgeOnly, lan, edgeOnly)
	}
	if src != "" && s.decided.Source != src {
		return fmt.Errorf("decided from %q, want %q", s.decided.Source, src)
	}
	return nil
}

func (s *edgeOnlyLaunchBDD) configMode(v string) error {
	c := loadConfig()
	c.Mode = v
	return saveConfig(c)
}

func (s *edgeOnlyLaunchBDD) exitsNaming(parts ...string) error {
	if s.err == nil {
		return fmt.Errorf("the command succeeded:\n%s", s.out)
	}
	for _, p := range parts {
		if !strings.Contains(s.err.Error(), p) {
			return fmt.Errorf("the error %q does not name %q", s.err, p)
		}
	}
	if s.decided != nil {
		return fmt.Errorf("the command still dispatched")
	}
	return nil
}

func TestLANEdgeOnlyLaunchBDD(t *testing.T) {
	s := &edgeOnlyLaunchBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restore()
				return ctx, nil
			})
			sc.Step(`^they run "([^"]*)"$`, s.run)
			sc.Step(`^then "([^"]*)"$`, s.run)
			sc.Step(`^the command runs in LAN mode, edge-only$`, func() error { return s.mode(true, true, "") })
			sc.Step(`^the command runs in LAN mode, edge-only, from source "([^"]*)"$`, func(src string) error {
				return s.mode(true, true, lanmode.Source(src))
			})
			sc.Step(`^the command runs in LAN mode, not edge-only, from source "([^"]*)"$`, func(src string) error {
				return s.mode(true, false, lanmode.Source(src))
			})
			sc.Step(`^the command runs in the default mode$`, func() error { return s.mode(false, false, "") })
			sc.Step(`^the command line carries --edge-only$`, func() error { s.extra = []string{"--edge-only"}; return nil })
			sc.Step(`^ROGER_LAN_EDGE_ONLY is set to "([^"]*)"$`, func(v string) error { t.Setenv("ROGER_LAN_EDGE_ONLY", v); return nil })
			sc.Step(`^config "mode" is "([^"]*)"$`, s.configMode)
			sc.Step(`^it exits non-zero saying --edge-only and --no-lan conflict$`, func() error {
				return s.exitsNaming("--edge-only", "--no-lan", "conflict")
			})
			sc.Step(`^it exits non-zero naming ROGER_LAN_EDGE_ONLY and the accepted values$`, func() error {
				return s.exitsNaming("ROGER_LAN_EDGE_ONLY", "1", "0")
			})
			sc.Step(`^it reports LAN mode, edge-only, from config$`, func() error {
				if !strings.Contains(s.out, "LAN mode: on (edge-only") || !strings.Contains(s.out, "from config") {
					return fmt.Errorf("status does not report LAN mode, edge-only, from config:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the next "([^"]*)" reports the default mode$`, func(line string) error {
				if err := s.run(line); err != nil {
					return err
				}
				if !strings.Contains(s.out, "mode: default") {
					return fmt.Errorf("status does not report the default mode:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^edge-only is on$`, func() error { t.Setenv("ROGER_LAN_EDGE_ONLY", "1"); return nil })
			sc.Step(`^it reports "([^"]*)" and its source$`, func(want string) error {
				if !strings.Contains(s.out, want) || !strings.Contains(s.out, "from env") {
					return fmt.Errorf("status lacks %q and its source:\n%s", want, s.out)
				}
				return nil
			})
			sc.Step(`^it says LAN Towers are not used$`, func() error {
				if !strings.Contains(s.out, "LAN Towers are not used") {
					return fmt.Errorf("status does not say LAN Towers are not used:\n%s", s.out)
				}
				return nil
			})
			sc.Step("^`roger status --json` carries \"lan\": true and \"edge_only\": true$", func() error {
				if err := s.run("roger status --json"); err != nil {
					return err
				}
				var j map[string]any
				if err := json.Unmarshal([]byte(s.out), &j); err != nil {
					return fmt.Errorf("status --json is not JSON: %v\n%s", err, s.out)
				}
				if j["lan"] != true || j["edge_only"] != true {
					return fmt.Errorf("status --json: lan=%v edge_only=%v", j["lan"], j["edge_only"])
				}
				return nil
			})
			sc.Step(`^stderr names LAN mode, edge-only, once$`, func() error {
				got := s.note.String()
				if strings.Count(got, "LAN mode") != 1 || !strings.Contains(got, "edge-only") {
					return fmt.Errorf("stderr: %q", got)
				}
				if strings.Contains(s.out, "LAN mode") {
					return fmt.Errorf("the note went to stdout: %q", s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: edgeOnlyPaths(34, 42, 56, 63, 69, 75, 80, 91, 99, 224)},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (launch) failed")
	}
}

// ---- sections 2, 4 (trust note) and 5: Towers absent everywhere -------------------------------

type edgeOnlyBDD struct {
	lanFrontBDD
	use      *lanUseBDD
	browsed  atomic.Int32
	lastLine string
}

func (s *edgeOnlyBDD) reset() {
	s.resetFront()
	s.edgeOnly = true
	s.t.Setenv("ROGER_LAN_EDGE_ONLY", "1")
	s.browsed.Store(0)
	s.lastLine = ""
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) {
		s.browsed.Add(1)
		return wire.BrowseResult{Adverts: s.adverts, Seen: len(s.adverts)}, nil
	}
}

// untouched is "no request of any kind was made" to a scenario address: no connection was even
// attempted through the dial map, and the Tower behind it (when there is one) saw nothing.
func (s *edgeOnlyBDD) untouched(addr string) error {
	if s.dial.wasDialed(addr) {
		return fmt.Errorf("a connection was attempted to %s: %v", addr, s.dial.dialed)
	}
	if tw, ok := s.towers[addr]; ok {
		tw.mu.Lock()
		n := tw.chatRequests
		tw.mu.Unlock()
		if n != 0 {
			return fmt.Errorf("the Tower at %s received %d completion(s)", addr, n)
		}
	}
	return nil
}

// cliRun runs a roger command line with the real CLI, holding no port (client.SetUseServe).
func (s *edgeOnlyBDD) cliRun(line string) error {
	restore := client.SetUseServe(func(ln net.Listener, h http.Handler) error {
		s.handler = h
		return ln.Close()
	})
	defer restore()
	s.cliOut, s.cliErr = captureEdgeStdout(func() error { return run(strings.Fields(line)[1:], loadConfig()) })
	return nil
}

func (s *edgeOnlyBDD) refusedEdgeOnly(parts ...string) error {
	text := s.body
	if s.cliErr != nil {
		text += " " + s.cliErr.Error()
	}
	text += " " + s.cliOut
	if s.code != 0 && s.code != http.StatusServiceUnavailable {
		return fmt.Errorf("status %d, want a refusal: %s", s.code, s.body)
	}
	if s.code == 0 && s.cliErr == nil {
		return fmt.Errorf("nothing was refused: %s", text)
	}
	for _, p := range parts {
		if !strings.Contains(text, p) {
			return fmt.Errorf("the refusal lacks %q: %s", p, text)
		}
	}
	return nil
}

// sendFrom sends one turn for band from a named front end, recording what came back.
func (s *edgeOnlyBDD) sendFrom(band, path string) error {
	s.code, s.body, s.reply, s.cliOut, s.cliErr = 0, "", "", "", nil
	switch path {
	case "roger use":
		if err := s.cliRun("roger use " + band + " --yes"); err != nil {
			return err
		}
		if s.cliErr == nil && s.handler != nil {
			return s.completion(band)
		}
		return nil
	case "the TUI tune", "a guest operator":
		s.tune(band)
		return s.completion(band)
	case "TUI chat", "console chat":
		s.tune(band)
		if err := s.chat(); err != nil {
			s.cliErr = err
		}
		return nil
	case "the agent":
		s.tune(band)
		msg, err := harness.BrokerCompleterRoute(harness.BrokerRoute{Broker: s.broker.srv.URL, User: "u", Model: band})(
			context.Background(), []harness.Message{{Role: "user", Content: "hi"}}, nil)
		s.reply, s.cliErr = msg.Content, err
		return nil
	case "roger say":
		return s.cliRun("roger say hi")
	}
	return fmt.Errorf("unknown front end %q", path)
}

func (s *edgeOnlyBDD) refusedFromFrontEnd() error {
	if s.code == http.StatusOK || strings.Contains(s.reply, "from tower") {
		return fmt.Errorf("the turn was served: %d %s %s", s.code, s.body, s.reply)
	}
	if s.code == 0 && s.cliErr == nil {
		return fmt.Errorf("nothing refused the turn (out: %s)", s.cliOut)
	}
	return nil
}

func TestLANEdgeOnlyBDD(t *testing.T) {
	s := &edgeOnlyBDD{lanFrontBDD: lanFrontBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				client.SetLANRelay(nil)
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^edge-only is on$`, func() error { return nil })
			sc.Step(`^the LAN Tower at ([0-9.:]+) is in lan_towers and serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^nothing else local serves "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^a turn is dispatched for "([^"]*)"$`, s.dispatch)
			sc.Step(`^a turn for "([^"]*)" is dispatched$`, s.dispatch)
			sc.Step(`^a streamed turn for "([^"]*)" is dispatched$`, func(band string) error {
				return s.dispatchBody(band, `{"model":"`+band+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			})
			sc.Step(`^no request of any kind was made to ([0-9.:]+)$`, s.untouched)
			sc.Step(`^no request was made to ([0-9.:]+)$`, s.untouched)
			sc.Step(`^no request was made to it$`, func() error { return s.untouched("192.168.1.20:8787") })
			sc.Step(`^it is refused, saying edge-only is on and LAN Towers are not used$`, func() error {
				return s.refusedEdgeOnly("edge-only is on", "LAN Towers are not used")
			})
			sc.Step(`^the broker setting is a standalone Tower at ([0-9.:]+)$`, func(addr string) error {
				if err := s.towerRuns(addr, "qwen3-8b"); err != nil {
					return err
				}
				t.Setenv("ROGER_BROKER", "http://"+addr)
				return nil
			})
			sc.Step(`^the LAN dial is read$`, s.read)
			sc.Step(`^a Tower at ([0-9.:]+) advertises itself on the LAN$`, func(addr string) error {
				host, port, _ := net.SplitHostPort(addr)
				var p int
				fmt.Sscan(port, &p)
				s.adverts = append(s.adverts, wire.Advert{Kind: wire.TowerKind, IP: net.ParseIP(host), Port: p, Name: "shed"})
				return s.towerRuns(addr, "qwen3-8b")
			})
			sc.Step(`^no candidate line for ([0-9.:]+) is shown$`, func(addr string) error {
				for _, st := range s.status {
					if st.Addr == addr || strings.Contains(st.Detail, addr) {
						return fmt.Errorf("a line for %s is shown: %+v", addr, st)
					}
				}
				if n := s.browsed.Load(); n != 0 {
					return fmt.Errorf("the LAN was browsed for Towers %d time(s)", n)
				}
				return s.untouched(addr)
			})
			sc.Step(`^this machine serves "([^"]*)", the Edge member "([^"]*)" serves "([^"]*)"$`, func(self, node, band string) error {
				if err := s.machineServes(self); err != nil {
					return err
				}
				return s.memberVerified(node, band)
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServes)
			sc.Step(`^it lists "([^"]*)" from this machine and "([^"]*)" from "([^"]*)"$`, func(self, band, node string) error {
				r, ok := s.row(self)
				if !ok || len(r.Sources) != 1 || r.Sources[0].Kind != client.LANSelf {
					return fmt.Errorf("%q is not listed from this machine: %+v (dial %+v)", self, r, s.bands)
				}
				r, ok = s.row(band)
				if !ok || len(r.Sources) != 1 || r.Sources[0].Kind != client.LANEdge || r.Sources[0].ID != node {
					return fmt.Errorf("%q is not listed from %q: %+v", band, node, r)
				}
				return nil
			})
			sc.Step(`^it does not list "([^"]*)"$`, func(model string) error {
				if r, ok := s.row(model); ok {
					return fmt.Errorf("%q is listed: %+v", model, r)
				}
				return s.untouched("192.168.1.20:8787")
			})
			sc.Step(`^they run "(roger [^"]*)"$`, func(line string) error {
				if strings.Contains(line, "192.168.1.20:8787") {
					if err := s.towerRuns("192.168.1.20:8787", "qwen3-8b"); err != nil {
						return err
					}
				}
				return s.cliRun(line)
			})
			sc.Step(`^it is refused, saying edge-only is on and Towers are not used$`, func() error {
				return s.refusedEdgeOnly("edge-only is on", "Towers are not used")
			})
			sc.Step(`^the Tower is added to lan_towers$`, func() error {
				if s.cliErr != nil {
					return fmt.Errorf("lan add failed: %v", s.cliErr)
				}
				if !contains(loadConfig().LanTowers, "192.168.1.20:8787") {
					return fmt.Errorf("lan_towers: %v", loadConfig().LanTowers)
				}
				return nil
			})
			sc.Step(`^the output says it will be used in LAN mode but not while edge-only is on$`, func() error {
				if !strings.Contains(s.cliOut, "not while edge-only is on") {
					return fmt.Errorf("the output does not say so:\n%s", s.cliOut)
				}
				return nil
			})
			sc.Step(`^edge-only is on and lan_towers has ([0-9.:]+)$`, func(addr string) error {
				if err := s.towerRuns(addr, "qwen3-8b"); err != nil {
					return err
				}
				s.addTower(addr)
				return nil
			})
			sc.Step(`^([0-9.:]+) is listed as "([^"]*)"$`, func(addr, want string) error {
				for _, ln := range strings.Split(s.cliOut, "\n") {
					if strings.HasPrefix(ln, addr) && strings.Contains(ln, want) {
						return nil
					}
				}
				return fmt.Errorf("no line lists %s as %q:\n%s", addr, want, s.cliOut)
			})
			sc.Step(`^edge-only is on and the only source of "([^"]*)" is a LAN Tower$`, func(band string) error {
				return s.towerServing("192.168.1.20:8787", band)
			})
			sc.Step(`^a turn for "([^"]*)" is sent from "([^"]*)"$`, s.sendFrom)
			sc.Step(`^it is refused and the Tower was never contacted$`, func() error {
				if err := s.refusedFromFrontEnd(); err != nil {
					return err
				}
				return s.untouched("192.168.1.20:8787")
			})
			// section 4: the trust note
			sc.Step(`^edge-only is on and the trust note has never been shown$`, func() error {
				return s.towerServing("192.168.1.20:8787", "qwen3-8b")
			})
			sc.Step(`^the plain-http Tower trust note is not shown$`, func() error {
				if strings.Contains(s.cliOut, "plain http") || loadConfig().LanTrustNoted {
					return fmt.Errorf("the trust note was shown:\n%s", s.cliOut)
				}
				return s.untouched("192.168.1.20:8787")
			})
			sc.Step(`^it is still shown the first time a turn goes to a LAN Tower with edge-only off$`, func() error {
				os.Unsetenv("ROGER_LAN_EDGE_ONLY")
				s.edgeOnly = false
				if err := s.cliRun("roger --lan use qwen3-8b --yes"); err != nil {
					return err
				}
				if !strings.Contains(s.cliOut, "plain http") {
					return fmt.Errorf("with edge-only off the note is not shown:\n%s (err %v)", s.cliOut, s.cliErr)
				}
				return nil
			})
			// section 5
			sc.Step(`^the enrolled Edge member "([^"]*)" lives at 192\.168\.1\.20$`, func(node string) error {
				return s.face(node, "qwen3-8b", 0)
			})
			sc.Step(`^a standalone Tower also listens at ([0-9.:]+)$`, func(addr string) error {
				return s.towerServing(addr, "qwen3-8b")
			})
			sc.Step(`^"([^"]*)" is reached over mutual TLS on its Edge face$`, func(node string) error {
				if err := s.dispatch("qwen3-8b"); err != nil {
					return err
				}
				if err := s.servedBy(node); err != nil {
					return err
				}
				if s.faces[node].mutualTLS.Load() == 0 {
					return fmt.Errorf("%q was not reached over mutual TLS", node)
				}
				return nil
			})
			sc.Step(`^the Edge member "([^"]*)" and a LAN Tower both serve "([^"]*)"$`, func(node, band string) error {
				if err := s.face(node, band, 0); err != nil {
					return err
				}
				return s.towerServing("192.168.1.20:8787", band)
			})
			sc.Step(`^"([^"]*)" fails$`, func(node string) error { s.faces[node].srv.Close(); return nil })
			sc.Step(`^it is refused, naming "([^"]*)" and how it failed$`, func(node string) error {
				return s.refusedNaming(node, "edge-only is on")
			})
			sc.Step(`^the Tower was never contacted$`, func() error { return s.untouched("192.168.1.20:8787") })
			sc.Step(`^no request was made to the public broker$`, s.noMarketInference)
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: edgeOnlyPaths(111, 120, 127, 134, 143, 150, 157, 164, 230, 241, 258, 270)},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (Towers absent) failed")
	}
}

// ---- section 3: share under edge-only ----------------------------------------------------------

func TestLANEdgeOnlyShareBDD(t *testing.T) {
	s := &lanShareBDD{t: t}
	var console *httptest.Server
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				t.Setenv("ROGER_LAN_EDGE_ONLY", "1")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if console != nil {
					console.Close()
					console = nil
				}
				s.after()
				return ctx, nil
			})
			sc.Step(`^edge-only is on and lan_towers has ([0-9.:]+)$`, func(addr string) error {
				if err := s.machineServes("qwen3-8b"); err != nil {
					return err
				}
				return s.addTower(addr, true)
			})
			sc.Step(`^they run "([^"]*)"$`, s.runShare)
			sc.Step(`^it serves this Edge's members$`, func() error {
				if s.err != nil {
					return fmt.Errorf("share failed: %v\n%s", s.err, s.out)
				}
				if !strings.Contains(s.out, "Edge members only") {
					return fmt.Errorf("the share does not say it serves the Edge's members:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^it never polls ([0-9.:]+)$`, func(addr string) error { return edgeOnlyNeverPolled(s, addr) })
			sc.Step(`^([0-9.:]+) is never polled$`, func(addr string) error {
				time.Sleep(300 * time.Millisecond) // a poll loop would have started by now
				return edgeOnlyNeverPolled(s, addr)
			})
			sc.Step(`^it says it is serving Edge members only because edge-only is on$`, func() error {
				if !strings.Contains(s.out, "Edge members only") || !strings.Contains(s.out, "edge-only is on") {
					return fmt.Errorf("the share does not say why:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^SHARE goes on air from the TUI or the console$`, func() error {
				cfg := loadConfig()
				cfg.lan = lanmode.Mode{LAN: true, EdgeOnly: true, Source: lanmode.SrcEnv}
				s.ctrl = tui.NewController(cfg.Broker, tuiHooks(cfg))
				s.ctrl.SetRows([]node.ShareRow{{Model: s.model, Upstream: loadConfig().Share.Upstream}})
				ctrl, model := s.ctrl, s.model
				t.Cleanup(func() {
					if ctrl.Sessions()[model] != nil {
						ctrl.ToggleOnAir(model)
					}
				})
				srv := webui.New(s.ctrl, webui.Options{Broker: cfg.Broker})
				console = httptest.NewServer(srv.Handler())
				req, _ := http.NewRequest(http.MethodPost, console.URL+"/api/share/onair?t="+srv.Token(),
					strings.NewReader(`{"model":"`+s.model+`"}`))
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return fmt.Errorf("console on-air: %d", resp.StatusCode)
				}
				// The TUI's SHARE view over the same controller: the real model, [2] pressed.
				tui.SetLANDial(lanDialFor(cfg))
				t.Cleanup(func() { tui.SetLANDial(nil) })
				var m tea.Model = tui.NewWithHooksController(cfg.Broker, "u", nil, tuiHooks(cfg), s.ctrl)
				m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
				s.out = ansi.Strip(m.View())
				return nil
			})
			sc.Step(`^it reads "([^"]*)" with no Tower named$`, func(want string) error {
				if !strings.Contains(s.out, want) || strings.Contains(s.out, "tower:") {
					return fmt.Errorf("SHARE reads:\n%s", s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: edgeOnlyPaths(184, 192)},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (share) failed")
	}
}

func edgeOnlyNeverPolled(s *lanShareBDD, addr string) error {
	pt := s.towers[addr]
	if pt == nil {
		return fmt.Errorf("no Tower stands at %s", addr)
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if len(pt.paths) != 0 {
		return fmt.Errorf("the Tower at %s was asked %v", addr, pt.paths)
	}
	return nil
}
