package store

import (
	"testing"
	"time"
)

// rc_store_test.go covers the remote-control roster store on BOTH Mem and real Postgres
// (gated on ROGERAI_TEST_DATABASE_URL), so the roster methods are honestly covered — the
// broker-level BDD suites (Increment 2+) drive these under a different package's coverage.

func rcSeed(id, wallet, codeHash, hostHash string) RCSession {
	return RCSession{
		ID: id, OwnerWallet: wallet, Name: id + " · RogerAI",
		CodeHash: codeHash, CodeExpires: time.Unix(1_800_000_000, 0).Add(RCCodeTTL).Unix(),
		CodeDisplay: "RC 147.520 MHz · ••••-••••", HostTokenHash: hostHash,
		CreatedAt: time.Unix(1_800_000_000, 0).Unix(),
	}
}

func rcRoundtrip(t *testing.T, db Store) {
	t.Helper()
	// create + lookups
	if err := db.CreateRCSession(rcSeed("rcs_1", "u_gh_7", "codehash1", "hosthash1")); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := db.RCSessionByID("rcs_1"); err != nil || !ok || s.OwnerWallet != "u_gh_7" {
		t.Fatalf("RCSessionByID: %+v ok=%v err=%v", s, ok, err)
	}
	if s, ok, err := db.RCSessionByCodeHash("codehash1"); err != nil || !ok || s.ID != "rcs_1" {
		t.Fatalf("RCSessionByCodeHash: %+v ok=%v err=%v", s, ok, err)
	}
	if _, ok, _ := db.RCSessionByCodeHash("nope"); ok {
		t.Fatal("unknown code hash must not resolve")
	}
	if _, ok, _ := db.RCSessionByCodeHash(""); ok {
		t.Fatal("empty code hash must not resolve")
	}

	// rotate: old hash dies, new resolves
	s, _, _ := db.RCSessionByID("rcs_1")
	s.CodeHash = "codehash2"
	if err := db.UpdateRCSession(s); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.RCSessionByCodeHash("codehash1"); ok {
		t.Fatal("rotated-away code hash must stop resolving")
	}
	if _, ok, _ := db.RCSessionByCodeHash("codehash2"); !ok {
		t.Fatal("rotated-in code hash must resolve")
	}

	// attach tokens
	if err := db.PutRCAttachToken(RCAttachToken{Hash: "athash", SessionID: "rcs_1", DeviceLabel: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutRCAttachToken(RCAttachToken{Hash: "athash", SessionID: "rcs_1", DeviceLabel: "web"}); err != nil {
		t.Fatalf("PutRCAttachToken must be idempotent: %v", err)
	}
	if at, ok, err := db.RCAttachTokenByHash("athash"); err != nil || !ok || at.SessionID != "rcs_1" {
		t.Fatalf("RCAttachTokenByHash: %+v ok=%v err=%v", at, ok, err)
	}
	if _, ok, _ := db.RCAttachTokenByHash("missing"); ok {
		t.Fatal("unknown attach hash must not resolve")
	}

	// a second session for the same owner + one for another owner
	if err := db.CreateRCSession(rcSeed("rcs_2", "u_gh_7", "codehash3", "hosthash2")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRCSession(rcSeed("rcs_other", "u_gh_9", "codehash4", "hosthash3")); err != nil {
		t.Fatal(err)
	}
	if list, err := db.RCSessionsByOwner("u_gh_7"); err != nil || len(list) != 2 {
		t.Fatalf("RCSessionsByOwner u_gh_7: %d err=%v", len(list), err)
	}

	// revoke-all for u_gh_7: both revoked, attach token gone, codes stop resolving; the
	// other owner's session is untouched.
	n, err := db.RevokeRCSessions("u_gh_7")
	if err != nil || n != 2 {
		t.Fatalf("RevokeRCSessions: n=%d err=%v (want 2)", n, err)
	}
	if s, _, _ := db.RCSessionByID("rcs_1"); !s.Revoked {
		t.Fatal("rcs_1 must be revoked")
	}
	if _, ok, _ := db.RCSessionByCodeHash("codehash2"); ok {
		t.Fatal("a revoked session's code must not resolve")
	}
	if _, ok, _ := db.RCAttachTokenByHash("athash"); ok {
		t.Fatal("a revoked session's attach token must be dropped")
	}
	if s, _, _ := db.RCSessionByID("rcs_other"); s.Revoked {
		t.Fatal("another owner's session must be untouched by revoke-all")
	}
	if n2, _ := db.RevokeRCSessions("u_gh_7"); n2 != 0 {
		t.Fatalf("second revoke-all must be a no-op, got %d", n2)
	}
}

func TestMemRCRoster(t *testing.T) { rcRoundtrip(t, NewMem()) }
func TestPostgresRCRoster(t *testing.T) {
	rcRoundtrip(t, pgOnly(t))
}

func TestRCSessionHelpers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	open := rcSeed("rcs_1", "u_gh_7", "h", "ht")
	if !open.Active() || !open.CodeOpen(now) {
		t.Fatal("a fresh session should be active + code-open")
	}
	closed := open
	closed.CodeExpires = 0
	if closed.CodeOpen(now) {
		t.Fatal("a closed window must not be code-open")
	}
	expired := open
	expired.CodeExpires = now.Add(-time.Minute).Unix()
	if expired.CodeOpen(now) {
		t.Fatal("an expired window must not be code-open")
	}
	revoked := open
	revoked.Revoked = true
	if revoked.Active() || revoked.CodeOpen(now) {
		t.Fatal("a revoked session is neither active nor code-open")
	}
	if RCSessionQuota("anyone") != 5 {
		t.Fatal("RC session quota should be 5")
	}
}
