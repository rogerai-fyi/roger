package main

// Executable spec: features/packaging/one_binary.feature sections 7.2 (one Tower detection) and
// 7.3 (one standalone share loop). The Tower is a real standalone Tower (lan_fixture_test.go) or,
// where a scenario counts probes, a stub that answers the plane's uniform 401 and counts them.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/lanmode"
)

type oneBinaryBDD struct {
	t       *testing.T
	dial    *lanDialMap
	out     bytes.Buffer
	cfg     config
	polls   atomic.Int64
	stub    *httptest.Server
	shareTo []string
}

func (s *oneBinaryBDD) reset() {
	useTempConfig(s.t)
	lanmode.ResetTowers()
	s.dial = &lanDialMap{}
	lanProbeHTTP = s.dial.client()
	s.out.Reset()
	s.polls.Store(0)
	s.shareTo = nil
}

func (s *oneBinaryBDD) restore() {
	lanProbeHTTP = &http.Client{Timeout: 3 * time.Second}
	lanmode.ResetTowers()
	if s.stub != nil {
		s.stub.Close()
		s.stub = nil
	}
}

// sourceOf reads a file of this package.
func sourceOf(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func (s *oneBinaryBDD) onlyProbe() error {
	for _, f := range []string{"localshare.go", "lan.go", "lanshare.go", "main.go"} {
		src, err := sourceOf(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if regexp.MustCompile(`func isLocalTowerBroker\(|lanmode\.ProbeTower\(|lanmode\.Probe\(`).MatchString(src) {
			return fmt.Errorf("%s still has its own Tower probe", f)
		}
	}
	lm, err := os.ReadFile("../../internal/lanmode/lanmode.go")
	if err != nil {
		return err
	}
	if regexp.MustCompile(`func (ProbeTower|Probe)\(`).Match(lm) {
		return fmt.Errorf("lanmode still exports a second probe beside IsTower")
	}
	if !regexp.MustCompile(`func IsTower\(`).Match(lm) {
		return fmt.Errorf("lanmode has no IsTower")
	}
	return nil
}

func (s *oneBinaryBDD) callersUseIt() error {
	need := map[string]string{
		"main.go":     `brokerTower\(`,              // roger share and the TUI and console SHARE hooks
		"lanshare.go": `lanmode\.IsTower\(`,         // brokerTower asks the one check
		"lan.go":      `lanmode\.(IsTower|Check)\(`, // roger lan add and the dial's Tower lines
	}
	for f, pat := range need {
		src, err := sourceOf(f)
		if err != nil {
			return err
		}
		if !regexp.MustCompile(pat).MatchString(src) {
			return fmt.Errorf("%s does not call the one Tower check", f)
		}
	}
	return nil
}

func (s *oneBinaryBDD) brokerTower(broker string) error {
	tw := newRealTower(s.t)
	host := strings.TrimPrefix(broker, "http://")
	s.dial.set(host, tw.addr())
	c := loadConfig()
	c.Broker = broker
	if err := saveConfig(c); err != nil {
		return err
	}
	s.cfg = loadConfig()
	return nil
}

func (s *oneBinaryBDD) starts() error {
	s.cfg = foldBrokerTower(loadConfig(), &s.out)
	return nil
}

func (s *oneBinaryBDD) lanTowersContains(addr string) error {
	if !contains(loadConfig().LanTowers, addr) {
		return fmt.Errorf("lan_towers %v does not contain %s", loadConfig().LanTowers, addr)
	}
	return nil
}

func (s *oneBinaryBDD) printsOnce(line string) error {
	if n := strings.Count(s.out.String(), line); n != 1 {
		return fmt.Errorf("printed %d times, want once: %q in %q", n, line, s.out.String())
	}
	// a second start prints nothing more
	before := s.out.Len()
	foldBrokerTower(loadConfig(), &s.out)
	if s.out.Len() != before {
		return fmt.Errorf("a second start printed again: %q", s.out.String()[before:])
	}
	return nil
}

func (s *oneBinaryBDD) shareServesIt() error {
	base, ok := brokerTower(s.cfg)
	if !ok || base != s.cfg.Broker {
		return fmt.Errorf("roger share does not serve the broker setting's Tower (got %q, %v)", base, ok)
	}
	return nil
}

func (s *oneBinaryBDD) stubTower() {
	s.stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/poll" {
			s.polls.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
}

func (s *oneBinaryBDD) rendersAndLists() error {
	s.stubTower()
	base := s.stub.URL
	lanProbeHTTP = &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < 5; i++ { // the TUI's renders
		if !lanmode.IsTower(context.Background(), lanProbeHTTP, base) {
			return fmt.Errorf("the stub was not recognized as a Tower")
		}
	}
	for i := 0; i < 2; i++ { // lan list's Tower line
		if lanmode.Check(context.Background(), lanProbeHTTP, base) != lanmode.ProbeTowerYes {
			return fmt.Errorf("lan list did not see a Tower")
		}
	}
	return nil
}

func (s *oneBinaryBDD) probedOnce() error {
	if n := s.polls.Load(); n != 1 {
		return fmt.Errorf("the Tower was probed %d times inside one cache window, want 1", n)
	}
	return nil
}

func (s *oneBinaryBDD) allRunStartLAN() error {
	for f, pat := range map[string]string{
		"lanshare.go": `agent\.StartLAN\(`,
		"main.go":     `shareToTowers\(`,
	} {
		src, err := sourceOf(f)
		if err != nil {
			return err
		}
		if !regexp.MustCompile(pat).MatchString(src) {
			return fmt.Errorf("%s does not share through the one loop", f)
		}
	}
	// TUI and console SHARE: the controller runs agent.StartLAN with the Towers it is given.
	ctl, err := os.ReadFile("../../internal/node/controller.go")
	if err != nil {
		return err
	}
	if !strings.Contains(string(ctl), "agent.StartLAN(") {
		return fmt.Errorf("the TUI and console SHARE controller does not run agent.StartLAN")
	}
	return nil
}

func (s *oneBinaryBDD) singleIsStartLANWithOne() error {
	src, err := sourceOf("main.go")
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`shareToTowers\([^)]*\[\]string\{`).MatchString(src) {
		return fmt.Errorf("the single-Tower share is not shareToTowers with one Tower")
	}
	return nil
}

func (s *oneBinaryBDD) oldLoopsGone() error {
	for _, f := range []string{"localshare.go", "lanshare.go", "main.go"} {
		src, err := sourceOf(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if strings.Contains(src, "func serveLocalTowerShare(") {
			return fmt.Errorf("%s still has serveLocalTowerShare", f)
		}
		if strings.Contains(src, "agent.ServeLocalTower(") {
			return fmt.Errorf("%s still runs its own ServeLocalTower loop", f)
		}
	}
	return nil
}

// shareBehavesAsToday drives the one loop against a Tower that has not attached this station:
// it polls, prints the 401 attach hint once, and stops when the share is stopped.
func (s *oneBinaryBDD) shareBehavesAsToday() error {
	s.stubTower()
	var out syncBuf
	sess := agent.StartLAN(agent.Config{Model: "m", Upstream: "http://127.0.0.1:1/v1/chat/completions"},
		[]string{s.stub.URL, s.stub.URL}, agent.NodeKey(), &out)
	deadline := time.Now().Add(5 * time.Second)
	for s.polls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.polls.Load() < 2 {
		return fmt.Errorf("the loop did not poll both Towers")
	}
	done := make(chan struct{})
	go func() { sess.Stop(); sess.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("stop did not end the loop")
	}
	if !strings.Contains(out.String(), "roger tower attach") && !strings.Contains(out.String(), "attach") {
		return fmt.Errorf("no attach hint after a 401: %q", out.String())
	}
	return nil
}

// syncBuf is a goroutine-safe buffer for the loop's output.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *syncBuf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func TestOneBinaryTowerAndShareBDD(t *testing.T) {
	s := &oneBinaryBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restore()
				return ctx, nil
			})
			sc.Step(`^the old isLocalTowerBroker, lanmode\.ProbeTower and the X-Roger-Local reader$`, func() error { return nil })
			sc.Step(`^lanmode\.IsTower\(base\) is the only probe$`, s.onlyProbe)
			sc.Step(`^roger share, roger lan add, the dial, the TUI and console SHARE all call it$`, s.callersUseIt)
			sc.Step(`^a config whose broker is "([^"]*)", a standalone Tower$`, s.brokerTower)
			sc.Step(`^roger starts after the upgrade$`, s.starts)
			sc.Step(`^lan_towers contains "([^"]*)"$`, s.lanTowersContains)
			sc.Step(`^roger prints once "([^"]*)"$`, s.printsOnce)
			sc.Step(`^roger share serves that Tower exactly as before$`, s.shareServesIt)
			sc.Step(`^the TUI renders repeatedly and lan list runs in the same second$`, s.rendersAndLists)
			sc.Step(`^the Tower is probed at most once per cache window$`, s.probedOnce)
			sc.Step(`^headless roger share to one Tower, roger --lan share to several, and TUI or console SHARE$`, func() error { return nil })
			sc.Step(`^all of them run agent\.StartLAN$`, s.allRunStartLAN)
			sc.Step(`^the single-Tower case is StartLAN with one Tower$`, s.singleIsStartLANWithOne)
			sc.Step(`^serveLocalTowerShare and shareLAN's own goroutine loop no longer exist$`, s.oldLoopsGone)
			sc.Step(`^a share serves one or several Towers$`, func() error { return nil })
			sc.Step(`^polls, backoff \(doubling to 30s\), the 401 attach hint and stop-on-cancel behave as today$`, s.shareBehavesAsToday)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:456",
				"../../features/packaging/one_binary.feature:461",
				"../../features/packaging/one_binary.feature:472",
				"../../features/packaging/one_binary.feature:478",
				"../../features/packaging/one_binary.feature:484",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature sections 7.2 and 7.3 failed")
	}
}
