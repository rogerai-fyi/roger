package main

// rc_existence_oracle_test.go enforces the uniform-404 discipline on rcRotateCode + rcDisable
// (audit finding #10): a cross-account caller must NOT be able to distinguish a session that
// exists-but-is-someone-else's (was 403 "not your session") from one that does not exist (404
// "no such session"). Every other RC handler (rcAttach/rcJoin/rcSend/rcStream) already collapses
// both to a uniform 404. Real /rc handlers over store.NewMem(), no mocks; reuses rcSessState.

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"testing"
)

// rcCrossAccountStatuses drives handler h as mallory against (a) alice's real sid and (b) a
// nonexistent sid, returning the two status codes. Uniform discipline => they must be equal.
func rcCrossAccountStatuses(t *testing.T, h func(*rcSessState, string)) (foreign, missing int) {
	t.Helper()
	s := &rcSessState{}
	s.reset(t)
	if err := s.aliceHasSession(); err != nil {
		t.Fatal(err)
	}
	h(s, s.sid) // mallory hits alice's real session
	foreign = s.lastCode
	nonexistent := "rcs_" + hex.EncodeToString([]byte("no-such-session!"))[:16]
	h(s, nonexistent) // mallory hits a session id that does not exist
	missing = s.lastCode
	return foreign, missing
}

func TestRCRotateCodeNoExistenceOracle(t *testing.T) {
	foreign, missing := rcCrossAccountStatuses(t, func(s *rcSessState, sid string) {
		s.do(s.mallory, http.MethodPost, "/rc/"+sid+"/code", nil, func(w http.ResponseWriter, r *http.Request) {
			s.b.rcRotateCode(w, r, sid)
		})
	})
	if foreign != missing {
		t.Fatalf("rcRotateCode leaks session existence: foreign-session=%d vs nonexistent=%d (want equal, uniform 404)", foreign, missing)
	}
	if foreign != http.StatusNotFound {
		t.Errorf("want uniform 404, got %d", foreign)
	}
}

func TestRCDisableNoExistenceOracle(t *testing.T) {
	foreign, missing := rcCrossAccountStatuses(t, func(s *rcSessState, sid string) {
		s.do(s.mallory, http.MethodPost, "/rc/"+sid+"/disable", nil, func(w http.ResponseWriter, r *http.Request) {
			s.b.rcDisable(w, r, sid)
		})
	})
	if foreign != missing {
		t.Fatalf("rcDisable leaks session existence: foreign-session=%d vs nonexistent=%d (want equal, uniform 404)", foreign, missing)
	}
	if foreign != http.StatusNotFound {
		t.Errorf("want uniform 404, got %d", foreign)
	}
}

// keep ed25519 import honest if the harness signature changes
var _ = ed25519.GenerateKey
