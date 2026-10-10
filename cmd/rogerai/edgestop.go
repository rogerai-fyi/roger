package main

// STOPPING AND RESTARTING a roger on THIS machine, asked for by a member that holds a maintain grant
// (features/edge/maintenance.feature). The two locks are checked before any of this runs.
//
// Signalling a process by number is how a pid gets reused under you, so a stop signals nothing it
// cannot vouch for. ALL of these must hold:
//   - the household registration naming the pid is FRESH (its heartbeat is inside InstanceLife), so
//     the process it describes is the process still beating;
//   - the pid is not this process, because a roger cannot kill the face answering the request;
//   - on Linux the pid's own command line still names a roger.
//
// It sends SIGTERM and never SIGKILL: a roger that will not leave is reported, not forced.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

// edgeStopWait is how long a stop waits for the registration to go before it answers. A stop that
// waited forever would hold the face; one that did not wait at all could never say it worked.
const edgeStopWait = 3 * time.Second

// edgeSignal is the seam a test uses instead of really signalling a process.
var edgeSignal = func(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// edgeProcIsRoger reports whether a pid's own command line still names a roger, so a reused pid is
// not signalled. On a system with no /proc it cannot tell, and says so rather than guessing yes.
var edgeProcIsRoger = func(pid int) (bool, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "cmdline"))
	if err != nil {
		return false, false // cannot tell: not Linux, or the process is gone
	}
	return cmdlineIsRoger(b), true
}

// cmdlineIsRoger reports whether a /proc cmdline names a roger. An empty one (a zombie, a kernel
// thread) names nothing, so it is not a roger.
func cmdlineIsRoger(b []byte) bool {
	fields := strings.Fields(strings.ReplaceAll(string(b), "\x00", " "))
	if len(fields) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(filepath.Base(fields[0])), "roger")
}

// edgeStopTarget finds the LIVE registration for an agent and says whether it may be signalled. It
// reads the live household, so a registration that stopped beating is not there at all: a process
// that is not beating reads as gone here exactly as it does on every other surface (the fleet draws
// it DARK), and nothing is signalled on the strength of a pid nobody can vouch for.
//
// An empty refusal with pid 0 means it is not running, which is not a failure: the owner asked for an
// end state and it is already so.
func edgeStopTarget(name string, now time.Time) (pid int, refused string) {
	for _, in := range edge.LiveHousehold(edgeInstancesDir(), now) {
		if in.Name != name {
			continue
		}
		if in.PID <= 0 {
			return 0, fmt.Sprintf("%q was registered by a roger too old to record its process, so there is nothing safe to signal - it stops when it is restarted", name)
		}
		if in.PID == os.Getpid() {
			return 0, "that is the roger answering this request: a process cannot stop itself through its own face"
		}
		if isRoger, known := edgeProcIsRoger(in.PID); known && !isRoger {
			return 0, fmt.Sprintf("the process %q registered is not a roger any more, so nothing was signalled", name)
		}
		return in.PID, ""
	}
	return 0, "" // not running
}

// edgeStopAgent asks a running agent to stop. changed is false when it was not running.
func edgeStopAgent(name string, now time.Time) (says string, changed bool, refused string) {
	if !edge.IsPersistent(edgePersistDir(), name) {
		return "", false, fmt.Sprintf("no persistent agent named %q - `roger edge agent` lists them", name)
	}
	pid, refused := edgeStopTarget(name, now)
	if refused != "" {
		return "", false, refused
	}
	if pid == 0 {
		return fmt.Sprintf("%q is not running - nothing to stop.", name), false, ""
	}
	if err := edgeSignal(pid, syscall.SIGTERM); err != nil {
		return "", false, fmt.Sprintf("could not ask %q to stop: %v", name, err)
	}
	// Wait a bounded time for it to leave the household, and say plainly whether it went.
	deadline := time.Now().Add(edgeStopWait)
	for time.Now().Before(deadline) {
		if !edgeStillRunning(name, time.Now()) {
			return fmt.Sprintf("asked %q to stop, and it has gone.", name), true, ""
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("asked %q to stop; it had not gone yet when this was answered. It is SIGTERM, never SIGKILL, so it leaves in its own time.", name), true, ""
}

// edgeStillRunning reports whether a name is still in the LIVE household: what the stop waits on, and
// what decides whether a restart may go on to start it. A roger that exits cleanly deregisters, so
// the file goes at once; one that is killed leaves it until the reaper, and "still running" then says
// what every other surface says until it is reaped.
func edgeStillRunning(name string, now time.Time) bool {
	for _, in := range edge.LiveHousehold(edgeInstancesDir(), now) {
		if in.Name == name {
			return true
		}
	}
	return false
}

// edgeRestartAgent is a stop then a start, and says which half did what. A stop that refuses stops
// the whole thing: nothing is launched on top of a process that is still there.
func edgeRestartAgent(cfg config, name string, now time.Time) (says string, changed bool, refused string) {
	stopSays, stopped, refused := edgeStopAgent(name, now)
	if refused != "" {
		return "", false, refused
	}
	// A roger that has not gone yet is NOT started on top of itself, and the answer says so rather
	// than reporting a restart that did not happen. SIGTERM was sent; asking again in a moment works.
	if stopped && edgeStillRunning(name, time.Now()) {
		return fmt.Sprintf("asked %q to stop, and it had not gone when this was answered, so it was NOT started again - ask again in a moment.", name), true, ""
	}
	startSays, err := edgeAgentResumed(cfg, name)
	if err != nil {
		return "", stopped, fmt.Sprintf("%s but it could not be started again: %v", strings.TrimSuffix(stopSays, "."), err)
	}
	if !stopped {
		return startSays, true, "" // it was not running: a restart is simply a start
	}
	return strings.TrimSuffix(stopSays, ".") + " " + startSays, true, ""
}

// edgeStartAgent launches a persistent agent that is down and WAITS briefly for it to register, so
// the answer says whether it is really running rather than only that it was launched. A spawned agent
// that cannot stand up its face exits at once, and the owner deserves to hear that from the action
// they asked for (features/edge/maintenance.feature).
func edgeStartAgent(cfg config, name string, now time.Time) (says string, changed bool, refused string) {
	msg, err := edgeAgentResumed(cfg, name)
	if err != nil {
		return "", false, err.Error()
	}
	if strings.Contains(msg, "already running") {
		return msg, false, ""
	}
	deadline := time.Now().Add(edgeStartWait)
	for time.Now().Before(deadline) {
		if edgeStillRunning(name, time.Now()) {
			return fmt.Sprintf("started %q, and it is on your Edge.", name), true, ""
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Sprintf("launched a roger for %q, but it had not joined your Edge %s later - look at that machine's log (`roger edge logs`) for why.",
		name, edgeStartWait), true, ""
}

// edgeStartWait is how long a start waits for the agent to register before it answers. Long enough
// for a roger to stand up its face, short enough that the caller is not left holding the request.
const edgeStartWait = 6 * time.Second
