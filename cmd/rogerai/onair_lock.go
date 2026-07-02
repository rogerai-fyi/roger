package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/rogerai-fyi/roger/internal/onair"
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
		release()
		signal.Stop(c)
		os.Exit(130) // 128 + SIGINT, the conventional Ctrl-C exit code
	}()
	return release, nil
}
