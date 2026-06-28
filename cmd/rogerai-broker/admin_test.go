package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// newAdminBroker wires a broker with BOTH admin credentials configured: the broker key
// (X-Roger-Admin) and the super-admin GitHub id (the web-session path). A bound operator
// (octocat, gid 7) exists but is NOT the admin (gid 7 != adminGitHubID 42), so the test
// can prove an ordinary logged-in owner is rejected.
func newAdminBroker(t *testing.T) (*broker, store.Store, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	_ = db.BindNode("n", pubHex)
	b := &broker{
		priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{},
		bannedOwners: map[string]bool{}, banned: map[string]bool{},
		// nodes/lastSeen/private left nil: liveMarket ranges over them safely (empty market).
		// admin creds: broker key + the single super-admin github id.
		adminKey: "admin-secret-key", adminGitHubID: 42,
	}
	b.bill.creditUSD = 1
	return b, db, priv
}

// TestAdminGateRejectsNonAdmin locks the SECURITY-CRITICAL contract: every admin
// aggregate endpoint 403s for (a) an anonymous caller, (b) a wrong broker key, and (c) an
// ORDINARY logged-in owner whose github id is not the configured super-admin. It also
// confirms BOTH admin credentials are accepted (the broker key AND the matching session).
func TestAdminGateRejectsNonAdmin(t *testing.T) {
	b, _, _ := newAdminBroker(t)
	// Give the broker a real (empty) in-memory node registry so liveMarket is safe.
	b.nodes = nil
	b.lastSeen = nil
	b.private = nil

	// The portal-facing live feed is the gated read; the gate (requireAdmin) is the same code
	// path for every /admin/* endpoint, so this proves the security contract for the surface.
	endpoints := []struct {
		name string
		fn   http.HandlerFunc
		path string
	}{
		{"live", b.adminLive, "/admin/live"},
	}

	for _, ep := range endpoints {
		// (a) anonymous -> 403
		w := httptest.NewRecorder()
		ep.fn(w, httptest.NewRequest(http.MethodGet, ep.path, nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s anon = %d, want 403", ep.name, w.Code)
		}
		// (b) wrong broker key -> 403
		r := httptest.NewRequest(http.MethodGet, ep.path, nil)
		r.Header.Set("X-Roger-Admin", "nope")
		w = httptest.NewRecorder()
		ep.fn(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s bad key = %d, want 403", ep.name, w.Code)
		}
		// (c) ordinary logged-in owner (gid 7, not the admin 42) -> 403 (no data leak)
		w = httptest.NewRecorder()
		ep.fn(w, sessionReq(b, http.MethodGet, ep.path, "octocat", 7))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s non-admin session = %d, want 403 (NO data leak)", ep.name, w.Code)
		}
		// (d) correct broker key -> 200
		r = httptest.NewRequest(http.MethodGet, ep.path, nil)
		r.Header.Set("X-Roger-Admin", "admin-secret-key")
		w = httptest.NewRecorder()
		ep.fn(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("%s admin key = %d, want 200 (%s)", ep.name, w.Code, w.Body.String())
		}
		// (e) matching super-admin session (gid 42) -> 200
		w = httptest.NewRecorder()
		ep.fn(w, sessionReq(b, http.MethodGet, ep.path, "founder", 42))
		if w.Code != http.StatusOK {
			t.Errorf("%s admin session = %d, want 200 (%s)", ep.name, w.Code, w.Body.String())
		}
	}
}

// TestAdminSurfaceClosedWhenUnconfigured locks fail-closed: with NEITHER admin credential
// configured, even the correct-looking header is rejected (the surface is OFF).
func TestAdminSurfaceClosedWhenUnconfigured(t *testing.T) {
	b, _, _ := newAdminBroker(t)
	b.adminKey = ""
	b.adminGitHubID = 0
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminLive(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("live with no creds configured = %d, want 403 (closed)", w.Code)
	}
}

// The financial / payout-queue COMPUTATION specs moved with the queries to the private
// roger-admin repo (db_test.go: TestFinancialsWithData / TestPayoutQueueWithData), which now
// owns that logic. The broker keeps only the live feed (adminLive) + the gate, tested above.
