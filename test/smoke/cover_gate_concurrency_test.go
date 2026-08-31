package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Several agent sessions push to this repo at once (CLAUDE.md says so in as many words),
// and the pre-push gate starts a throwaway Postgres for the money path. It used to do
// that with a FIXED container name and a FIXED host port, and to begin by force-removing
// that name:
//
//	PG_CT="rogerai-covergate-pg"
//	"$RUNTIME" rm -f "$PG_CT"
//	... -p 5466:5432 ...
//
// So a second gate starting while a first was running did not merely race it - it DELETED
// the first run's database. The victim's suite then failed with "connection refused" or
// "connection reset by peer" from a dozen unrelated packages, which reads exactly like a
// real regression and cost three pushes to diagnose.
//
// The gate is the thing that decides whether code is allowed out. It has to be safe to
// run twice at once.
// codeLines is the script with comment-only lines dropped. Every check here is about
// what the script DOES, and a comment explaining why something is avoided necessarily
// names it - which has now tripped three separate assertions in this file.
func codeLines(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func gateScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "cover-gate.sh"))
	if err != nil {
		t.Fatalf("read cover-gate.sh: %v", err)
	}
	return string(b)
}

func TestCoverGatePostgresIsPerRun(t *testing.T) {
	s := gateScript(t)

	assign := regexp.MustCompile(`PG_CT="([^"]*)"`)
	var fixed []string
	for _, m := range assign.FindAllStringSubmatch(s, -1) {
		v := m[1]
		if v == "" {
			continue // the empty initializer
		}
		if !strings.Contains(v, "$") {
			fixed = append(fixed, v)
		}
	}
	if len(fixed) > 0 {
		t.Errorf("the gate names its Postgres container with a fixed string %q, so two "+
			"concurrent runs share one container", fixed)
	}
}

func TestCoverGateDoesNotBindAFixedHostPort(t *testing.T) {
	s := gateScript(t)
	// A fixed host port fails the second run outright, or worse, points it at the first
	// run's database.
	if m := regexp.MustCompile(`-p\s+(\d+):5432`).FindStringSubmatch(s); m != nil {
		t.Errorf("the gate binds Postgres to the fixed host port %s; let the runtime pick one", m[1])
	}
}

// The per-run rename removed the only thing that swept up orphans - the EXIT trap misses
// SIGKILL and power loss - so the gate has to reclaim its own stale siblings, and only
// those.
//
// The first version of this test asserted the STRING "until=2h" was present, which passed
// whether or not the sweep worked: `--filter until=` is a podman feature and a
// docker-prune-only one, so on a CI runner the sweep errored into /dev/null and reclaimed
// nothing while this test stayed green. It asserts the discriminator instead.
func TestCoverGateReclaimsItsOwnOrphans(t *testing.T) {
	s := gateScript(t)
	code := codeLines(s)
	if !strings.Contains(code, "name=^rogerai-covergate-pg-") {
		t.Error("the gate never looks for orphaned containers, so a killed run leaks one forever")
	}
	// Liveness must be owner-agnostic. `kill -0` alone returns EPERM for another user's
	// process, which reads as "dead", so a live sibling belonging to a different user was
	// classified as an orphan and force-removed - the very failure this file exists to
	// prevent, reintroduced by the fix for it.
	if !strings.Contains(code, "/proc/$owner") && !strings.Contains(code, "ps -p") {
		t.Error("the sweep has no owner-agnostic liveness check, so another user's live run " +
			"reads as an orphan")
	}
	if strings.Contains(code, "kill -0") {
		t.Error("the sweep uses kill -0, which cannot see a process owned by another user")
	}
	for i, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "until=") {
			t.Errorf("cover-gate.sh:%d filters by age with `until=`, which docker's ps does "+
				"not support - it would silently reclaim nothing there", i+1)
		}
		if strings.Contains(line, "xargs") {
			t.Errorf("cover-gate.sh:%d pipes to xargs, whose -r is GNU-only", i+1)
		}
	}
}

// Two gates in one worktree used to write one another's coverage profile, so the loser
// measured a suite it did not run.
func TestCoverGateProfileIsPerRun(t *testing.T) {
	s := gateScript(t)
	if regexp.MustCompile(`PROFILE="\$\{COVER_PROFILE:-[^}]*\}"`).MatchString(s) {
		t.Error("the coverage profile defaults to a fixed shared path, so two runs collide")
	}
	if !strings.Contains(s, "mktemp") {
		t.Error("the coverage profile is not per-run")
	}
}

func TestCoverGateOnlyRemovesContainersItMayRemove(t *testing.T) {
	s := gateScript(t)
	// A CONTAINER removal is what matters here; `rm -f` on a temp file is not this
	// test's business, and an earlier version of it flagged exactly that.
	//
	// Two removals are legitimate: our own $PG_CT, and the sweep of containers whose
	// owning process is gone. Anything else can take a live run's database, which is the
	// failure this whole file exists to prevent.
	for i, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, `rm -f`) || !strings.Contains(line, "RUNTIME") {
			continue
		}
		ownContainer := strings.Contains(line, `"$PG_CT"`)
		orphanSweep := strings.Contains(line, `rm -f "$ct"`) // the sweep's own removal
		if !ownContainer && !orphanSweep {
			t.Errorf("cover-gate.sh:%d removes a container that is neither its own nor "+
				"an orphan of a dead run: %s", i+1, strings.TrimSpace(line))
		}
	}
	// That the allowed sweep really does discriminate by owning process is asserted from
	// the other side, in TestCoverGateReclaimsItsOwnOrphans.
}
