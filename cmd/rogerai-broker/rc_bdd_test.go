package main

// rc_bdd_test.go makes features/remote/rc_isolation.feature and rc_lifecycle.feature
// EXECUTABLE against the REAL /rc/* handlers over an in-memory store (no mocks). It drives
// the security-critical surface: same-account isolation, the constant-work uniform 404, and
// the enable/online/offline/disable/revoke/quota lifecycle.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type rcSessState struct {
	t         *testing.T
	b         *broker
	mem       *store.Mem
	alice     ed25519.PrivateKey
	mallory   ed25519.PrivateKey
	sid       string // alice's primary session
	sid2      string // alice's second session
	code      string // alice's primary link code (one-time)
	hostTok   string
	attachTok string
	code2     string
	lastCode  int
	lastBody  map[string]any
}

func (s *rcSessState) reset(t *testing.T) {
	*s = rcSessState{t: t, mem: store.NewMem()}
	s.b = &broker{db: s.mem, pubOfUser: map[string]string{}}
	_, s.alice, _ = ed25519.GenerateKey(nil)
	_, s.mallory, _ = ed25519.GenerateKey(nil)
	// Bind alice → u_gh_7, mallory → u_gh_9 (both logged-in accounts).
	_ = s.mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: hexPub(s.alice)})
	_ = s.mem.BindOwner(store.Owner{GitHubID: 9, Login: "mallory", Pubkey: hexPub(s.mallory)})
}

func hexPub(p ed25519.PrivateKey) string {
	return hex.EncodeToString(p.Public().(ed25519.PublicKey))
}

// do drives a handler with a signed request, capturing the JSON response.
func (s *rcSessState) do(priv ed25519.PrivateKey, method, path string, body any, h http.HandlerFunc) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if priv != nil {
		signReq(r, priv, raw)
	}
	w := httptest.NewRecorder()
	h(w, r)
	s.lastCode = w.Code
	s.lastBody = nil
	_ = json.Unmarshal(w.Body.Bytes(), &s.lastBody)
}

func (s *rcSessState) enable(priv ed25519.PrivateKey, name string) {
	s.do(priv, http.MethodPost, "/rc/enable", map[string]any{"name": name}, s.b.rcEnable)
}

// --- shared Background ---

func (s *rcSessState) runningBroker() error { return nil } // reset() built it
func (s *rcSessState) freshStore() error    { return nil }

func (s *rcSessState) aliceHasSession() error {
	s.enable(s.alice, "hermes · RogerAI")
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("enable failed: %d", s.lastCode)
	}
	s.sid, _ = s.lastBody["session_id"].(string)
	s.code, _ = s.lastBody["code"].(string)
	s.hostTok, _ = s.lastBody["host_token"].(string)
	return nil
}
func (s *rcSessState) malloryExists() error { return nil }

// --- isolation ---

func (s *rcSessState) aliceAttachesOwn() error {
	s.do(s.alice, http.MethodPost, "/rc/attach", map[string]any{"code": s.code}, s.b.rcAttach)
	return nil
}
func (s *rcSessState) attachSucceeds() error {
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("attach want 200, got %d", s.lastCode)
	}
	if _, ok := s.lastBody["attach_token"].(string); !ok {
		return fmt.Errorf("no attach token returned")
	}
	s.attachTok, _ = s.lastBody["attach_token"].(string)
	return nil
}
func (s *rcSessState) malloryAttachesAliceCode() error {
	s.do(s.mallory, http.MethodPost, "/rc/attach", map[string]any{"code": s.code}, s.b.rcAttach)
	return nil
}
func (s *rcSessState) uniform404() error {
	if s.lastCode != http.StatusNotFound {
		return fmt.Errorf("want uniform 404, got %d", s.lastCode)
	}
	if s.lastBody["error"] != "no such session" {
		return fmt.Errorf("want uniform body, got %v", s.lastBody)
	}
	return nil
}
func (s *rcSessState) aliceAttachesCase(kase string) error {
	switch kase {
	case "a wrong code":
		s.do(s.alice, http.MethodPost, "/rc/attach", map[string]any{"code": "RC 100.000 MHz · ZZZZ-ZZZZ"}, s.b.rcAttach)
	case "a revoked session":
		sess, _, _ := s.mem.RCSessionByID(s.sid)
		sess.Revoked = true
		_ = s.mem.UpdateRCSession(sess)
		s.do(s.alice, http.MethodPost, "/rc/attach", map[string]any{"code": s.code}, s.b.rcAttach)
	case "an expired code":
		sess, _, _ := s.mem.RCSessionByID(s.sid)
		sess.CodeExpires = time.Now().Add(-time.Minute).Unix()
		_ = s.mem.UpdateRCSession(sess)
		s.do(s.alice, http.MethodPost, "/rc/attach", map[string]any{"code": s.code}, s.b.rcAttach)
	case "a nonexistent id":
		s.do(s.alice, http.MethodPost, "/rc/attach", map[string]any{"code": ""}, s.b.rcAttach)
	default:
		return fmt.Errorf("unknown case %q", kase)
	}
	return nil
}
func (s *rcSessState) aliceSecondSession() error {
	s.enable(s.alice, "second")
	s.sid2, _ = s.lastBody["session_id"].(string)
	s.code2, _ = s.lastBody["code"].(string)
	return nil
}
func (s *rcSessState) sendToSecondWithFirstToken() error {
	// Attach to the FIRST session, then use that token against the SECOND session.
	_ = s.aliceAttachesOwn()
	_ = s.attachSucceeds()
	r := httptest.NewRequest(http.MethodPost, "/rc/"+s.sid2+"/send", bytes.NewReader([]byte(`{"kind":"turn","text":"hi"}`)))
	signReq(r, s.alice, []byte(`{"kind":"turn","text":"hi"}`))
	r.Header.Set("X-Roger-Attach", s.attachTok)
	w := httptest.NewRecorder()
	s.b.rcSubtree(w, r)
	s.lastCode = w.Code
	return nil
}
func (s *rcSessState) unauthorized() error {
	if s.lastCode != http.StatusUnauthorized {
		return fmt.Errorf("want 401, got %d", s.lastCode)
	}
	return nil
}
func (s *rcSessState) notInDiscover() error {
	// A remote-control session is never a node: it has no tunnel, so pickFor can't select it
	// and it never enters the registry the market/discover read.
	s.b.mu.Lock()
	_, isNode := s.b.nodes[s.sid]
	s.b.mu.Unlock()
	if isNode {
		return fmt.Errorf("a session must never be registered as a node")
	}
	return nil
}
func (s *rcSessState) notEligible() error { return s.notInDiscover() }
func (s *rcSessState) notInMarket() error { return s.notInDiscover() }

// --- lifecycle ---

func (s *rcSessState) enableNamed(wallet, name string) error {
	// wallet string is cosmetic in the feature ("u_gh_7"); we always act as alice.
	s.enable(s.alice, name)
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("enable failed: %d", s.lastCode)
	}
	s.sid, _ = s.lastBody["session_id"].(string)
	s.hostTok, _ = s.lastBody["host_token"].(string)
	s.code, _ = s.lastBody["code"].(string)
	return nil
}
func (s *rcSessState) sessionListed() error {
	s.do(s.alice, http.MethodGet, "/rc/sessions", nil, s.b.rcSessions)
	list, _ := s.lastBody["sessions"].([]any)
	for _, it := range list {
		if m, _ := it.(map[string]any); m["id"] == s.sid {
			return nil
		}
	}
	return fmt.Errorf("session %s not listed", s.sid)
}
func (s *rcSessState) onlineAfterPoll() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	if !s.b.rcOnline(sess, time.Now()) {
		return fmt.Errorf("session should be online right after enable")
	}
	return nil
}
func (s *rcSessState) polledOnce() error { return s.enableNamed("u_gh_7", "poller") }
func (s *rcSessState) silence31s() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	sess.LastHostSeen = time.Now().Add(-31 * time.Second).Unix()
	return s.mem.UpdateRCSession(sess)
}
func (s *rcSessState) reportsOffline() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	if s.b.rcOnline(sess, time.Now()) {
		return fmt.Errorf("session should report offline after 31s of silence")
	}
	return nil
}
func (s *rcSessState) hostReconnects() error {
	// A poll refreshes LastHostSeen. Pre-queue an inbound so the long-poll returns at once.
	h := s.b.rcHubFor(s.sid)
	h.in <- protocol.RCInbound{Kind: protocol.RCInBackfill}
	r := httptest.NewRequest(http.MethodGet, "/rc/"+s.sid+"/poll", nil)
	r.Header.Set("Authorization", "Bearer "+s.hostTok)
	w := httptest.NewRecorder()
	s.b.rcSubtree(w, r)
	return nil
}
func (s *rcSessState) onlineAgainSameID() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	if !s.b.rcOnline(sess, time.Now()) || sess.ID != s.sid {
		return fmt.Errorf("session should be online again with the same id")
	}
	return nil
}
func (s *rcSessState) enabledWithDevice() error {
	if err := s.enableNamed("u_gh_7", "dev"); err != nil {
		return err
	}
	_ = s.aliceAttachesOwn()
	return s.attachSucceeds()
}
func (s *rcSessState) ownerDisables() error {
	r := httptest.NewRequest(http.MethodPost, "/rc/"+s.sid+"/disable", nil)
	signReq(r, s.alice, nil)
	w := httptest.NewRecorder()
	s.b.rcSubtree(w, r)
	s.lastCode = w.Code
	return nil
}
func (s *rcSessState) sessionRevoked() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	if !sess.Revoked {
		return fmt.Errorf("session should be revoked")
	}
	return nil
}
func (s *rcSessState) tokenNoLongerSends() error {
	r := httptest.NewRequest(http.MethodPost, "/rc/"+s.sid+"/send", bytes.NewReader([]byte(`{"kind":"turn","text":"x"}`)))
	signReq(r, s.alice, []byte(`{"kind":"turn","text":"x"}`))
	r.Header.Set("X-Roger-Attach", s.attachTok)
	w := httptest.NewRecorder()
	s.b.rcSubtree(w, r)
	if w.Code != http.StatusUnauthorized {
		return fmt.Errorf("a revoked session's send want 401, got %d", w.Code)
	}
	return nil
}
func (s *rcSessState) nSessions(n int) error {
	for i := 0; i < n; i++ {
		if err := s.enableNamed("u_gh_7", "s"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}
func (s *rcSessState) revokeAll() error {
	s.do(s.alice, http.MethodPost, "/rc/revoke-all", nil, s.b.rcRevokeAll)
	return nil
}
func (s *rcSessState) allRevoked(n int) error {
	list, _ := s.mem.RCSessionsByOwner("u_gh_7")
	for _, sess := range list {
		if !sess.Revoked {
			return fmt.Errorf("session %s not revoked", sess.ID)
		}
	}
	return nil
}
func (s *rcSessState) enablesAnother() error { return s.enableNamed("u_gh_7", "another") }
func (s *rcSessState) accountDeleted(_ string) error {
	// Simulate the account-delete hook: revoke all RC sessions for the wallet.
	if list, err := s.mem.RCSessionsByOwner("u_gh_7"); err == nil {
		for _, sess := range list {
			if sess.Active() {
				s.b.rcEndSession(sess.ID)
			}
		}
	}
	_, _ = s.mem.RevokeRCSessions("u_gh_7")
	return nil
}
func (s *rcSessState) thatSessionRevoked() error {
	sess, _, _ := s.mem.RCSessionByID(s.sid)
	if !sess.Revoked {
		return fmt.Errorf("the session should be revoked after account delete")
	}
	return nil
}
func (s *rcSessState) hostPolled8dAgo() error {
	if err := s.enableNamed("u_gh_7", "idle"); err != nil {
		return err
	}
	h := s.b.rcHubFor(s.sid)
	h.mu.Lock()
	h.lastHost = time.Now().Add(-8 * 24 * time.Hour)
	h.mu.Unlock()
	return nil
}
func (s *rcSessState) idleSweep() error { s.b.rcGCOnce(time.Now()); return nil }
func (s *rcSessState) sessionGCd() error {
	s.b.rcMu.Lock()
	_, alive := s.b.rcHubs[s.sid]
	s.b.rcMu.Unlock()
	if alive {
		return fmt.Errorf("idle session hub should be garbage-collected")
	}
	return nil
}
func (s *rcSessState) enable6th() error { s.enable(s.alice, "sixth"); return nil }
func (s *rcSessState) refusedQuota() error {
	if s.lastCode != http.StatusTooManyRequests {
		return fmt.Errorf("6th session want 429, got %d", s.lastCode)
	}
	return nil
}

func rcSuite(t *testing.T, path string, init func(*godog.ScenarioContext, *rcSessState)) {
	s := &rcSessState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(t); return ctx, nil })
			init(sc, s)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{path}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatalf("%s scenarios failed", path)
	}
}

func TestRCIsolationFeature(t *testing.T) {
	rcSuite(t, "../../features/remote/rc_isolation.feature", func(sc *godog.ScenarioContext, s *rcSessState) {
		sc.Step(`^a running broker$`, s.runningBroker)
		sc.Step(`^an owner "alice" \(u_gh_7\) with a remote-control session$`, s.aliceHasSession)
		sc.Step(`^a different owner "mallory" \(u_gh_9\)$`, s.malloryExists)
		sc.Step(`^alice attaches to her session with the correct code$`, s.aliceAttachesOwn)
		sc.Step(`^the attach succeeds and returns a one-time attach token$`, s.attachSucceeds)
		sc.Step(`^mallory attaches with alice's correct code$`, s.malloryAttachesAliceCode)
		sc.Step(`^the response is the uniform 404 "no such session"$`, s.uniform404)
		sc.Step(`^alice attaches with "([^"]*)"$`, s.aliceAttachesCase)
		sc.Step(`^alice has a second remote-control session$`, s.aliceSecondSession)
		sc.Step(`^alice sends to the second session using the first session's attach token$`, s.sendToSecondWithFirstToken)
		sc.Step(`^the response is unauthorized$`, s.unauthorized)
		sc.Step(`^no remote-control session appears in /discover$`, s.notInDiscover)
		sc.Step(`^no remote-control session is eligible in pickFor$`, s.notEligible)
		sc.Step(`^no remote-control session appears in the market$`, s.notInMarket)
	})
}

func TestRCLifecycleFeature(t *testing.T) {
	rcSuite(t, "../../features/remote/rc_lifecycle.feature", func(sc *godog.ScenarioContext, s *rcSessState) {
		sc.Step(`^a fresh store$`, s.freshStore)
		sc.Step(`^wallet "([^"]*)" enables a remote-control session named "([^"]*)"$`, s.enableNamed)
		sc.Step(`^the session is listed for that wallet$`, s.sessionListed)
		sc.Step(`^after a host poll the session reports online$`, s.onlineAfterPoll)
		sc.Step(`^an enabled session for "([^"]*)" that has polled once$`, func(_ string) error { return s.polledOnce() })
		sc.Step(`^(\d+) seconds pass with no host poll$`, func(int) error { return s.silence31s() })
		sc.Step(`^the session reports offline$`, s.reportsOffline)
		sc.Step(`^the host reconnects with its host token$`, s.hostReconnects)
		sc.Step(`^the session reports online again with the same id$`, s.onlineAgainSameID)
		sc.Step(`^an enabled session for "([^"]*)" with one attached device$`, func(_ string) error { return s.enabledWithDevice() })
		sc.Step(`^the owner disables the session$`, s.ownerDisables)
		sc.Step(`^the session is revoked$`, s.sessionRevoked)
		sc.Step(`^the device's attach token no longer authorizes sends$`, s.tokenNoLongerSends)
		sc.Step(`^"([^"]*)" has (\d+) enabled sessions$`, func(_ string, n int) error { return s.nSessions(n) })
		sc.Step(`^the owner revokes all remote-control sessions$`, s.revokeAll)
		sc.Step(`^all (\d+) sessions are revoked$`, s.allRevoked)
		sc.Step(`^"([^"]*)" enables another session$`, func(_ string) error { return s.enablesAnother() })
		sc.Step(`^the account "([^"]*)" is deleted$`, s.accountDeleted)
		sc.Step(`^that session is revoked too$`, s.thatSessionRevoked)
		sc.Step(`^an enabled session for "([^"]*)" whose host last polled (\d+) days ago$`, func(_ string, _ int) error { return s.hostPolled8dAgo() })
		sc.Step(`^the idle sweep runs$`, s.idleSweep)
		sc.Step(`^the session is garbage-collected$`, s.sessionGCd)
		sc.Step(`^the owner enables a 6th remote-control session$`, s.enable6th)
		sc.Step(`^it is refused for exceeding the session quota$`, s.refusedQuota)
	})
}
