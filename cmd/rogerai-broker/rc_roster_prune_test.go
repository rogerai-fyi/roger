package main

// rc_roster_prune_test.go pins that the BASE STATION roster self-cleans: an ENDED (revoked)
// session drops off the list on the next fetch instead of lingering as "ended" forever, and a
// long-idle session ages out. This is what makes the app/TUI swipe-to-end actually remove a
// session (the founder-hit "I can't get rid of a killed session" gap). Real /rc handlers over
// store.NewMem(), no mocks; reuses the rcSessState harness.

import (
	"net/http"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

func rosterHasSession(body map[string]any, sid string) bool {
	list, _ := body["sessions"].([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m["id"] == sid {
			return true
		}
	}
	return false
}

func (s *rcSessState) listSessions() {
	s.do(s.alice, http.MethodGet, "/rc/sessions", nil, s.b.rcSessions)
}

func TestRCEndedSessionDropsFromRoster(t *testing.T) {
	s := &rcSessState{}
	s.reset(t)
	if err := s.aliceHasSession(); err != nil {
		t.Fatal(err)
	}
	sid := s.sid

	s.listSessions()
	if !rosterHasSession(s.lastBody, sid) {
		t.Fatalf("precondition: the live session should be listed (%v)", s.lastBody)
	}

	if err := s.ownerDisables(); err != nil {
		t.Fatal(err)
	}
	// A fresh roster fetch must NOT list the ended session - it is pruned, not shown as "ended".
	s.listSessions()
	if rosterHasSession(s.lastBody, sid) {
		t.Fatalf("an ended session must drop off the roster, not linger forever: %v", s.lastBody)
	}
}

func TestRCIdleSessionAgesOffRoster(t *testing.T) {
	s := &rcSessState{}
	s.reset(t)
	if err := s.aliceHasSession(); err != nil {
		t.Fatal(err)
	}
	sid := s.sid
	// Make the host silent for 8 days (> RCIdleGC) - a killed session that will never poll again.
	sess, _, _ := s.mem.RCSessionByID(sid)
	sess.LastHostSeen = time.Now().Add(-8 * 24 * time.Hour).Unix()
	if err := s.mem.UpdateRCSession(sess); err != nil {
		t.Fatal(err)
	}
	// The roster fetch runs the lazy GC (idle past RCIdleGC), so the dead session is gone.
	s.listSessions()
	if rosterHasSession(s.lastBody, sid) {
		t.Fatalf("a session idle > %v must age off the roster: %v", store.RCIdleGC, s.lastBody)
	}
}

// TestRCLiveSessionStaysOnRoster: a recently-offline (but not revoked, not long-idle) session
// must NOT be pruned - only ended or long-dead ones are.
func TestRCLiveSessionStaysOnRoster(t *testing.T) {
	s := &rcSessState{}
	s.reset(t)
	if err := s.aliceHasSession(); err != nil {
		t.Fatal(err)
	}
	sid := s.sid
	sess, _, _ := s.mem.RCSessionByID(sid)
	sess.LastHostSeen = time.Now().Add(-2 * time.Minute).Unix() // offline a couple minutes, recoverable
	_ = s.mem.UpdateRCSession(sess)
	s.listSessions()
	if !rosterHasSession(s.lastBody, sid) {
		t.Fatalf("a recently-offline session must stay on the roster (host may reconnect): %v", s.lastBody)
	}
}
