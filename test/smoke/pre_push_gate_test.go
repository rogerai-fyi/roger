package smoke

// pre_push_gate_test.go runs the tracked pre-push hook and asserts which gates it picks.
//
// The hook decides, from the files in the push range, whether to run the FULL coverage
// gate and whether to run the web gate. It used to decide with
//
//	if printf '%s\n' "$changed" | grep -q '\.go$'; then any_go=1; fi
//
// which is wrong under the `set -o pipefail` the same file sets. grep -q exits at the
// FIRST match, closing the pipe and SIGPIPEing printf; pipefail then makes the whole
// pipeline 141, and `if` reads 141 as "no match". So a push whose file list was long
// enough to still be streaming when grep bailed concluded there were NO .go files and
// SKIPPED the coverage gate - a gate failing OPEN, which is the exact inversion of the
// rule stated at the top of that file, and invisible, because the gate simply never
// appears in the output.
//
// This runs the REAL script with `make` and `go` stubbed out, so it observes the actual
// decision rather than a copy of the logic.
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cleanEnv drops the git variables git exports to a hook. These tests can THEMSELVES run
// inside the gate (the coverage run is part of the push they are gating), and an inherited
// GIT_DIR points at the other checkout: `git log HEAD` then answers for main while the push
// is on a branch, so the tests would silently examine the wrong history and still pass.
func cleanEnv() []string {
	// PREFIX, not a list of names. An explicit denylist rots: git also exports
	// GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_CEILING_DIRECTORIES,
	// GIT_NAMESPACE and GIT_CONFIG_*, and adds more over time. GIT_EXEC_PATH is kept
	// because it tells git where its own subcommands live and says nothing about which
	// repository to act on.
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "GIT_EXEC_PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// cleanGitEnv is the same scrub, exported within the package for the other smoke tests
// that shell out to git and can also run under the gate.
func cleanGitEnv() []string { return cleanEnv() }

// gitClean runs git with that environment scrubbed.
func gitClean(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = cleanEnv()
	return cmd.Output()
}

// stubPath builds a PATH whose `make` and `go` only announce themselves, and whose `git`
// passes everything through to the real one EXCEPT `worktree`.
//
// The hook checks out each pushed sha into a throwaway worktree to prove the COMMIT builds
// - correct for a push, ruinous for a test: a real `git worktree add` copies the whole tree
// and takes a lock on the shared .git. This repo routinely has a dozen live worktrees and
// several sessions working at once, so that lock is contended, and losing the race makes
// the hook exit before it ever prints which gate it chose - failing this test for a reason
// that has nothing to do with what it is testing.
//
// Everything the classification depends on (git diff, git merge-base) stays real.
func stubPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"make", "go"} {
		body := "#!/bin/sh\necho \"STUB " + name + " $*\"\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	// `worktree add` is skipped; `worktree remove` deletes the directory the hook made and
	// then reports failure, so the hook's own `|| rm -rf "$wt"` fallback still runs and no
	// empty temp directory is left behind. Swallowing remove silently stranded one per run.
	// `worktree add` is skipped (that is the expensive checkout), and `worktree remove`
	// deletes the directory the hook created, so nothing is stranded in /tmp. Reporting
	// success for both keeps the hook on its normal path; swallowing remove WITHOUT
	// deleting is what left an empty temp dir behind on every run.
	gitStub := "#!/bin/sh\n" +
		"if [ \"$1\" = \"worktree\" ]; then\n" +
		"  [ \"$2\" = \"remove\" ] && rm -rf \"$3\" 2>/dev/null\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitStub), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// runHook feeds the hook one push line for range base..head and returns everything it said.
func runHook(t *testing.T, base, head string) string {
	t.Helper()
	root, err := gitClean(t, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Skip("not a git checkout")
	}
	repo := strings.TrimSpace(string(root))
	hook := filepath.Join(repo, "scripts", "hooks", "pre-push")
	if _, err := os.Stat(hook); err != nil {
		t.Skipf("tracked hook not present: %v", err)
	}
	cmd := exec.Command("bash", hook, "origin", "git@example.invalid:x/y.git")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader("refs/heads/probe " + head + " refs/heads/main " + base + "\n")
	cmd.Env = append(cleanEnv(), "PATH="+stubPath(t))
	out, _ := cmd.CombinedOutput() // a non-zero exit is itself a reportable outcome below
	return string(out)
}

// commitTouching finds a real commit in this repo whose diff matches want, so the test
// exercises the hook against genuine ranges rather than invented ones.
func commitTouching(t *testing.T, want func(files []string) bool) (base, head string) {
	t.Helper()
	out, err := gitClean(t, "log", "--format=%H", "-n", "400", "HEAD")
	if err != nil {
		t.Skip("no history")
	}
	for _, sha := range strings.Fields(string(out)) {
		files, err := gitClean(t, "diff", "--name-only", sha+"^", sha)
		if err != nil {
			continue
		}
		list := strings.Fields(string(files))
		if len(list) > 0 && want(list) {
			return sha + "^", sha
		}
	}
	t.Skip("no commit in recent history matches this shape")
	return "", ""
}

func hasGo(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			return true
		}
	}
	return false
}

func hasWeb(files []string) bool {
	for _, f := range files {
		if strings.HasPrefix(f, "web/") {
			return true
		}
	}
	return false
}

func TestPrePushRunsTheFullGateWhenGoChanged(t *testing.T) {
	base, head := commitTouching(t, func(f []string) bool { return hasGo(f) })
	out := runHook(t, base, head)
	if !strings.Contains(out, "FULL coverage gate") {
		t.Errorf("a range containing .go files must run the FULL coverage gate, but the hook "+
			"chose otherwise. A gate that skips itself on a real Go change fails OPEN, which "+
			"is the inversion this test exists to catch.\n%s", out)
	}
}

func TestPrePushRunsTheWebGateWhenWebChanged(t *testing.T) {
	base, head := commitTouching(t, func(f []string) bool { return hasWeb(f) })
	out := runHook(t, base, head)
	if !strings.Contains(out, "web gate") {
		t.Errorf("a range touching web/ must run the web gate:\n%s", out)
	}
}

func TestPrePushSkipsTheFullGateWhenNoGoChanged(t *testing.T) {
	base, head := commitTouching(t, func(f []string) bool { return !hasGo(f) })
	out := runHook(t, base, head)
	if strings.Contains(out, "FULL coverage gate") {
		t.Errorf("a range with no .go files should take the fast gate, not the full one:\n%s", out)
	}
	if !strings.Contains(out, "fast gate") {
		t.Errorf("a range with no .go files should announce the fast gate:\n%s", out)
	}
}

// The decision must not depend on how MUCH changed. This is the actual regression: the old
// pipeline classified a short list correctly and a long one wrongly, so the gate quietly
// stopped running on exactly the large pushes that most needed it.
func TestPrePushClassifiesALongFileListTheSameWayAsAShortOne(t *testing.T) {
	// Build the list INSIDE the shell: 40k paths blows past ARG_MAX as an argv entry, and
	// the point is the classifier, not the harness.
	script := `set -euo pipefail
changed="$(seq 1 40000 | sed 's|^|internal/pkg/f|; s|$|.go|')"
any_go=0; any_web=0
` + classifierFromHook(t) + `
echo "go=$any_go web=$any_web"`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the classifier failed outright on a long list, which is the SIGPIPE bug: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "go=1 web=0" {
		t.Errorf("40k .go paths must classify as go=1; got %q. The old `printf | grep -q` "+
			"pipeline returned 141 here under pipefail, and the caller read that as "+
			"\"no Go files changed\" - so the coverage gate silently did not run.", got)
	}
}

// classifierFromHook lifts the two classification lines out of the tracked hook, so this
// test breaks if someone reintroduces a pipeline there rather than testing a stale copy.
func classifierFromHook(t *testing.T) string {
	t.Helper()
	root, err := gitClean(t, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Skip("not a git checkout")
	}
	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(root)), "scripts", "hooks", "pre-push"))
	if err != nil {
		t.Skipf("tracked hook not present: %v", err)
	}
	var lines []string
	for _, ln := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(ln)
		if strings.HasPrefix(s, "case \"$changed\" in") {
			lines = append(lines, s)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("expected the hook to classify with two case statements, found %d - if it is "+
			"back to `printf | grep -q`, that is the fail-open bug returning", len(lines))
	}
	return strings.Join(lines, "\n")
}
