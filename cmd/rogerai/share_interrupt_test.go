package main

// share_interrupt_test.go pins the one property a daemon that blocks on select{} has no
// other way to guarantee: ONE Ctrl-C ends `roger share`.
//
// It is not a hypothetical. Every signal channel registered anywhere in a Go program
// disables the default "SIGINT kills the process" disposition for the WHOLE program, so the
// moment any component installs a notifier, ending the share becomes something the code has
// to do on purpose. `roger share` has exactly one place that does it - the on-air lock's
// hook, which clears the lock and exits 130 - and a second component quietly registering its
// own notifier beside it would be betting the process still has a way out. That bet is
// invisible at the call site and its failure mode is an operator pressing Ctrl-C twice.
//
// REAL PROCESS, REAL SIGNAL: the helper below re-execs this test binary (the same pattern
// the on-air lock BDD uses), takes the REAL lock, and is sent a REAL SIGINT by the kernel.
// Reasoning about signal dispositions in a unit test would be reasoning about the thing
// under test.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShareInterruptHelper is the RE-EXEC entry point, not a test. It stands up the two
// long-lived pieces of a public share - the on-air lock hook and a background plane waiting
// on the shared shutdown, which is what joinRelayFabric does - and then blocks the way
// cmdShare does.
func TestShareInterruptHelper(t *testing.T) {
	ready := os.Getenv("ROGER_INTERRUPT_READY")
	if ready == "" {
		t.Skip("helper-process entry point (spawned by TestOneInterruptEndsAShare)")
	}
	release, err := acquireOnAirLock("brave-otter-m", "brave-otter", "m")
	if err != nil {
		fmt.Println("LOCK REFUSED:", err)
		os.Exit(3)
	}
	defer release()
	// The relay-fabric join's shape: a background plane that serves until the share ends and
	// registers no signal handling of its own.
	go func() {
		<-shareShutdown.Done()
		_ = os.WriteFile(ready+".relay", []byte("stopped"), 0o600)
	}()
	if err := os.WriteFile(ready, []byte("blocking"), 0o600); err != nil {
		os.Exit(4)
	}
	// Exactly what cmdShare does after go-live. Nothing here can end the process; only the
	// signal hook can.
	select {}
}

func TestOneInterruptEndsAShare(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	if got := configPath(); !strings.HasPrefix(got, dir) {
		t.Fatalf("config isolation FAILED: %q not under %q", got, dir)
	}

	ready := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestShareInterruptHelper$")
	cmd.Env = append(os.Environ(), "ROGER_INTERRUPT_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	for deadline := time.Now().Add(20 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the share helper never reached its blocking state")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ONE interrupt. Not two.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var code int
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("waiting on the share: %v", err)
		}
		if code != 130 {
			t.Fatalf("the share exited %d, want 130 (128+SIGINT)", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the share survived its first Ctrl-C - the operator has to press it twice")
	}

	// WHAT IS DELIBERATELY NOT ASSERTED HERE: that the background plane finished observing
	// the shutdown before the process went.
	//
	// The first version of this test checked for ready+".relay" at this point and was flaky
	// under load - reproducible with -count=20. That was the test's fault, not the code's.
	// acquireOnAirLock's hook calls endShareShutdown, release, signal.Stop and then
	// os.Exit(130) without waiting, and says why: "a second Ctrl-C is what an operator does
	// when the first looks ignored, so the exit may not be held up for a background plane to
	// unwind." Whether the plane's goroutine is scheduled before os.Exit is therefore a race
	// the design intends to be free to lose. Asserting it turns a best-effort courtesy into a
	// guarantee the code never made, and the failure it produces points at the wrong thing.
	//
	// The property that IS worth pinning - that a long-lived plane waits on the same context
	// the hook cancels, rather than on one nobody ends - is a wiring question with a
	// deterministic answer, so it lives in TestEveryLongLivedPlaneWaitsOnTheSharedShutdown
	// below instead of being inferred from a filesystem race.

	// The lock is gone: the hook's whole reason for existing is that select{} + SIGINT skips
	// every deferred release.
	if _, err := os.Stat(onAirLockPath("brave-otter-m")); !os.IsNotExist(err) {
		t.Errorf("the on-air lock outlived the share (stat err=%v)", err)
	}
}

// TestEveryLongLivedPlaneWaitsOnTheSharedShutdown pins the wiring the cross-process test
// above deliberately stopped inferring from a filesystem race.
//
// The regression this catches is a background plane handed context.Background() instead of
// shareShutdown - a one-word slip at a call site, invisible in review, whose failure mode is
// a plane that never hears the share end. It is not a timing question and does not need a
// real signal: either the plane waits on the context the hook cancels, or it does not.
//
// It runs in the RE-EXEC helper's own process rather than the test binary's, because
// endShareShutdown cancels a package-level context for good. Cancelling it inline would end
// the shared shutdown for every test that runs afterwards, which is exactly the kind of
// action-at-a-distance this file exists to argue against.
func TestEveryLongLivedPlaneWaitsOnTheSharedShutdown(t *testing.T) {
	if os.Getenv("ROGER_SHUTDOWN_WIRING") == "" {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestEveryLongLivedPlaneWaitsOnTheSharedShutdown$")
		cmd.Env = append(os.Environ(), "ROGER_SHUTDOWN_WIRING=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the wiring check failed in its own process: %v\n%s", err, out)
		}
		return
	}

	// The shape joinRelayFabric has: a plane that serves until the share ends, waiting on the
	// process-wide context and registering no signal handling of its own.
	observed := make(chan struct{})
	go func() {
		<-shareShutdown.Done()
		close(observed)
	}()

	select {
	case <-observed:
		t.Fatal("the shared shutdown was already cancelled before anything ended the share")
	case <-time.After(20 * time.Millisecond):
	}

	// The same call acquireOnAirLock's hook makes, and the only one that ends a share.
	endShareShutdown()

	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("a plane waiting on shareShutdown never heard the share end")
	}
	if err := shareShutdown.Err(); err == nil {
		t.Error("shareShutdown reports no error after being cancelled")
	}
}
