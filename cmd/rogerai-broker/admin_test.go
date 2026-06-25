package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
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

	endpoints := []struct {
		name string
		fn   http.HandlerFunc
		path string
	}{
		{"overview", b.adminOverview, "/admin/overview"},
		{"payouts", b.adminPayouts, "/admin/payouts"},
		{"abuse", b.adminAbuse, "/admin/abuse"},
		{"activity", b.adminActivity, "/admin/activity"},
		{"whoami", b.adminWhoami, "/admin/whoami"},
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
	r := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminOverview(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("overview with no creds configured = %d, want 403 (closed)", w.Code)
	}
}

// TestAdminOverviewAggregates locks that the overview actually COMPUTES the rollup: after
// a topup + a paid settle, the platform fee, consumer spend, operator earned, and the
// marketplace request total reflect the real money/receipts (no zeros, no drift).
func TestAdminOverviewAggregates(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, db, _ := newAdminBroker(t)
	b.nodes = nil
	b.lastSeen = nil
	b.private = nil
	b.feeRate = 0.30

	// A consumer tops up $100 and spends $10 on a request; the owner share (70%) is $7.
	_, _ = db.AddCredits("u_gh_7", 100)
	_, _ = db.Settle("u_gh_7", "n", 10, 7, rec("rq1"))

	r := httptest.NewRequest(http.MethodGet, "/admin/overview?days=30", nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminOverview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("overview = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Marketplace struct {
			RequestsTotal int64 `json:"requests_total"`
		} `json:"marketplace"`
		Financial struct {
			PlatformFee    float64 `json:"platform_fee"`
			ConsumerSpend  float64 `json:"consumer_spend"`
			OperatorEarned float64 `json:"operator_earned"`
			TopupVolume    float64 `json:"topup_volume"`
			WalletCount    int     `json:"wallet_count"`
		} `json:"financial"`
		Health struct {
			Ready bool `json:"ready"`
		} `json:"health"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Financial.ConsumerSpend != 10 {
		t.Errorf("consumer_spend = %v, want 10", resp.Financial.ConsumerSpend)
	}
	if resp.Financial.OperatorEarned != 7 {
		t.Errorf("operator_earned = %v, want 7", resp.Financial.OperatorEarned)
	}
	if resp.Financial.PlatformFee != 3 {
		t.Errorf("platform_fee = %v, want 3 (spend 10 - earned 7)", resp.Financial.PlatformFee)
	}
	if resp.Financial.TopupVolume != 100 {
		t.Errorf("topup_volume = %v, want 100", resp.Financial.TopupVolume)
	}
	if resp.Financial.WalletCount < 1 {
		t.Errorf("wallet_count = %d, want >=1", resp.Financial.WalletCount)
	}
	if resp.Marketplace.RequestsTotal != 1 {
		t.Errorf("requests_total = %d, want 1", resp.Marketplace.RequestsTotal)
	}
	if !resp.Health.Ready {
		t.Error("health.ready = false, want true (Mem store healthy)")
	}
}

// TestAdminPayoutQueueComputes locks the payouts view: the queue reports the operator's
// payable balance after a (held=0) settle, so an admin can see who is owed.
func TestAdminPayoutQueueComputes(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, db, priv := newAdminBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_ = db.BindNode("n2", pubHex)
	_, _ = db.AddCredits("u_gh_7", 50)
	_, _ = db.Settle("u_gh_7", "n2", 50, 35, rec("rq2"))

	r := httptest.NewRequest(http.MethodGet, "/admin/payouts", nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminPayouts(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("payouts = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Queue []store.AdminPayoutQueueRow `json:"queue"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	var found bool
	for _, q := range resp.Queue {
		if q.AccountID == pubHex && q.Payable == 35 {
			found = true
		}
	}
	if !found {
		t.Errorf("queue missing payable=35 for the operator account: %+v", resp.Queue)
	}
}
