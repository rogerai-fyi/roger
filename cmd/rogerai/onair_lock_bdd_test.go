package main

// onair_lock_bdd_test.go makes features/sharing/on_air_lock.feature EXECUTABLE: the
// per-node-id ON-AIR lock must guard EVERY front-end, not just the headless daemon
// (the 2026-07-02 eager-puma-54-voice double-broadcast: an abandoned TUI share and a
// systemd `roger share` unit rotated each other's bridge tokens forever).
//
// REAL DEPS, NO MOCKS:
//   - The "other process" legs re-exec THIS test binary (the stdlib helper-process
//     pattern): a child really acquires the lock via the REAL acquireOnAirLock and is
//     really SIGKILLed for the stale-reclaim leg, so PID liveness is the kernel's
//     answer, not a stub.
//   - The console is the REAL internal/node Controller driving the REAL agent.Start
//     against an httptest broker that accepts registrations (the tui/node test
//     pattern); the dead-broker leg points at a refused port.
//   - Lock files live in the real configPath() layout, isolated per scenario via the
//     same env triplet useTempConfig documents (XDG_CONFIG_HOME / HOME / AppData).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/node"
)

// TestOnAirLockHelper is the RE-EXEC entry point, not a test: it only runs when the
// parent scenario spawns this binary with ROGER_ONAIR_HELPER set (hold | try).
//
//	hold: acquire the lock for ROGER_ONAIR_NODE, touch ROGER_ONAIR_READY, block until
//	  killed (bounded so a crashed parent can't leak it forever).
//	try:  attempt the acquire once and report ACQUIRED / REFUSED:<err> on stdout.
func TestOnAirLockHelper(t *testing.T) {
	mode := os.Getenv("ROGER_ONAIR_HELPER")
	if mode == "" {
		t.Skip("helper-process entry point (spawned by the on-air lock BDD)")
	}
	nodeID := os.Getenv("ROGER_ONAIR_NODE")
	release, err := acquireOnAirLock(nodeID, os.Getenv("ROGER_ONAIR_STATION"), os.Getenv("ROGER_ONAIR_MODEL"))
	switch mode {
	case "hold":
		if err != nil {
			fmt.Println("REFUSED:", err)
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv("ROGER_ONAIR_READY"), []byte("held"), 0600); err != nil {
			os.Exit(4)
		}
		time.Sleep(120 * time.Second) // parent SIGKILLs us; bounded so nothing leaks
		release()
	case "try":
		if err != nil {
			fmt.Println("REFUSED:", err)
			return
		}
		fmt.Println("ACQUIRED")
		release()
	}
}

type onAirLockState struct {
	t *testing.T

	station    string
	brokerURL  string // lazily built accept-all broker (or the refused port)
	brokerDown bool

	ctrl     *node.Controller
	loggedIn bool

	holder    *exec.Cmd // the live "other process" (hold helper)
	holderPID int

	res  node.ToggleResult
	pres node.PrivateResult
	// tryOut is the try-helper's stdout (ACQUIRED / REFUSED:<err>).
	tryOut string
}

func (s *onAirLockState) reset(t *testing.T) {
	s.t = t
	s.station, s.brokerURL, s.brokerDown = "", "", false
	s.ctrl, s.loggedIn = nil, false
	s.holder, s.holderPID = nil, 0
	s.res, s.pres, s.tryOut = node.ToggleResult{}, node.PrivateResult{}, ""
}

func (s *onAirLockState) cleanup() {
	if s.ctrl != nil {
		s.ctrl.StopAll()
	}
	s.killHolder()
}

func (s *onAirLockState) killHolder() {
	if s.holder != nil && s.holder.Process != nil {
		_ = s.holder.Process.Kill()
		_, _ = s.holder.Process.Wait()
		s.holder = nil
	}
}

// controller lazily builds the REAL shared node controller for station, against a
// live accept-all httptest broker (or the refused port for the dead-broker leg).
func (s *onAirLockState) controller(station string) *node.Controller {
	if s.ctrl != nil {
		return s.ctrl
	}
	if s.brokerDown {
		s.brokerURL = "http://127.0.0.1:1" // reserved port: connections are refused
	}
	if s.brokerURL == "" {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}))
		s.t.Cleanup(srv.Close)
		s.brokerURL = srv.URL
	}
	s.station = station
	s.ctrl = node.New(node.Config{Broker: s.brokerURL, HW: "cpu", Station: station})
	s.ctrl.SetRows([]node.ShareRow{
		{Model: "voice", Modality: "tts", Ctx: 4096, Upstream: "http://127.0.0.1:9/v1/chat/completions"},
		{Model: "chat", Ctx: 4096, Upstream: "http://127.0.0.1:9/v1/chat/completions"},
	})
	if s.loggedIn {
		s.ctrl.SetLoggedIn(true)
	}
	return s.ctrl
}

// helperCmd builds a re-exec of this test binary running ONLY the helper entry
// point, inheriting the scenario's isolated config env (t.Setenv mutates our
// process env, so the child sees the same temp config dir).
func (s *onAirLockState) helperCmd(mode, nodeID, station, model, ready string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run", "^TestOnAirLockHelper$", "-test.v")
	cmd.Env = append(os.Environ(),
		"ROGER_ONAIR_HELPER="+mode,
		"ROGER_ONAIR_NODE="+nodeID,
		"ROGER_ONAIR_STATION="+station,
		"ROGER_ONAIR_MODEL="+model,
		"ROGER_ONAIR_READY="+ready,
	)
	return cmd
}

// --- Given -----------------------------------------------------------------------

func (s *onAirLockState) daemonOnAir(station, model string) error {
	nodeID := agent.ShareNodeID(station, model, 0)
	ready := onAirLockPath(nodeID) + ".ready"
	cmd := s.helperCmd("hold", nodeID, station, model, ready)
	if err := cmd.Start(); err != nil {
		return err
	}
	s.holder, s.holderPID = cmd, cmd.Process.Pid
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			s.killHolder()
			return fmt.Errorf("the hold helper never signalled ready (lock %s)", onAirLockPath(nodeID))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *onAirLockState) loggedInAtConsole() error {
	s.loggedIn = true
	if s.ctrl != nil {
		s.ctrl.SetLoggedIn(true)
	}
	return nil
}

func (s *onAirLockState) brokerRefuses() error {
	s.brokerDown = true
	return nil
}

func (s *onAirLockState) holderKilled() error {
	if s.holder == nil {
		return fmt.Errorf("no hold helper to kill")
	}
	nodeID := agent.ShareNodeID(s.station, "voice", 0)
	if s.station == "" {
		nodeID = agent.ShareNodeID("eager-puma-54", "voice", 0)
	}
	s.killHolder()
	// The SIGKILL must leave the lock file behind (that is what makes it STALE).
	if _, err := os.Stat(onAirLockPath(nodeID)); err != nil {
		return fmt.Errorf("the killed daemon's lock file is unexpectedly gone: %v", err)
	}
	return nil
}

// --- When ------------------------------------------------------------------------

func (s *onAirLockState) togglesOnAir(model, station string) error {
	s.res = s.controller(station).ToggleOnAir(model)
	return nil
}

func (s *onAirLockState) togglesOffAir(model string) error {
	if s.ctrl == nil {
		return fmt.Errorf("no console controller built")
	}
	s.res = s.ctrl.ToggleOnAir(model)
	if !s.res.WentOff {
		return fmt.Errorf("toggle did not take %q off air: %+v", model, s.res)
	}
	return nil
}

func (s *onAirLockState) flipsPrivate(model, station string) error {
	s.pres = s.controller(station).TogglePrivate(model)
	return nil
}

func (s *onAirLockState) stopsAll() error {
	if s.ctrl == nil {
		return fmt.Errorf("no console controller built")
	}
	s.ctrl.StopAll()
	return nil
}

func (s *onAirLockState) headlessAcquire(nodeID string) error {
	out, err := s.helperCmd("try", nodeID, "second-daemon", "voice", "").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("try helper exited %v: %s %s", err, out, ee.Stderr)
		}
		return err
	}
	s.tryOut = string(out)
	return nil
}

// --- Then ------------------------------------------------------------------------

// refusedNamingHolder asserts err carries the headless path's exact refusal shape:
// "already on air", the HOLDER's station in quotes, its live PID, and the lock path
// (the operator's escape hatch).
func (s *onAirLockState) refusedNamingHolder(err error) error {
	if err == nil {
		return fmt.Errorf("expected the on-air refusal, got no error (the double-broadcast enabler)")
	}
	msg := err.Error()
	for _, want := range []string{"already on air", `"eager-puma-54"`, fmt.Sprintf("pid %d", s.holderPID), "share-"} {
		if !strings.Contains(msg, want) {
			return fmt.Errorf("refusal %q does not name %q (must mirror the headless error text)", msg, want)
		}
	}
	return nil
}

func (s *onAirLockState) toggleRefused() error { return s.refusedNamingHolder(s.res.Err) }
func (s *onAirLockState) flipRefused() error   { return s.refusedNamingHolder(s.pres.Err) }

func (s *onAirLockState) noSessionStarted() error {
	if s.ctrl == nil {
		return fmt.Errorf("no console controller built")
	}
	if n := s.ctrl.OnAirCount(); n != 0 {
		return fmt.Errorf("%d session(s) on air after a refused toggle - the node id is double-broadcast", n)
	}
	return nil
}

func (s *onAirLockState) otherProcessHoldsLock() error {
	nodeID := agent.ShareNodeID("eager-puma-54", "voice", 0)
	return s.lockOwnedBy(nodeID, s.holderPID)
}

func (s *onAirLockState) lockOwnedBy(nodeID string, pid int) error {
	b, err := os.ReadFile(onAirLockPath(nodeID))
	if err != nil {
		return fmt.Errorf("lock for %s unreadable: %v", nodeID, err)
	}
	var info onAirInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return err
	}
	if info.PID != pid {
		return fmt.Errorf("lock for %s owned by pid %d, want %d", nodeID, info.PID, pid)
	}
	return nil
}

func (s *onAirLockState) helperRefused() error {
	if !strings.Contains(s.tryOut, "REFUSED:") || !strings.Contains(s.tryOut, "already on air") {
		return fmt.Errorf("the second daemon was not refused (output: %q)", s.tryOut)
	}
	return nil
}

func (s *onAirLockState) helperAcquired(nodeID string) error {
	if err := s.headlessAcquire(nodeID); err != nil {
		return err
	}
	if !strings.Contains(s.tryOut, "ACQUIRED") {
		return fmt.Errorf("a fresh daemon could not acquire the released lock (output: %q)", s.tryOut)
	}
	return nil
}

func (s *onAirLockState) lockReleased(nodeID string) error {
	if _, err := os.Stat(onAirLockPath(nodeID)); !os.IsNotExist(err) {
		return fmt.Errorf("lock file for %s still present (stat err=%v) - the lock leaked", nodeID, err)
	}
	return nil
}

func (s *onAirLockState) shareOnAir() error {
	if s.res.Err != nil || s.res.AtLimit || s.res.LoginNeeded || s.res.WentOff {
		return fmt.Errorf("share did not go on air: %+v", s.res)
	}
	if n := s.ctrl.OnAirCount(); n == 0 {
		return fmt.Errorf("no session on air after a successful toggle")
	}
	return nil
}

func (s *onAirLockState) consoleOwnsLock(nodeID string) error {
	return s.lockOwnedBy(nodeID, os.Getpid())
}

func (s *onAirLockState) flipSucceeds() error {
	if s.pres.Err != nil || s.pres.LoginNeeded || s.pres.AtLimit {
		return fmt.Errorf("private flip failed: %+v", s.pres)
	}
	if !s.pres.NowPrivate {
		return fmt.Errorf("flip did not go private: %+v", s.pres)
	}
	return nil
}

func (s *onAirLockState) toggleFailsRegistration() error {
	if s.res.Err == nil {
		return fmt.Errorf("toggle against a dead broker unexpectedly succeeded")
	}
	if strings.Contains(s.res.Err.Error(), "already on air") {
		return fmt.Errorf("expected a registration error, got the on-air refusal: %v", s.res.Err)
	}
	return nil
}

func TestOnAirLockBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &onAirLockState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				// Same cross-platform isolation as useTempConfig: the lock lives in the
				// real configPath() layout, so every scenario gets a throwaway config dir.
				dir := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", dir)
				t.Setenv("HOME", dir)
				t.Setenv("AppData", dir)
				if got := configPath(); !strings.HasPrefix(got, dir) {
					return ctx, fmt.Errorf("config isolation FAILED: %q not under %q", got, dir)
				}
				st.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return ctx, nil
			})

			sc.Step(`^a headless daemon in another process is on air as station "([^"]*)" model "([^"]*)"$`, st.daemonOnAir)
			sc.Step(`^the operator is logged in at the console$`, st.loggedInAtConsole)
			sc.Step(`^the console's broker refuses registrations$`, st.brokerRefuses)
			sc.Step(`^that process is killed without releasing its lock$`, st.holderKilled)

			sc.Step(`^the operator toggles "([^"]*)" on air from the console for station "([^"]*)"$`, st.togglesOnAir)
			sc.Step(`^the operator toggles "([^"]*)" off air$`, st.togglesOffAir)
			sc.Step(`^the operator flips "([^"]*)" private from the console for station "([^"]*)"$`, st.flipsPrivate)
			sc.Step(`^the operator stops all shares$`, st.stopsAll)
			sc.Step(`^another process runs the headless on-air acquire for node id "([^"]*)"$`, st.headlessAcquire)

			sc.Step(`^the toggle is refused with the on-air error naming the live broadcaster$`, st.toggleRefused)
			sc.Step(`^the flip is refused with the on-air error naming the live broadcaster$`, st.flipRefused)
			sc.Step(`^no share session is started$`, st.noSessionStarted)
			sc.Step(`^the other process still holds the on-air lock$`, st.otherProcessHoldsLock)
			sc.Step(`^that process is refused with the on-air error naming the live broadcaster$`, st.helperRefused)
			sc.Step(`^the on-air lock for node id "([^"]*)" is released$`, st.lockReleased)
			sc.Step(`^another process can now acquire the headless on-air lock for node id "([^"]*)"$`, st.helperAcquired)
			sc.Step(`^the share goes on air$`, st.shareOnAir)
			sc.Step(`^the console's process owns the on-air lock for node id "([^"]*)"$`, st.consoleOwnsLock)
			sc.Step(`^the flip succeeds$`, st.flipSucceeds)
			sc.Step(`^the toggle fails with a registration error$`, st.toggleFailsRegistration)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/sharing/on_air_lock.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("on-air lock scenarios failed (see godog output above)")
	}
}
