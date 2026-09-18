package edge

// THE SESSION MIRROR - one machine, many processes, one view.
//
// A session ledger lives in the process that recorded it: `roger use` in one terminal,
// the TUI in another, the console inside the TUI. The Edge screen must show all of them,
// and nothing here may make one process wait on, lock against, or reach into another. So
// each recording ledger PUBLISHES its live list to one private file of its own under a
// shared directory, and a viewing ledger MERGES its siblings of the same account on read.
// Nobody writes another process's file. A mirror carries what the view draws - never the
// receipt bodies - and mirrored sessions fade on the same life as local ones. A file whose
// every session has faded is removed by the first reader that finds it so, which is how a
// dead process's leftovers go away without anyone tracking pids.
//
// Spec: features/edge/session_attribution.feature (@edge).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// mirrorSession is what crosses processes: the drawn fields only.
type mirrorSession struct {
	Request  string             `json:"request"`
	Kind     Initiator          `json:"kind"`
	Who      string             `json:"who,omitempty"`
	Via      string             `json:"via,omitempty"`
	Band     string             `json:"band"`
	Station  string             `json:"station,omitempty"`
	Left     string             `json:"left,omitempty"`
	Escalate bool               `json:"escalate,omitempty"`
	Contract store.EdgeContract `json:"contract,omitempty"`
	Route    string             `json:"route,omitempty"`
	Outcome  Outcome            `json:"outcome"`
	Reason   string             `json:"reason,omitempty"`
	At       int64              `json:"at"`
}

type mirrorFile struct {
	Account  string          `json:"account"`
	Sessions []mirrorSession `json:"sessions"`
}

// mirrorSeq makes two ledgers in ONE process (tests, or a TUI beside its console) write
// two files: the name is pid plus a per-process sequence, never pid alone.
var mirrorSeq atomic.Int64

// Mirror publishes this ledger under dir and merges its siblings into Live(). It creates
// nothing until the first Record: a ledger that never records leaves no file.
func (s *Sessions) Mirror(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dir = dir
	s.path = filepath.Join(dir, fmt.Sprintf("%d-%d.json", os.Getpid(), mirrorSeq.Add(1)))
}

// MirrorPath is this ledger's own file ("" when unmirrored).
func (s *Sessions) MirrorPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return ""
	}
	return s.path
}

// OnError hears the FIRST mirror write failure. A mirror that cannot be written never
// refuses a session - the relay is never refused by the thing looking at it - so the
// failure is reported once and the ledger goes on in memory.
func (s *Sessions) OnError(fn func(error)) { s.mu.Lock(); s.onErr = fn; s.mu.Unlock() }

func toMirror(x Session) mirrorSession {
	return mirrorSession{Request: x.Request, Kind: x.Kind, Who: x.Who, Via: x.Via, Band: x.Band,
		Station: x.Station, Left: x.Left, Escalate: x.Escalate, Contract: x.Contract, Route: x.Route,
		Outcome: x.Outcome, Reason: x.Reason, At: x.At}
}

func (m mirrorSession) session(account string) Session {
	return Session{Request: m.Request, Account: account, Kind: m.Kind, Who: m.Who, Via: m.Via, Band: m.Band,
		Station: m.Station, Left: m.Left, Escalate: m.Escalate, Contract: m.Contract, Route: m.Route,
		Outcome: m.Outcome, Reason: m.Reason, At: m.At}
}

// publishLocked writes this ledger's live list to its own file, atomically (a reader never
// sees half a file). Called with mu held, after every Record.
func (s *Sessions) publishLocked() {
	if s.dir == "" {
		return
	}
	f := mirrorFile{Account: s.account, Sessions: make([]mirrorSession, 0, len(s.order))}
	for _, id := range s.order {
		f.Sessions = append(f.Sessions, toMirror(s.byReq[id]))
	}
	err := func() error {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return err
		}
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, s.path)
	}()
	if err != nil && !s.errSaid {
		s.errSaid = true
		if s.onErr != nil {
			s.onErr(fmt.Errorf("edge sessions: the mirror could not be written; sessions stay in this process: %w", err))
		}
	}
}

// mirroredLocked merges the siblings: same account, still within life, not already held
// here (this ledger's own record wins). A file whose every session has faded is removed.
func (s *Sessions) mirroredLocked(now time.Time) []Session {
	if s.dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	sort.Strings(files)
	cut := now.Add(-SessionLife).Unix()
	var out []Session
	for _, p := range files {
		if p == s.path {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f mirrorFile
		if json.Unmarshal(b, &f) != nil {
			continue // a corrupt file is nobody's session
		}
		live := 0
		for _, m := range f.Sessions {
			if m.At < cut {
				continue
			}
			live++
			if f.Account != s.account {
				continue // counted as live so it is not removed, never drawn
			}
			if _, mine := s.byReq[m.Request]; mine {
				continue
			}
			out = append(out, m.session(s.account))
		}
		if live == 0 && len(f.Sessions) > 0 {
			// Everything in it has faded - but only a file nobody has REPUBLISHED for a
			// whole life is a dead process's leftovers. A live process rewrites its file
			// on every Record, so a fresh mtime means a session may have just landed
			// between this read and the remove; leave it and let the next read see it.
			if fi, err := os.Stat(p); err == nil && now.Sub(fi.ModTime()) > SessionLife {
				_ = os.Remove(p)
			}
		}
	}
	return out
}
