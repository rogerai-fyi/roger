// Package onair is the cooperative per-node-id ON-AIR lock shared by EVERY front-end
// that can put a node id on the air: the headless `roger share` daemon (cmd/rogerai)
// AND the TUI/web-console share toggle (internal/node's controller).
//
// If two processes broadcast the SAME node id (<station>-<model>) the broker sees one
// station flapping between two upstreams and bridge tokens, which breaks routing and,
// for a priced node, scrambles earnings attribution (the 2026-07-02
// eager-puma-54-voice incident: an abandoned TUI share and a systemd unit rotated each
// other's tokens forever). A per-node-id lockfile lets the second broadcaster DETECT
// the live session and bow out cleanly. The lock is keyed on the node id, NOT the
// machine, so a multi-model rig still runs several shares side by side (distinct node
// ids => distinct locks => no false collision).
//
// The lock is advisory (a cooperative file, not a kernel lock): a lock left behind by
// a crashed process is reclaimed once its PID is no longer alive, and the error
// message names the lock path so a stuck operator can always remove it by hand.
package onair

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Info is the on-disk lock content: who is broadcasting this node id.
type Info struct {
	PID     int    `json:"pid"`
	Station string `json:"station"`
	Model   string `json:"model"`
	Started int64  `json:"started"` // unix seconds, for diagnostics
}

// lockSlug keeps a node id filesystem-safe for use in a lock filename.
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

// LockPath is the cooperative lock for one node id, alongside config.json in the
// brand-named config dir (<UserConfigDir>/rogerai). One lock per node id = one
// broadcaster per <station>-<model>.
func LockPath(nodeID string) string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rogerai", "share-"+lockSlug(nodeID)+".lock")
}

// Acquire claims the on-air lock for this node id. If a LIVE session already holds
// it, it returns an error describing that session; a STALE lock (owning PID gone, or
// our own) is reclaimed. The returned release func removes the lock, but only while
// it is still ours - so it never deletes a newer broadcaster's.
//
// Acquire installs NO signal handling: the headless daemon layers its own
// SIGINT/SIGTERM lock-clearing exit hook on top (cmd/rogerai acquireOnAirLock), while
// the controller releases through its stop paths and relies on PID-staleness reclaim
// if the host process dies.
func Acquire(nodeID, station, model string) (release func(), err error) {
	path := LockPath(nodeID)
	if b, rerr := os.ReadFile(path); rerr == nil {
		var prev Info
		if json.Unmarshal(b, &prev) == nil && prev.PID > 0 && prev.PID != os.Getpid() && ProcessAlive(prev.PID) {
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
	info := Info{PID: os.Getpid(), Station: station, Model: model, Started: time.Now().Unix()}
	b, _ := json.Marshal(info)
	if err := os.WriteFile(path, b, 0600); err != nil {
		return nil, err
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			// Only remove the lock if it is still OURS: a slow shutdown must not
			// delete a fresh broadcaster that already reclaimed a stale lock from us.
			if b, rerr := os.ReadFile(path); rerr == nil {
				var cur Info
				if json.Unmarshal(b, &cur) == nil && cur.PID != os.Getpid() {
					return
				}
			}
			_ = os.Remove(path)
		})
	}
	return release, nil
}
