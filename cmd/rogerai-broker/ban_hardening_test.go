package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestStrikeCorroborationBeforeBan locks the corroboration guard (audit 3.2): an
// accumulating-signal ban requires strikes across MORE THAN ONE distinct signal class,
// so a single noisy class can never auto-ban an account even past the count threshold.
func TestStrikeCorroborationBeforeBan(t *testing.T) {
	b, _, priv := newRecourseBroker(t)
	acct := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	b.strikeWarnAt, b.strikeBanAt = 2, 3
	b.strikeCorroborateKinds = 2
	b.strikeDecayDays = 0 // count all strikes (decay covered separately)

	// Three strikes of the SAME class -> past the count threshold but only ONE distinct
	// signal class -> NOT banned (held, but not durably banned).
	for _, k := range []string{"e1", "e2", "e3"} {
		b.strike("n", store.StrikeEmptyOutput, k, false, nil)
	}
	if b.isOwnerBanned(acct) {
		t.Fatal("a single signal class must NOT auto-ban even past the count threshold (corroboration guard)")
	}

	// A strike from a SECOND distinct class corroborates -> now banned.
	b.strike("n", store.StrikeRecountDiscrepancy, "r1", false, nil)
	if !b.isOwnerBanned(acct) {
		t.Fatal("a second distinct signal class past the threshold must ban (corroborated)")
	}
}

// TestZeroDoubtBansImmediately locks that the impossible-input arithmetic proof still
// bans on the first strike, bypassing decay + corroboration.
func TestZeroDoubtBansImmediately(t *testing.T) {
	b, _, priv := newRecourseBroker(t)
	acct := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	b.strikeBanAt, b.strikeCorroborateKinds = 5, 2
	b.strike("n", store.StrikeImpossibleInput, "imp1", true, nil)
	if !b.isOwnerBanned(acct) {
		t.Fatal("zero-doubt impossible-input must ban on the first strike")
	}
}

// TestOwnerAppealOwnerScoped locks the self-serve appeal flow (3.3): owner-authed,
// strictly owner-scoped (a caller can only appeal their own node), and a clear false
// positive (a report-origin ban below the corroboration threshold) auto-exonerates.
func TestOwnerAppealOwnerScoped(t *testing.T) {
	b, db, priv := newRecourseBroker(t)
	b.banned = map[string]bool{}
	b.reportEjectAt, b.reportDecayDays = 3, 30

	// Anonymous -> 401.
	w := httptest.NewRecorder()
	b.ownerAppeal(w, httptest.NewRequest(http.MethodPost, "/owner/appeal", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon appeal = %d, want 401", w.Code)
	}

	// Cross-account: bind another account's node, then octocat tries to appeal it -> 403.
	mpub, _, _ := ed25519.GenerateKey(nil)
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "mallory", Pubkey: hex.EncodeToString(mpub)})
	_ = db.BindNode("malnode", hex.EncodeToString(mpub))
	body, _ := json.Marshal(ownerAppealRequest{NodeID: "malnode", Reason: "let me in"})
	w = httptest.NewRecorder()
	b.ownerAppeal(w, signedReq(http.MethodPost, "/owner/appeal", body, priv))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-account appeal = %d, want 403 (%s)", w.Code, w.Body.String())
	}

	// Own node, report-origin ban below the corroboration threshold (0 reports) -> the
	// appeal is recorded AND the node is auto-exonerated (lifted immediately).
	b.banNode("n", reportBanReasonPrefix+" (3 distinct reporters)")
	if !b.isBanned("n") {
		t.Fatal("setup: node n should be banned")
	}
	body, _ = json.Marshal(ownerAppealRequest{NodeID: "n", Reason: "false positive"})
	w = httptest.NewRecorder()
	b.ownerAppeal(w, signedReq(http.MethodPost, "/owner/appeal", body, priv))
	if w.Code != http.StatusOK {
		t.Fatalf("own-node appeal = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		OK             bool `json:"ok"`
		AutoExonerated bool `json:"auto_exonerated"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.OK || !resp.AutoExonerated {
		t.Fatalf("expected ok+auto_exonerated, got %+v", resp)
	}
	if b.isBanned("n") {
		t.Fatal("a clear false-positive report-ban must auto-lift on appeal")
	}
	// The appeal is queued for admin review (owner-scoped to the caller).
	acct := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	appeals, _ := db.AppealsByOwner(acct, 10)
	if len(appeals) != 1 {
		t.Fatalf("appeal not recorded for the caller, got %d", len(appeals))
	}
}

// TestAdminUnbanNode locks the admin node recovery path: admin-gated, lifts the ban.
func TestAdminUnbanNode(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	b.banned = map[string]bool{}
	b.banNode("n", "manual: confirmed abuse")

	// No admin key -> 403.
	body, _ := json.Marshal(adminUnbanNodeRequest{Node: "n"})
	w := httptest.NewRecorder()
	b.adminUnbanNode(w, httptest.NewRequest(http.MethodPost, "/admin/unban-node", bytes.NewReader(body)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unban without admin key = %d, want 403", w.Code)
	}
	// Correct admin key -> lifts the ban.
	r := httptest.NewRequest(http.MethodPost, "/admin/unban-node", bytes.NewReader(body))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w = httptest.NewRecorder()
	b.adminUnbanNode(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin unban = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if b.isBanned("n") {
		t.Fatal("admin unban must lift the ban")
	}
}
