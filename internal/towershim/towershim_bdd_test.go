package towershim

// Executable spec: features/packaging/one_binary.feature section 3, the old names for one
// release. The shims run as real processes: this test binary is copied into a temp directory as
// "roger-tower" (or "roger-tower-local") and, beside it, as "roger". Started as a shim it runs
// Main; started as roger it records what it was asked to run and exits with a chosen code.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
)

func TestMain(m *testing.M) {
	if os.Getenv("TOWERSHIM_TEST_AS") == "shim" {
		if filepath.Base(os.Args[0]) == "roger" && os.Getenv("TOWERSHIM_FAKE_ROGER") == "1" {
			// A stand-in roger: print the argv it got (one per line) and exit as told.
			fmt.Println(strings.Join(os.Args[1:], "\n"))
			code, _ := strconv.Atoi(os.Getenv("FAKE_ROGER_EXIT"))
			os.Exit(code)
		}
		os.Exit(Main(filepath.Base(os.Args[0]), os.Args[1:]))
	}
	os.Exit(m.Run())
}

type shimBDD struct {
	t       *testing.T
	dir     string
	shim    string
	stdout  string
	stderr  string
	code    int
	exit    int
	noRoger bool
	selfRog bool
}

func copyExe(t *testing.T, dst string) {
	t.Helper()
	b, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (s *shimBDD) installed(old string) error {
	s.shim = filepath.Join(s.dir, old)
	copyExe(s.t, s.shim)
	if !s.noRoger {
		copyExe(s.t, filepath.Join(s.dir, "roger"))
	}
	return nil
}

func (s *shimBDD) run(argline string) {
	args := strings.Fields(argline)
	cmd := exec.Command(s.shim, args...)
	env := []string{"TOWERSHIM_TEST_AS=shim", "FAKE_ROGER_EXIT=" + strconv.Itoa(s.exit)}
	if !s.selfRog {
		env = append(env, "TOWERSHIM_FAKE_ROGER=1")
	}
	if s.noRoger {
		env = append(env, "PATH="+s.dir)
	} else {
		env = append(env, "PATH="+os.Getenv("PATH"))
	}
	cmd.Env = env
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	s.code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		s.code = ee.ExitCode()
	} else if err != nil {
		s.code = -1
		e.WriteString(err.Error())
	}
	s.stdout, s.stderr = o.String(), e.String()
}

// The stand-in roger is the same test binary; it is told to behave as roger through the
// environment the shim passes on (the shim keeps the caller's environment).
// operatorRuns is every "the operator runs <old> <args>" step. A pipe to wc -l is the shell's
// part; the shim's own stdout is what it counts, compared in the Then.
func (s *shimBDD) operatorRuns(old, args string) error {
	if s.shim == "" {
		if err := s.installed(old); err != nil {
			return err
		}
	}
	if !s.noRoger && !s.selfRog {
		s.exit = 3 // a distinctive code to prove pass-through
	}
	s.run(strings.TrimSuffix(args, " | wc -l"))
	return nil
}

func (s *shimBDD) rogerRanWith(newArgs string) error {
	want := strings.Join(append([]string{"tower"}, strings.Fields(newArgs)...), "\n")
	if strings.TrimSpace(s.stdout) != want {
		return fmt.Errorf("roger was run with %q, want %q (stderr %q)", s.stdout, want, s.stderr)
	}
	return nil
}

func (s *shimBDD) noticeFirst(old, now string) error {
	first, _, _ := strings.Cut(s.stderr, "\n")
	want := fmt.Sprintf("%s is now `%s`; this name is removed in the next release", old, now)
	if first != want {
		return fmt.Errorf("stderr starts %q, want %q", first, want)
	}
	return nil
}

func (s *shimBDD) sameExitCode() error {
	if s.code != s.exit {
		return fmt.Errorf("the shim exited %d, roger exited %d", s.code, s.exit)
	}
	return nil
}

func (s *shimBDD) stdoutIsRogers() error {
	if strings.Contains(s.stdout, "is now `") {
		return fmt.Errorf("the deprecation notice reached stdout: %q", s.stdout)
	}
	return nil
}

// countsEqual: stdout carries exactly what roger printed (its argv, one per line), so a pipe
// counts the same lines with or without the shim; the notice is on stderr only.
func (s *shimBDD) countsEqual() error {
	if want := "tower\nstations\n--dir\nD\n"; s.stdout != want {
		return fmt.Errorf("stdout %q, want exactly roger's %q", s.stdout, want)
	}
	return nil
}

func (s *shimBDD) rogerMissing() error {
	s.noRoger = true
	return s.installed("roger-tower")
}

func (s *shimBDD) exits127() error {
	if s.code != 127 {
		return fmt.Errorf("exit %d, want 127 (stderr %q)", s.code, s.stderr)
	}
	return nil
}

func (s *shimBDD) saysNotInstalled(want string) error {
	if !strings.Contains(s.stderr, want) {
		return fmt.Errorf("stderr %q does not say %q", s.stderr, want)
	}
	return nil
}

func (s *shimBDD) rogerIsShim() error {
	// "roger" beside the shim is itself a shim: it would hand off to itself forever.
	s.shim = filepath.Join(s.dir, "roger-tower")
	copyExe(s.t, s.shim)
	copyExe(s.t, filepath.Join(s.dir, "roger"))
	s.selfRog = true
	return nil
}

func (s *shimBDD) shimExecsRoger() error {
	// Both copies start as shims (TOWERSHIM_TEST_AS=shim is inherited and the stand-in roger
	// switch is off), so the hand-off would recurse without the guard.
	s.run("status --dir D")
	return nil
}

func (s *shimBDD) detectsLoop() error {
	if s.code == 0 || !strings.Contains(s.stderr, "loop") {
		return fmt.Errorf("exit %d stderr %q; want a refused loop", s.code, s.stderr)
	}
	return nil
}

func TestShimsBDD(t *testing.T) {
	s := &shimBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = shimBDD{t: t, dir: t.TempDir()}
				return ctx, nil
			})
			sc.Step(`^roger and the "([^"]*)" shim are installed$`, s.installed)
			sc.Step(`^the operator runs "(roger-tower(?:-local)?) ([^"]*)"$`, s.operatorRuns)
			sc.Step(`^"roger tower ([^"]*)" runs with the same arguments$`, s.rogerRanWith)
			sc.Step("^stderr starts with one line: \"(\\S+) is now `([^`]*)`; this name is removed in the next release\"$", s.noticeFirst)
			sc.Step(`^the exit code is the exit code of "roger tower [^"]*"$`, s.sameExitCode)
			sc.Step(`^stdout is exactly what "roger tower [^"]*" printed$`, s.stdoutIsRogers)
			sc.Step(`^the count is the same as "roger tower stations --dir D \| wc -l"$`, s.countsEqual)
			sc.Step(`^the roger-tower shim is installed but roger is not on PATH nor beside the shim$`, s.rogerMissing)
			sc.Step(`^it exits 127$`, s.exits127)
			sc.Step("^it says \"(roger is not installed; install roger, then run `roger tower status`)\"$", s.saysNotInstalled)
			sc.Step(`^a roger binary that is itself the roger-tower shim$`, s.rogerIsShim)
			sc.Step(`^the shim execs roger$`, s.shimExecsRoger)
			sc.Step(`^it detects the loop and exits non-zero instead of exec-ing itself again$`, s.detectsLoop)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:189",
				"../../features/packaging/one_binary.feature:206",
				"../../features/packaging/one_binary.feature:210",
				"../../features/packaging/one_binary.feature:216",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature section 3 (the old names) failed")
	}
}
