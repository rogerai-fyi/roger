package main

// spool.go is the settle courier's CRASH INSURANCE. The in-memory queue and retry backlog
// die with the process, and a receipt is the node's pay: a tower restarted (deploy, crash,
// OOM) mid-window would otherwise silently unbank every completion it had not yet forwarded.
// So every receipt is spooled to disk the moment it is queued and removed only when its ride
// to Core succeeds (or is deliberately abandoned); at startup, leftover spool entries rejoin
// the retry backlog. Files are tiny (a receipt + ids), short-lived (the settle window), and
// 0600 under the tower's own data dir.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const spoolDirName = "settle-spool"

// settleSpool persists pending settles under dir. A nil spool (setup failed) degrades to
// memory-only, loudly - never silently.
type settleSpool struct{ dir string }

// spoolEntry is the on-disk shape. The deadline rides along so a restart cannot revive a
// receipt whose settle window has already closed.
type spoolEntry struct {
	StationID string    `json:"station_id"`
	AttemptID string    `json:"attempt_id"`
	Receipt   []byte    `json:"receipt"`
	Deadline  time.Time `json:"deadline"`
}

func newSettleSpool(base string) (*settleSpool, error) {
	dir := filepath.Join(base, spoolDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &settleSpool{dir: dir}, nil
}

// path derives the entry's filename from a hash of the attempt id, so an id can never
// traverse or collide however it is spelled.
func (s *settleSpool) path(attemptID string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(attemptID))))
}

func (s *settleSpool) put(p pendingSettle) error {
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(spoolEntry{
		StationID: p.stationID, AttemptID: p.attemptID, Receipt: p.receipt, Deadline: p.deadline,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(p.attemptID), raw, 0o600)
}

func (s *settleSpool) drop(attemptID string) {
	if s == nil {
		return
	}
	_ = os.Remove(s.path(attemptID))
}

// load returns every still-live spooled settle and deletes the expired ones. Called once at
// courier start; the returned entries rejoin the retry backlog.
func (s *settleSpool) load(now time.Time) []pendingSettle {
	if s == nil {
		return nil
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []pendingSettle
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(s.dir, e.Name())
		raw, rerr := os.ReadFile(full)
		if rerr != nil {
			continue
		}
		var se spoolEntry
		if json.Unmarshal(raw, &se) != nil || se.AttemptID == "" || len(se.Receipt) == 0 {
			_ = os.Remove(full) // unreadable: it can never settle, and it must not re-load forever
			continue
		}
		if now.After(se.Deadline) {
			_ = os.Remove(full)
			continue
		}
		out = append(out, pendingSettle{
			stationID: se.StationID, attemptID: se.AttemptID, receipt: se.Receipt,
			notBefore: now, deadline: se.Deadline,
		})
	}
	return out
}
