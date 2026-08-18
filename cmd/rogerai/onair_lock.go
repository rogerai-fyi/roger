package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"rogerai.fm/roger/v5/internal/onair"
)

// On-air single-instance guard.
//
// The cooperative per-node-id lock itself lives in internal/onair so that EVERY
// front-end shares ONE lock: this headless `roger share` path AND the TUI/web-console
// controller toggle (internal/node startLocked). Before the move only this path took
// the lock, so an abandoned TUI share and a headless daemon could double-broadcast
// one node id and rotate each other's bridge tokens (the 2026-07-02
// eager-puma-54-voice incident; see features/sharing/on_air_lock.feature).

// onAirInfo is the on-disk lock content (aliased so the CLI's tests and any callers
// keep their names).
type onAirInfo = onair.Info

// onAirLockPath is the cooperative lock file for one node id.
func onAirLockPath(nodeID string) string { return onair.LockPath(nodeID) }

// processAlive reports whether a PID is currently running (platform probe).
func processAlive(pid int) bool { return onair.ProcessAlive(pid) }

// shareShutdown is the ONE process-wide answer to "the operator ended this share". The hook
// in acquireOnAirLock cancels it on SIGINT/SIGTERM, immediately before it clears the lock and
// exits; anything else in the process that runs for the life of a share (today: the
// relay-fabric join) waits on this rather than registering a notifier of its own.
//
// WHY IT IS SHARED RATHER THAN ONE PER COMPONENT. Registering ANY signal channel disables
// Go's default "SIGINT kills the program" disposition for the whole program, not for the
// registering package. So a component that installs its own notifier and only cancels its own
// context is quietly betting that some other component still calls os.Exit - a bet that is
// invisible at the call site, and one whose failure mode is the operator pressing Ctrl-C and
// watching nothing happen. `roger share` blocks on select{} forever, so there is no other
// stopping mechanism to fall back on. One registration, one exit, and every long-lived part
// of a share hears about it through this context.
var shareShutdown, endShareShutdown = context.WithCancel(context.Background())

// acquireOnAirLock claims the on-air lock for this node id (see onair.Acquire for
// the live/stale semantics) and layers the DAEMON-specific signal hook on top:
// `roger share` blocks on `select {}` and is normally ended by Ctrl-C / SIGTERM,
// which would skip a deferred release and leave a stale lock behind. Clear it on
// those signals, then exit with the conventional Ctrl-C code.
func acquireOnAirLock(nodeID, station, model string) (release func(), err error) {
	release, err = onair.Acquire(nodeID, station, model)
	if err != nil {
		return nil, err
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		// Tell the rest of the share first, then clear the lock, then go. The notice is
		// deliberately not WAITED on: a second Ctrl-C is what an operator does when the first
		// looks ignored, so the exit may not be held up for a background plane to unwind. What
		// this buys is that a hub poll in flight sees a cancelled context instead of being cut
		// mid-call, which is worth the nanoseconds it costs.
		endShareShutdown()
		release()
		signal.Stop(c)
		os.Exit(130) // 128 + SIGINT, the conventional Ctrl-C exit code
	}()
	return release, nil
}
