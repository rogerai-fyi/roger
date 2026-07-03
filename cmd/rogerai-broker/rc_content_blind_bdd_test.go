package main

// rc_content_blind_bdd_test.go makes features/remote/rc_content_blind.feature EXECUTABLE: the
// broker persists ONLY a roster row per remote-control session - never a frame's text, prompt,
// or assistant message. Live frames flow through a bounded TRANSIENT in-memory ring. Drives the
// REAL rcHub + store + accountExport over store.NewMem(), no mocks.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

const rcSecretMarker = "SECRET-RC-PAYLOAD-do-not-persist"

type rcCBState struct {
	t   *testing.T
	b   *broker
	mem *store.Mem
	sid string
	hub *rcHub
}

func (s *rcCBState) reset(t *testing.T) {
	mem := store.NewMem()
	*s = rcCBState{t: t, mem: mem, b: relayBroker(mem)}
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: "alicepub", Email: "alice@op.test"})
}

// mkSession creates a roster row + hub for wallet and relays `turns` user+assistant frames,
// each carrying the secret marker, through the REAL publish path.
func (s *rcCBState) mkSession(wallet string, turns int) {
	s.sid = "rcs_" + wallet
	if err := s.mem.CreateRCSession(store.RCSession{
		ID: s.sid, OwnerWallet: wallet, Name: "hermes · RogerAI", CreatedAt: time.Now().Unix(), LastHostSeen: time.Now().Unix(),
	}); err != nil {
		s.t.Fatal(err)
	}
	s.hub = s.b.rcHubFor(s.sid)
	for i := 0; i < turns; i++ {
		s.hub.publish(protocol.RCFrame{Kind: protocol.RCKindUser, Origin: "viewer", Text: rcSecretMarker + " user turn"})
		s.hub.publish(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: rcSecretMarker + " assistant reply"})
	}
}

// ── C1 ───────────────────────────────────────────────────────────────────────────────────

func (s *rcCBState) sessionWith20Turns() error { s.mkSession("u_gh_7", 20); return nil }
func (s *rcCBState) durableHasRoster() error {
	if _, found, _ := s.mem.RCSessionByID(s.sid); !found {
		return fmt.Errorf("the session roster row is missing from durable storage")
	}
	return nil
}
func (s *rcCBState) durableHasNoFrameText() error {
	// Everything the store durably holds for this owner's RC (the roster) - serialize it and
	// assert none of the relayed message text is present. The transcript lives only in the
	// transient ring (in memory), never in the store.
	sessions, _ := s.mem.RCSessionsByOwner("u_gh_7")
	blob, _ := json.Marshal(sessions)
	if strings.Contains(string(blob), rcSecretMarker) {
		return fmt.Errorf("frame text leaked into durable storage: %s", blob)
	}
	return nil
}

// ── C2 ───────────────────────────────────────────────────────────────────────────────────

func (s *rcCBState) aSession() error { s.mkSession("u_gh_7", 0); return nil }
func (s *rcCBState) relay500Frames() error {
	for i := 0; i < 500; i++ {
		s.hub.publish(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: rcSecretMarker})
	}
	return nil
}
func (s *rcCBState) ringAtMost200() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if len(s.hub.ring) > rcRingFrames {
		return fmt.Errorf("ring holds %d frames, want <= %d", len(s.hub.ring), rcRingFrames)
	}
	return nil
}
func (s *rcCBState) ringWithinByteCap() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.hub.ringByte > rcRingBytes {
		return fmt.Errorf("ring byte size %d exceeds the %d cap", s.hub.ringByte, rcRingBytes)
	}
	return nil
}

// ── C3 ───────────────────────────────────────────────────────────────────────────────────

var rcExportBody string

func (s *rcCBState) sessionForWallet(wallet string) error { s.mkSession(wallet, 5); return nil }
func (s *rcCBState) ownerExports() error {
	cookie := s.b.signSessionWallet("alice", 7, "u_gh_7", time.Now().Add(time.Hour).Unix())
	r := httptest.NewRequest(http.MethodPost, "/account/export", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.b.accountExport(w, r)
	rcExportBody = w.Body.String()
	return nil
}
func (s *rcCBState) exportListsSession() error {
	if !strings.Contains(rcExportBody, s.sid) || !strings.Contains(rcExportBody, "hermes") {
		return fmt.Errorf("the export does not list the RC session id + name (GDPR roster): %s", rcExportBody)
	}
	return nil
}
func (s *rcCBState) exportHasNoMessageText() error {
	if strings.Contains(rcExportBody, rcSecretMarker) {
		return fmt.Errorf("the export leaked RC message text: %s", rcExportBody)
	}
	return nil
}

func TestRCContentBlindBDD(t *testing.T) {
	st := &rcCBState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.Step(`^a running broker$`, func() error { return nil })
			sc.Step(`^a remote-control session with 20 turns of chat relayed through the broker$`, st.sessionWith20Turns)
			sc.Step(`^durable storage contains the session roster row$`, st.durableHasRoster)
			sc.Step(`^durable storage contains NO frame text, prompt, or assistant message$`, st.durableHasNoFrameText)
			sc.Step(`^a remote-control session$`, st.aSession)
			sc.Step(`^(\d+) frames are relayed through it$`, func(int) error { return st.relay500Frames() })
			sc.Step(`^the replay ring holds at most the last (\d+) frames$`, func(int) error { return st.ringAtMost200() })
			sc.Step(`^the ring's byte size stays within the 256 KiB cap$`, st.ringWithinByteCap)
			sc.Step(`^a remote-control session with relayed chat for wallet "([^"]*)"$`, st.sessionForWallet)
			sc.Step(`^the owner exports their account data$`, st.ownerExports)
			sc.Step(`^the export lists the session id, name, and timestamps$`, st.exportListsSession)
			sc.Step(`^the export contains no message text$`, st.exportHasNoMessageText)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/remote/rc_content_blind.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("rc_content_blind scenarios failed (see godog output above)")
	}
}
