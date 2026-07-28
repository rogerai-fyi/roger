package operator

// claude_guest_bdd_test.go makes features/handoff/claude_guest.feature EXECUTABLE against
// the REAL registry and the REAL Materialize. The credential scenarios are the point of the
// suite: every other guest is deliberately wired to the tuned band, and this one must be
// deliberately not - so the assertions walk the ACTUAL Launch (argv + env) looking for the
// live session key, base URL and model, rather than trusting the strategy's name.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

const (
	liveKey   = "sk-roger-live-session-key"
	liveBase  = "https://broker.rogerai.fyi"
	liveModel = "gpt-oss-20b"
)

type claudeGuestState struct {
	t *testing.T

	workdir string
	scratch string
	launch  Launch
	err     error
	cleanup func() error
}

func (s *claudeGuestState) reset() {
	s.workdir = s.t.TempDir()
	s.scratch = s.t.TempDir()
	s.launch, s.err, s.cleanup = Launch{}, nil, nil
}

func (s *claudeGuestState) guest(name string) (Guest, bool) {
	for _, g := range Registry() {
		if g.Name == name {
			return g, true
		}
	}
	return Guest{}, false
}

// --- the registry entry ----------------------------------------------------------

func (s *claudeGuestState) registryContains(name string) error {
	if _, ok := s.guest(name); !ok {
		return fmt.Errorf("the registry has no %q entry", name)
	}
	return nil
}

func (s *claudeGuestState) strategyIsContextOnly() error {
	g, _ := s.guest("claude")
	if g.Strategy != StrategyContextOnly {
		return fmt.Errorf("claude's strategy is %q, want %q", g.Strategy, StrategyContextOnly)
	}
	return nil
}

func (s *claudeGuestState) notNeedingSetup() error {
	g, _ := s.guest("claude")
	if g.NeedsSetup {
		return fmt.Errorf("claude is still marked NeedsSetup - it is launchable now")
	}
	return nil
}

func (s *claudeGuestState) mvpGuestsUnchanged() error {
	want := map[string]string{
		"opencode": StrategyScratchConfig,
		"hermes":   StrategyScratchHome,
		"aider":    StrategyEnvFlags,
	}
	for name, strategy := range want {
		g, ok := s.guest(name)
		if !ok {
			return fmt.Errorf("%q vanished from the registry", name)
		}
		if g.Strategy != strategy {
			return fmt.Errorf("%q strategy = %q, want the unchanged %q", name, g.Strategy, strategy)
		}
	}
	return nil
}

func (s *claudeGuestState) noEntryNamed(name string) error {
	if _, ok := s.guest(name); ok {
		return fmt.Errorf("the registry contains %q", name)
	}
	return nil
}

// --- the launch injects nothing ----------------------------------------------------

func (s *claudeGuestState) liveSession() error { return nil }

func (s *claudeGuestState) materializeClaude() error {
	g, ok := s.guest("claude")
	if !ok {
		return fmt.Errorf("no claude entry to materialize")
	}
	s.launch, s.cleanup, s.err = Materialize(g, Session{
		BaseURL: liveBase, SessionKey: liveKey, Model: liveModel,
		Workdir: s.workdir, ScratchRoot: s.scratch,
	})
	return s.err
}

// blob is everything the child could read: argv plus env.
func (s *claudeGuestState) blob() string {
	return strings.Join(append(append([]string{}, s.launch.Argv...), s.launch.Env...), "\x00")
}

func (s *claudeGuestState) noSessionKey() error {
	if strings.Contains(s.blob(), liveKey) {
		return fmt.Errorf("the session key reached the guest: %v %v", s.launch.Argv, s.launch.Env)
	}
	return nil
}

func (s *claudeGuestState) noBaseURL() error {
	if strings.Contains(s.blob(), liveBase) {
		return fmt.Errorf("the broker base URL reached the guest: %v %v", s.launch.Argv, s.launch.Env)
	}
	return nil
}

func (s *claudeGuestState) noModelOverride() error {
	if strings.Contains(s.blob(), liveModel) {
		return fmt.Errorf("a model override reached the guest: %v %v", s.launch.Argv, s.launch.Env)
	}
	return nil
}

func (s *claudeGuestState) noScratchConfig() error {
	if s.launch.Dir != "" {
		return fmt.Errorf("a scratch dir was created at %q for a guest that needs no config", s.launch.Dir)
	}
	entries, err := os.ReadDir(s.scratch)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return fmt.Errorf("materializing claude wrote %v under the scratch root", names)
	}
	return nil
}

func (s *claudeGuestState) runsInWorkdir() error {
	cmd := Command(s.launch, "/usr/bin/claude", s.workdir, nil)
	if cmd.Dir != s.workdir {
		return fmt.Errorf("child cwd = %q, want the user's workdir %q", cmd.Dir, s.workdir)
	}
	return nil
}

// --- handing over the brief ---------------------------------------------------------

func (s *claudeGuestState) materializeWithBrief() error {
	dir := filepath.Join(s.workdir, filepath.Dir(BriefRelPath))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.workdir, BriefRelPath), []byte("# RogerAI session handoff\n"), 0o600); err != nil {
		return err
	}
	return s.materializeClaude()
}

func (s *claudeGuestState) argvCarriesOnePrompt() error {
	// Argv[0] is the binary (the Command() contract); everything after it is what the
	// guest is actually told.
	if len(s.launch.Argv) != 2 {
		return fmt.Errorf("argv = %v, want the binary plus exactly one opening prompt", s.launch.Argv)
	}
	if s.launch.Argv[0] != "claude" {
		return fmt.Errorf("argv[0] = %q, want the guest binary", s.launch.Argv[0])
	}
	return nil
}

func (s *claudeGuestState) promptNamesBrief() error {
	if !strings.Contains(strings.Join(s.launch.Argv, " "), BriefRelPath) {
		return fmt.Errorf("the opening prompt does not name the brief: %v", s.launch.Argv)
	}
	return nil
}

func (s *claudeGuestState) noNonInteractiveFlag() error {
	for _, a := range s.launch.Argv {
		if a == "-p" || a == "--print" {
			return fmt.Errorf("argv carries a non-interactive flag: %v", s.launch.Argv)
		}
	}
	return nil
}

func (s *claudeGuestState) noRecordedTurns() error { return nil }

func (s *claudeGuestState) noBriefInArgv() error {
	if strings.Contains(strings.Join(s.launch.Argv, " "), BriefRelPath) {
		return fmt.Errorf("argv points at a brief that was never written: %v", s.launch.Argv)
	}
	if len(s.launch.Argv) != 1 {
		return fmt.Errorf("argv = %v, want just the binary with no context to hand over", s.launch.Argv)
	}
	return nil
}

// --- not installed -------------------------------------------------------------------

func (s *claudeGuestState) claudeNotInstalled() error { return nil }

func (s *claudeGuestState) offeredWithInstallHint() error {
	g, ok := s.guest("claude")
	if !ok {
		return fmt.Errorf("claude is not in the registry, so it cannot be offered at all")
	}
	if strings.TrimSpace(g.InstallHint) == "" {
		return fmt.Errorf("claude carries no install hint")
	}
	// Absent from PATH means simply not detected - never an error, never hidden from the
	// registry that drives the not-installed suggestion row.
	det := Detect(Env{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	for _, d := range det {
		if d.Guest.Name == "claude" {
			return fmt.Errorf("claude was detected despite not being on PATH")
		}
	}
	return nil
}

func TestClaudeGuestBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &claudeGuestState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.cleanup != nil {
					_ = st.cleanup()
				}
				return ctx, err
			})

			sc.Step(`^the registry contains a "([^"]*)" entry$`, st.registryContains)
			sc.Step(`^its strategy is context-only$`, st.strategyIsContextOnly)
			sc.Step(`^it is not marked as needing setup$`, st.notNeedingSetup)
			sc.Step(`^"opencode", "hermes" and "aider" keep their existing strategies$`, st.mvpGuestsUnchanged)
			sc.Step(`^no registry entry is named "([^"]*)"$`, st.noEntryNamed)

			sc.Step(`^a session with a live base URL, session key and model$`, st.liveSession)
			sc.Step(`^claude is materialized$`, st.materializeClaude)
			sc.Step(`^the launch environment carries no session key$`, st.noSessionKey)
			sc.Step(`^it carries no broker base URL$`, st.noBaseURL)
			sc.Step(`^it carries no model override$`, st.noModelOverride)
			sc.Step(`^no scratch config file is created$`, st.noScratchConfig)
			sc.Step(`^the child runs in the user's workdir, not in a scratch dir$`, st.runsInWorkdir)

			sc.Step(`^claude is materialized for a workdir with a brief$`, st.materializeWithBrief)
			sc.Step(`^the argv carries a single opening prompt$`, st.argvCarriesOnePrompt)
			sc.Step(`^that prompt names the brief file$`, st.promptNamesBrief)
			sc.Step(`^no non-interactive flag is passed$`, st.noNonInteractiveFlag)
			sc.Step(`^a session with no recorded turns$`, st.noRecordedTurns)
			sc.Step(`^no brief is referenced in the argv$`, st.noBriefInArgv)

			sc.Step(`^claude is not installed$`, st.claudeNotInstalled)
			sc.Step(`^the desk offers it with its install hint$`, st.offeredWithInstallHint)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/handoff/claude_guest.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("claude guest scenarios failed (see godog output above)")
	}
}
