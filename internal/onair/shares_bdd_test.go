package onair_test

// Executable spec: features/packaging/one_binary.feature section 7.9, one live-share registry.
// A share's owner-only LAN record lives in internal/onair beside the on-air locks, with the
// locks' one staleness rule; records written by the previous release are read where they are.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/onair"
)

func repoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

// livePID is a real separate process that stays up for the scenario.
func livePID(t *testing.T) int {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep binary: cannot stand up a separate process")
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("no true binary")
	}
	return cmd.Process.Pid
}

type sharesBDD struct {
	t    *testing.T
	root string
	live int
	dead int
}

func (s *sharesBDD) reset() {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	s.live, s.dead = 0, 0
}

func (s *sharesBDD) liveShareElsewhere() error {
	s.live = livePID(s.t)
	s.dead = deadPID(s.t)
	if _, err := onair.WriteShare(onair.Share{PID: s.live, Band: "qwen3-8b", ChatURL: "http://127.0.0.1:11434/v1/chat/completions", Key: "up-key"}); err != nil {
		return err
	}
	_, err := onair.WriteShare(onair.Share{PID: s.dead, Band: "old-band", ChatURL: "http://127.0.0.1:9/v1/chat/completions"})
	return err
}

func (s *sharesBDD) readThroughOnair() error {
	for _, f := range []string{"cmd/rogerai/lan.go", "cmd/rogerai/edgeinstance.go"} {
		src, err := os.ReadFile(filepath.Join(s.root, f))
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "onair.Shares()") {
			return fmt.Errorf("%s does not read live shares through internal/onair", f)
		}
	}
	// No second registry: the CLI's own lan-shares reader is gone.
	src, err := os.ReadFile(filepath.Join(s.root, "cmd/rogerai/lanshareself.go"))
	if err == nil && regexp.MustCompile(`func lanShares\(`).Match(src) {
		return fmt.Errorf("cmd/rogerai still keeps its own lan-shares registry")
	}
	got := onair.Shares()
	if len(got) != 1 || got[0].PID != s.live || got[0].Band != "qwen3-8b" {
		return fmt.Errorf("live shares %+v, want the one live share", got)
	}
	return nil
}

func (s *sharesBDD) staleRemoved() error {
	if _, err := os.Stat(onair.ShareFile(s.dead)); !os.IsNotExist(err) {
		return fmt.Errorf("the dead pid's record was not removed (%v)", err)
	}
	return nil
}

func (s *sharesBDD) perms() error {
	fi, err := os.Stat(onair.ShareFile(s.live))
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("record mode %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(onair.SharesDir())
	if err != nil {
		return err
	}
	if di.Mode().Perm() != 0o700 {
		return fmt.Errorf("directory mode %v, want 0700", di.Mode().Perm())
	}
	return nil
}

// legacy writes a record exactly as the previous release did (cmd/rogerai's lan-shares files).
func (s *sharesBDD) legacy() error {
	s.live, s.dead = livePID(s.t), deadPID(s.t)
	dir, _ := os.UserConfigDir()
	ls := filepath.Join(dir, "rogerai", "lan-shares")
	if err := os.MkdirAll(ls, 0o700); err != nil {
		return err
	}
	for pid, band := range map[int]string{s.live: "qwen3-8b", s.dead: "old-band"} {
		b, _ := json.Marshal(map[string]any{"pid": pid, "band": band, "chat_url": "http://127.0.0.1:11434/v1/chat/completions"})
		if err := os.WriteFile(filepath.Join(ls, fmt.Sprintf("share-%d.json", pid)), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (s *sharesBDD) liveReadStaleRemoved() error {
	got := onair.Shares()
	if len(got) != 1 || got[0].PID != s.live {
		return fmt.Errorf("after the upgrade the live shares are %+v, want the one live record", got)
	}
	return s.staleRemoved()
}

func TestOneLiveShareRegistryBDD(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := &sharesBDD{t: t, root: root}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.Step(`^a share on air in another process$`, s.liveShareElsewhere)
			sc.Step(`^the LAN relay, the Edge face and the TUI read it through internal/onair$`, s.readThroughOnair)
			sc.Step(`^a stale record \(dead pid\) is ignored and removed by one rule$`, s.staleRemoved)
			sc.Step(`^the registry files stay 0600 in a 0700 directory$`, s.perms)
			sc.Step(`^lan-shares/share-<pid>\.json from the previous release$`, s.legacy)
			sc.Step(`^roger starts$`, func() error { return nil })
			sc.Step(`^a live one is read; a stale one is removed$`, s.liveReadStaleRemoved)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:571",
				"../../features/packaging/one_binary.feature:580",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature section 7.9 failed")
	}
}
