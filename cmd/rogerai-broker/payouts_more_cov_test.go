package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// buildPayoutBrokerForTest builds a payout broker reading the payout policy from the
// CURRENT env (so the caller's t.Setenv hold/reserve/min take effect, unlike
// newPayoutBroker which sets its own).
func buildPayoutBrokerForTest(db store.Store) *broker {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv, db: db, seedFunds: 0, conn: loadConnect()}
	b.bill.creditUSD = 1 // so the transfer amount converts to non-zero cents
	return b
}

// payableBroker builds a payout broker whose operator (octocat/pk1) has a releasable
// (hold=0, reserve=0) payable balance above the $25 minimum and a passed (dev-stub) KYC.
func payableBroker(t *testing.T) (*broker, store.Store) {
	t.Helper()
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25")
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pk1"})
	_ = db.BindNode("n", "pk1")
	b := buildPayoutBrokerForTest(db)
	_ = db.SetConnect("octocat", "acct_dev_stub", "active")
	_, _ = db.AddCredits("u", 1000)
	_, _ = db.Hold("u", 100)
	_, _ = db.Finalize("u", "n", 100, 100, 100, rec("rp1")) // pk1 now has ~100 payable
	return b, db
}

// TestPayoutsRequestMethodAndAuthGuards locks the front guards: a non-POST is 405 and a
// session whose login is not a bound operator (GitHubID 0) is 403.
func TestPayoutsRequestMethodAndAuthGuards(t *testing.T) {
	b, _ := newPayoutBroker(t)

	wm := httptest.NewRecorder()
	b.payoutsRequest(wm, sessionReq(b, http.MethodGet, "/payouts/request", "octocat", 7))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /payouts/request = %d, want 405", wm.Code)
	}

	wf := httptest.NewRecorder()
	b.payoutsRequest(wf, sessionReq(b, http.MethodPost, "/payouts/request", "stranger", 999))
	if wf.Code != http.StatusForbidden {
		t.Fatalf("non-operator payout = %d, want 403", wf.Code)
	}
}

// TestPayoutsRequestTransferFailRollsBack locks the transfer-failure rollback: when the
// Stripe transfer errors AFTER the debit, the payout is failed and the lots roll back to
// payable (no money left in limbo), and the caller gets a 502.
func TestPayoutsRequestTransferFailRollsBack(t *testing.T) {
	b, db := payableBroker(t)
	before, _ := db.EarningSplitOf("pk1", time.Now())
	b.conn.transfer = func(connectID string, cents int64, idem string) (string, error) {
		return "", errBoom // Stripe rejects the transfer
	}

	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("transfer-fail payout = %d, want 502 (%s)", w.Code, w.Body.String())
	}
	after, _ := db.EarningSplitOf("pk1", time.Now())
	if after.Payable != before.Payable {
		t.Errorf("transfer fail must roll lots back to payable: %v -> %v", before.Payable, after.Payable)
	}
}

// TestPayoutsRequestSettleFailSurfaces locks the money-moved-but-record-stuck path: the
// transfer succeeds but SettlePayout errors -> a 500 (lots are NOT rolled back, since the
// money did move), surfaced so the operator state is reconcilable.
func TestPayoutsRequestSettleFailSurfaces(t *testing.T) {
	b, db := payableBroker(t)
	fs := &failStore{Store: db, failSettlePay: true}
	b.db = fs
	b.conn.transfer = func(connectID string, cents int64, idem string) (string, error) {
		return "tr_ok", nil // transfer succeeds...
	}

	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("settle-fail payout = %d, want 500 (%s)", w.Code, w.Body.String())
	}
}

// TestPayoutsRequestSucceeds locks the happy path end to end: a transfer succeeds, the
// payout settles paid, and the payable lots are consumed.
func TestPayoutsRequestSucceeds(t *testing.T) {
	b, db := payableBroker(t)
	var gotCents int64
	b.conn.transfer = func(connectID string, cents int64, idem string) (string, error) {
		gotCents = cents
		return "tr_paid", nil
	}

	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("payout = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if gotCents <= 0 {
		t.Errorf("transfer cents = %d, want positive", gotCents)
	}
	if split, _ := db.EarningSplitOf("pk1", time.Now()); split.Payable > 1e-6 {
		t.Errorf("after payout payable = %v, want ~0 (consumed)", split.Payable)
	}
}
