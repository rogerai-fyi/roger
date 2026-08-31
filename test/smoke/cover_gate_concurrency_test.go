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

func TestCoverGateDoesNotRemoveAContainerItDidNotCreate(t *testing.T) {
	s := gateScript(t)
	// Every removal must target $PG_CT and nothing else. Together with the per-run name
	// above, that means the gate can only ever remove the container it started itself.
	//
	// Source ORDER is deliberately not what is checked: the cleanup function is defined
	// before the container is started but only runs on EXIT, and an earlier version of
	// this test flagged that correct code.
	for i, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, "rm -f") {
			continue
		}
		if !strings.Contains(line, `"$PG_CT"`) {
			t.Errorf("cover-gate.sh:%d force-removes something other than its own container: %s",
				i+1, strings.TrimSpace(line))
		}
	}
}
