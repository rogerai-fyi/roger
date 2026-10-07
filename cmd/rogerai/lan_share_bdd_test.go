package main

// lan_share_bdd_test.go runs features/lan/share_in_lan_mode.feature's @cli and @agent scenarios
// against a REAL standalone Tower (localplane over a real tower.State), the real `roger share`
// command and the real station loop (agent.ServeLocalTower). A recording broker proves nothing
// was registered.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/onair"
)

// pollTower is a real standalone Tower that records when it was polled.
type pollTower struct {
	*realTower
	mu    sync.Mutex
	polls []time.Time
}

func newPollTower(t *testing.T) *pollTower {
	rt := newRealTower(t)
	pt := &pollTower{realTower: rt}
	h := localplane.New(rt.st).Handler()
	rt.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/poll" {
			pt.mu.Lock()
			pt.polls = append(pt.polls, time.Now())
			pt.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	})
	return pt
}

func (p *pollTower) pollTimes() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.polls...)
}

type lanShareBDD struct {
	t       *testing.T
	broker  *recordingBroker
	model   string
	towers  map[string]*pollTower // by scenario address
	real    map[string]string     // scenario address -> real host:port
	out     string
	err     error
	runFor  time.Duration
	release func()
	fabric  bool
}

func (s *lanShareBDD) reset() {
	useTempConfig(s.t)
	s.broker = newRecordingBroker(s.t)
	s.t.Setenv("ROGER_BROKER", s.broker.srv.URL)
	s.model, s.out, s.err, s.runFor, s.release = "", "", nil, 1500*time.Millisecond, nil
	s.towers, s.real, s.fabric = map[string]*pollTower{}, map[string]string{}, false
	prev := startRelayFabric
	startRelayFabric = func(agent.Config) { s.fabric = true }
	s.t.Cleanup(func() { startRelayFabric = prev })
}

func (s *lanShareBDD) after() {
	if s.release != nil {
		s.release()
	}
}

// machineServes makes the share find a real local model server serving band.
func (s *lanShareBDD) machineServes(band string) error {
	s.model = band
	up := completionUpstream(s.t, "self")
	c := loadConfig()
	c.Share = &Share{Upstream: up.URL + "/v1", UpstreamKey: "k", Model: band}
	if err := saveConfig(c); err != nil {
		return err
	}
	prev := detectProbeKey
	detectProbeKey = func(string, string) (detect.Found, detect.Status) {
		return detect.Found{Name: "llama.cpp", BaseURL: up.URL + "/v1", Chat: up.URL + "/v1/chat/completions",
			Models: []string{band}}, detect.Reachable
	}
	s.t.Cleanup(func() { detectProbeKey = prev })
	return nil
}

func (s *lanShareBDD) addTower(addr string, attached bool) error {
	pt := newPollTower(s.t)
	pub, _ := newKey(s.t)
	if err := pt.admit(pub); err != nil { // the first admission makes the Tower's local operator
		return err
	}
	if attached {
		if err := pt.attach(agent.NodeID(), s.model); err != nil {
			return err
		}
	}
	s.towers[addr], s.real[addr] = pt, pt.addr()
	c := loadConfig()
	c.LanTowers = append(c.LanTowers, pt.addr())
	return saveConfig(c)
}

func (s *lanShareBDD) runShare(line string) error {
	prevCtx := lanShareContext
	ctx, cancel := context.WithTimeout(context.Background(), s.runFor)
	defer cancel()
	lanShareContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	defer func() { lanShareContext = prevCtx }()
	args := strings.Fields(line)[1:]
	s.out, s.err = captureEdgeStdout(func() error { return run(args, loadConfig()) })
	return nil
}

func (s *lanShareBDD) brokerUntouched(paths ...string) error {
	return brokerNotAsked(s.broker, paths...)
}

// onlyTowerProbe allows the one /local/poll probe LAN mode makes of a broker setting that is a
// local address (it is how a local Tower is told apart); anything else is a broker request.
func (s *lanShareBDD) onlyTowerProbe() error {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	for _, p := range s.broker.paths {
		if p != "/local/poll" {
			return fmt.Errorf("the broker was asked %s: %v", p, s.broker.paths)
		}
	}
	return nil
}

func TestLANShareBDD(t *testing.T) {
	s := &lanShareBDD{t: t}
	var paths []string
	for _, l := range []int{27, 35, 42, 71, 77, 84} {
		paths = append(paths, fmt.Sprintf("../../features/lan/share_in_lan_mode.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.after()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { t.Setenv("ROGER_LAN", "1"); return nil })
			sc.Step("^this machine serves \"([^\"]*)\" with `roger model serve`$", s.machineServes)
			sc.Step(`^lan_towers contains "([^"]*)" and that Tower has attached this machine's station key$`, func(a string) error {
				return s.addTower(a, true)
			})
			sc.Step(`^lan_towers contains "([^"]*)" and that Tower has not attached this station's key$`, func(a string) error {
				s.runFor = 4 * time.Second // long enough to see three retries back off
				return s.addTower(a, false)
			})
			sc.Step(`^lan_towers is empty and the broker setting is not local$`, func() error { return nil })
			sc.Step(`^they run "([^"]*)"$`, s.runShare)
			sc.Step(`^"([^"]*)" starts$`, s.runShare)
			sc.Step(`^the station polls "http://([^"]*)/local/poll"$`, func(addr string) error {
				if len(s.towers[addr].pollTimes()) == 0 {
					return fmt.Errorf("the Tower at %s was never polled:\n%s", addr, s.out)
				}
				return nil
			})
			sc.Step(`^no request is made to the public broker's /nodes/register or /nodes/heartbeat$`, func() error {
				return s.brokerUntouched("/nodes/register", "/nodes/heartbeat")
			})
			sc.Step(`^the relay fabric is not joined$`, func() error {
				if s.fabric {
					return fmt.Errorf("the relay fabric was joined")
				}
				return nil
			})
			sc.Step(`^the output says it is serving this Edge's members only$`, func() error {
				if !strings.Contains(s.out, "serving this Edge's members only") {
					return fmt.Errorf("output:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^no request is made to the public broker$`, s.onlyTowerProbe)
			sc.Step(`^the output names the Tower, says it has not attached this station, and shows the key to attach$`, func() error {
				for _, want := range []string{"has not attached this station", agent.NodeID()} {
					if !strings.Contains(s.out, want) {
						return fmt.Errorf("output lacks %q:\n%s", want, s.out)
					}
				}
				for _, real := range s.real {
					if !strings.Contains(s.out, real) {
						return fmt.Errorf("output does not name the Tower %s:\n%s", real, s.out)
					}
				}
				return nil
			})
			sc.Step(`^polling of that Tower backs off$`, func() error {
				for _, pt := range s.towers {
					at := pt.pollTimes()
					if len(at) < 3 {
						return fmt.Errorf("only %d polls", len(at))
					}
					first, last := at[1].Sub(at[0]), at[len(at)-1].Sub(at[len(at)-2])
					if last < 2*first {
						return fmt.Errorf("polls did not back off: first gap %s, last gap %s (%d polls)", first, last, len(at))
					}
				}
				return nil
			})
			sc.Step(`^it exits non-zero, saying private bands are a network feature and LAN mode is on$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "private bands are a network feature") || !strings.Contains(s.err.Error(), "LAN mode is on") {
					return fmt.Errorf("err = %v", s.err)
				}
				return nil
			})
			sc.Step(`^nothing was registered anywhere$`, func() error {
				return s.brokerUntouched("/nodes/register", "/nodes/heartbeat", "/bands", "/bands/create")
			})
			sc.Step(`^it exits non-zero, saying LAN sharing is free and has no price$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "LAN sharing is free and has no price") {
					return fmt.Errorf("err = %v", s.err)
				}
				return nil
			})
			sc.Step(`^the same machine was sharing on the market from another process$`, func() error {
				// A real other process holds the node id's on-air lock, as a market share would.
				other := exec.Command("sleep", "30")
				if err := other.Start(); err != nil {
					return err
				}
				s.release = func() { _ = other.Process.Kill(); _ = other.Wait() }
				station := loadOrCreateStation()
				nodeID := agent.ShareNodeID(station, s.model, 0)
				b, _ := json.Marshal(onair.Info{PID: other.Process.Pid, Station: station, Model: s.model, Started: time.Now().Unix()})
				if err := os.MkdirAll(filepath.Dir(onair.LockPath(nodeID)), 0o700); err != nil {
					return err
				}
				return os.WriteFile(onair.LockPath(nodeID), b, 0o600)
			})
			sc.Step(`^it refuses to start while the market share is live, naming it$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), s.model) {
					return fmt.Errorf("err = %v\n%s", s.err, s.out)
				}
				return s.brokerUntouched("/nodes/register")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("share_in_lan_mode scenarios failed")
	}
}
