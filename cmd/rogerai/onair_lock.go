package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// On-air single-instance guard.
//
// `roger share` registers a node (<station>-<model>) with the broker and serves
// forever. If a second `roger share` starts with the SAME node id it double-
// registers - the broker then sees one station flapping between two upstreams and
// bridge tokens, which breaks routing and, for a priced node, scrambles earnings
// attribution. A per-node-id lockfile lets the second invocation DETECT the live
// session and bow out cleanly. The lock is keyed on the node id, NOT the machine,
// so a multi-model rig can still run `roger share modelA` and `roger share modelB`
// side by side (distinct node ids => distinct locks => no false collision).
//
// The lock is advisory (a cooperative file, not a kernel lock): a lock left behind
// by a crashed daemon is reclaimed once its PID is no longer alive, and the error
// message names the lock path so a stuck operator can always remove it by hand.

type onAirInfo struct {
	PID     int    `json:"pid"`
	Station string `json:"station"`
	Model   string `json:"model"`
	Started int64  `json:"started"` // unix seconds, for diagnostics
}

// lockFileChars keeps a node id filesystem-safe for use in a lock filename.
func lockSlug(nodeID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, nodeID)
}

// onAirLockPath is the cooperative lock for one node id, alongside config.json in
// the brand-named config dir (~/.config/rogerai). One lock per node id = one daemon
// per <station>-<model>.
func onAirLockPath(nodeID string) string {
	return filepath.Join(filepath.Dir(configPath()), "share-"+lockSlug(nodeID)+".lock")
}

// acquireOnAirLock claims the on-air lock for this node id. If a LIVE session
// already holds it, it returns an error describing that session; a STALE lock
// (owning PID gone, or our own) is reclaimed. The returned release func removes
// the lock, but only while it is still ours - so it never deletes a newer daemon's.
func acquireOnAirLock(nodeID, station, model string) (release func(), err error) {
	path := onAirLockPath(nodeID)
	if b, rerr := os.ReadFile(path); rerr == nil {
		var prev onAirInfo
		if json.Unmarshal(b, &prev) == nil && prev.PID > 0 && prev.PID != os.Getpid() && processAlive(prev.PID) {
			where := prev.Station
			if where == "" {
				where = "this machine"
			}
			return nil, fmt.Errorf("already on air: %q is broadcasting (pid %d) - one `roger share` per machine. Stop that session first, or if nothing is actually running, delete the stale lock:\n  %s", where, prev.PID, path)
		}
		// stale (dead PID) or ours: fall through and take it over.
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	info := onAirInfo{PID: os.Getpid(), Station: station, Model: model, Started: time.Now().Unix()}
	b, _ := json.Marshal(info)
	if err := os.WriteFile(path, b, 0600); err != nil {
		return nil, err
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			// Only remove the lock if it is still OURS: a slow shutdown must not
			// delete a fresh daemon that already reclaimed a stale lock from us.
			if b, rerr := os.ReadFile(path); rerr == nil {
				var cur onAirInfo
				if json.Unmarshal(b, &cur) == nil && cur.PID != os.Getpid() {
					return
				}
			}
			_ = os.Remove(path)
		})
	}

	// `roger share` blocks on `select {}` and is normally ended by Ctrl-C / SIGTERM,
	// which would skip a deferred release and leave a stale lock behind. Clear it on
	// those signals, then re-raise the default disposition so the exit looks normal.
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
