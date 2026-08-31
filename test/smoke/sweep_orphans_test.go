package smoke

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The sweep decides which of the gate's throwaway Postgres containers may be removed, and
// getting it wrong in either direction is expensive: too eager deletes a live run's
// database (three of my pushes failed that way before it was found), too shy leaks a
// container and a port forever.
//
// It shipped broken once in a way NO string-matching test could see: `while IFS= read -r
// ct state` never split the two fields, so state was always empty, every container was
// skipped, and the sweep silently did nothing at all. Which is why the decision is its own
// script now, reading stdin and printing names - so it can be exercised for real, without
// a container runtime.
func runSweep(t *testing.T, ns, stdin string) []string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "sweep-orphans.sh"))
	cmd.Env = append(cmd.Environ(), "PG_NS="+ns)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sweep-orphans.sh: %v", err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

func TestSweepOrphansDecides(t *testing.T) {
	// pid 1 is alive and owned by root, so it also covers the EPERM case that made
	// `kill -0` classify another user's live run as an orphan.
	//
	// Each fixture is chosen so exactly ONE rule decides it: remove that rule and this
	// list changes. A fixture decided by two rules tests neither.
	const input = "" +
		"rogerai-covergate-pg-42-1\n" + // ours, owner alive                  -> keep
		"rogerai-covergate-pg-42-999999\n" + // ours, owner gone              -> reclaim
		"rogerai-covergate-pg-99-999999\n" + // another PID namespace, and a pid dead
		//                                       HERE: only the namespace check keeps it
		"rogerai-covergate-pg-42-notapid\n" + // unparsable owner             -> keep
		"some-other-container\n" + // not ours at all                         -> keep
		// Somebody else's container, named so that stripping our prefix would be a no-op
		// and leave a namespace and pid that both look like ours. Only the prefix guard
		// keeps it, which is the point: another project's container is not ours to remove.
		"42-999999\n"

	got := runSweep(t, "42", input)
	want := []string{"rogerai-covergate-pg-42-999999"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sweep reclaimed %v, want %v", got, want)
	}
}

// The window that made an earlier version dangerous. `run -d` reports "created" between
// create and start, and a rule that reclaimed anything not running was tested BEFORE the
// liveness check - so a concurrently starting gate would have deleted a live sibling's
// database in that window. State is not consulted at all now, and this pins that: a
// container whose owner is alive is never taken, whatever it is doing.
func TestSweepOrphansNeverTakesAContainerMidStartup(t *testing.T) {
	for _, extra := range []string{"", " created", " running", " exited"} {
		line := "rogerai-covergate-pg-42-1" + extra + "\n"
		if got := runSweep(t, "42", line); len(got) != 0 {
			t.Errorf("sweep would remove %q, whose owner is alive", got)
		}
	}
}

// The failure that actually shipped: with the fields unsplit, every line was skipped.
func TestSweepOrphansIsNotSilentlyInert(t *testing.T) {
	got := runSweep(t, "42", "rogerai-covergate-pg-42-999999\n")
	if len(got) == 0 {
		t.Error("the sweep reclaimed nothing at all from an obvious orphan - it is inert")
	}
}

// Never a live sibling, whatever the reason it looks unfamiliar.
func TestSweepOrphansNeverTakesALiveRun(t *testing.T) {
	for _, line := range []string{
		"rogerai-covergate-pg-42-1 running\n",      // alive, other user
		"rogerai-covergate-pg-99-1 running\n",      // alive, other namespace
		"rogerai-covergate-pg-99-999999 running\n", // other namespace, pid dead here
	} {
		if got := runSweep(t, "42", line); len(got) != 0 {
			t.Errorf("sweep would remove %q, which may be a live run", got)
		}
	}
}
