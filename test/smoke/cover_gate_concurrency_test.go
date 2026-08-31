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
// those. Age is the discriminator: a run going for two hours is not running.
func TestCoverGateReclaimsItsOwnOrphans(t *testing.T) {
	s := gateScript(t)
	if !strings.Contains(s, "name=^rogerai-covergate-pg-") {
		t.Error("the gate never sweeps up orphaned containers, so a killed run leaks one forever")
	}
	if !strings.Contains(s, "until=") {
		t.Error("the orphan sweep is not bounded by age, so it can remove a live sibling")
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
	// Two removals are legitimate: our own $PG_CT, and the age-bounded sweep of orphaned
	// siblings. Anything else can take a live run's database, which is the failure this
	// whole file exists to prevent.
	for i, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, `rm -f`) || !strings.Contains(line, "RUNTIME") {
			continue
		}
		ownContainer := strings.Contains(line, `"$PG_CT"`)
		agedSweep := strings.Contains(line, "xargs")
		if !ownContainer && !agedSweep {
			t.Errorf("cover-gate.sh:%d removes a container that is neither its own nor a "+
				"stale orphan: %s", i+1, strings.TrimSpace(line))
		}
	}
	// And the sweep that is allowed must actually be bounded, which the orphan test
	// asserts from the other side.
}
