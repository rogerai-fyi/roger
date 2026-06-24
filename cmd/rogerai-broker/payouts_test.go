package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rec is a tiny receipt helper for the payout/account broker tests.
func rec(id string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: id, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: time.Now().Unix()}
}

// sessionReq builds a request carrying a valid web session cookie for `login`/`gid`.
func sessionReq(b *broker, method, path, login string, gid int64) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession(login, gid, time.Now().Add(time.Hour).Unix())})
	return r
}

// newPayoutBroker wires a broker with an in-memory store, a bound operator (login
// "octocat", pubkey "pk1"), and a node "n" owned by that account.
func newPayoutBroker(t *testing.T) (*broker, store.Store) {
	t.Helper()
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0.10")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25")
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pk1"})
	_ = db.BindNode("n", "pk1")
	b := &broker{priv: priv, db: db, seedFunds: 0, conn: loadConnect()}
	return b, db
}

// TestPayoutKYCGate: a payout request before Connect onboarding (transfers not
// active) is rejected with 403, even when there is plenty payable.
func TestPayoutKYCGate(t *testing.T) {
	b, db := newPayoutBroker(t)
	// accrue 40 payable for the operator and fast-forward past the hold by serving now
	// against a clock the store advances via EarningSplit reads.
	_, _ = db.BalanceOf("u", 1000)
	_, _ = db.Hold("u", 40)
	_, _ = db.Finalize("u", "n", 40, 40, 40, rec("r1"))

	// Without onboarding, connect_status is "none" -> 403.
	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusForbidden {
		t.Errorf("payout without KYC = %d, want 403", w.Code)
	}
}

// TestPayoutBelowMinimum: after KYC, a payout below $25 minimum is rejected.
func TestPayoutBelowMinimum(t *testing.T) {
	b, db := newPayoutBroker(t)
	_ = db.SetConnect("octocat", "acct_dev_stub", "active") // KYC passed (dev stub)
	_, _ = db.BalanceOf("u", 1000)
	_, _ = db.Hold("u", 10)
	_, _ = db.Finalize("u", "n", 10, 10, 10, rec("r1")) // 10 < 25 minimum

	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusBadRequest {
		t.Errorf("below-min payout = %d, want 400", w.Code)
	}
}

// TestAccountDeleteGuard: an account with held earnings cannot be deleted (409).
func TestAccountDeleteGuard(t *testing.T) {
	b, db := newPayoutBroker(t)
	_, _ = db.BalanceOf("u", 1000)
	_, _ = db.Hold("u", 30)
	_, _ = db.Finalize("u", "n", 30, 30, 30, rec("r1")) // operator pk1 now holds 30

	w := httptest.NewRecorder()
	b.accountDelete(w, sessionReq(b, http.MethodPost, "/account/delete", "octocat", 7))
	if w.Code != http.StatusConflict {
		t.Errorf("delete with held earnings = %d, want 409", w.Code)
	}
	// the account must still resolve (not deleted)
	if _, ok, _ := db.OwnerByLogin("octocat"); !ok {
		t.Error("account should not have been deleted")
	}
}

// TestAccountDeleteOK: a clean account (no balance, no earnings) deletes + anonymizes.
func TestAccountDeleteOK(t *testing.T) {
	b, db := newPayoutBroker(t)
	w := httptest.NewRecorder()
	b.accountDelete(w, sessionReq(b, http.MethodPost, "/account/delete", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("clean delete = %d, want 200", w.Code)
	}
	var out struct {
		Deleted bool `json:"deleted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Deleted {
		t.Error("expected deleted=true")
	}
	if _, ok, _ := db.OwnerByLogin("octocat"); ok {
		t.Error("anonymized login should not resolve")
	}
}

// TestConnectOnboardStub: with no Stripe key, onboarding returns a stub link and
// marks the account onboarding.
func TestConnectOnboardStub(t *testing.T) {
	b, db := newPayoutBroker(t)
	w := httptest.NewRecorder()
	b.connectOnboard(w, sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("onboard stub = %d, want 200", w.Code)
	}
	if o, _, _ := db.OwnerByLogin("octocat"); o.ConnectStatus != "onboarding" {
		t.Errorf("connect status = %q, want onboarding", o.ConnectStatus)
	}
}

// TestConnectOnboardStubAndStatus: the dev-stub onboard marks the account
// onboarding, and connect/status then reports that stored status with can_payout
// false (transfers not yet active).
func TestConnectOnboardStubAndStatus(t *testing.T) {
	b, _ := newPayoutBroker(t)
	w := httptest.NewRecorder()
	b.connectOnboard(w, sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("onboard = %d, want 200", w.Code)
	}

	w2 := httptest.NewRecorder()
	b.connectStatus(w2, sessionReq(b, http.MethodGet, "/connect/status", "octocat", 7))
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w2.Code)
	}
	var out struct {
		Status    string `json:"status"`
		CanPayout bool   `json:"can_payout"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &out)
	if out.Status != "onboarding" || out.CanPayout {
		t.Errorf("status=%q can_payout=%v, want onboarding/false", out.Status, out.CanPayout)
	}
}

// TestPayoutBelowMin (alias of TestPayoutBelowMinimum, per the v0.3.1 test list):
// a payout below the minimum is rejected with 400 BEFORE any transfer is attempted.
func TestPayoutBelowMin(t *testing.T) {
	b, db := newPayoutBroker(t)
	_ = db.SetConnect("octocat", "acct_dev_stub", "active")
	b.bill.creditUSD = 1
	transferCalls := 0
	b.conn.transfer = func(dest string, cents int64, idem string) (string, error) {
		transferCalls++
		return "tr_should_not_happen", nil
	}
	_, _ = db.BalanceOf("u", 1000)
	_, _ = db.Hold("u", 10)
	_, _ = db.Finalize("u", "n", 10, 10, 10, rec("r1")) // 10 < 25 min

	w := httptest.NewRecorder()
	b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if w.Code != http.StatusBadRequest {
		t.Errorf("below-min payout = %d, want 400", w.Code)
	}
	if transferCalls != 0 {
		t.Errorf("a below-min request must NOT attempt a transfer (called %d times)", transferCalls)
	}
}

// TestPayoutTransfersRecordedAmount: with KYC active and payable >= min, the amount
// TRANSFERRED equals the amount RECORDED on the payout, the Stripe idempotency key
// is the store payout id, and a transfer FAILURE leaves NO completed transfer (the
// lots roll back to payable and the payout is not paid).
func TestPayoutTransfersRecordedAmount(t *testing.T) {
	// Zero hold + zero reserve so the earning is payable AT now (payoutsRequest reads
	// time.Now() internally - no clock injection), making the flow deterministic. The
	// store loads its policy at NewMem, so set env BEFORE building the broker/store.
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25")
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pk1"})
	_ = db.BindNode("n", "pk1")
	b := &broker{priv: priv, db: db, seedFunds: 0, conn: loadConnect()}
	_ = db.SetConnect("octocat", "acct_dev_stub", "active")
	b.bill.creditUSD = 1 // 1 credit == $1 so cents == amount*100

	// Accrue 40 payable for the operator (pk1 owns node n).
	_, _ = db.BalanceOf("u", 1000)
	_, _ = db.Hold("u", 40)
	_, _ = db.Finalize("u", "n", 40, 40, 40, rec("r1"))

	// --- transfer FAILURE first: nothing should be left paid -----------------
	b.conn.transfer = func(dest string, cents int64, idem string) (string, error) {
		return "", errStripeTransfer
	}
	wf := httptest.NewRecorder()
	b.payoutsRequest(wf, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if wf.Code != http.StatusBadGateway {
		t.Fatalf("failed transfer = %d, want 502", wf.Code)
	}
	if s, _ := db.EarningSplitOf("pk1", time.Now()); s.Paid != 0 || s.Payable < 39.9 {
		t.Errorf("after failed transfer split paid=%v payable=%v, want 0 paid / lots rolled back", s.Paid, s.Payable)
	}
	if pays, _ := db.PayoutsOf("pk1", 10); len(pays) != 1 || pays[0].State != store.PayoutFailed {
		t.Errorf("a failed transfer must leave the payout FAILED, got %+v", pays)
	}

	// --- transfer SUCCESS: transferred cents == recorded amount; idem == payout id
	var gotCents int64
	var gotIdem string
	b.conn.transfer = func(dest string, cents int64, idem string) (string, error) {
		gotCents = cents
		gotIdem = idem
		return "tr_ok_1", nil
	}
	ws := httptest.NewRecorder()
	b.payoutsRequest(ws, sessionReq(b, http.MethodPost, "/payouts/request", "octocat", 7))
	if ws.Code != http.StatusOK {
		t.Fatalf("good payout = %d, want 200 body=%s", ws.Code, ws.Body.String())
	}
	var out struct {
		Payout store.Payout `json:"payout"`
	}
	_ = json.Unmarshal(ws.Body.Bytes(), &out)
	if out.Payout.State != store.PayoutPaid || out.Payout.StripeTransferID != "tr_ok_1" {
		t.Errorf("payout = %+v, want PAID with transfer tr_ok_1", out.Payout)
	}
	// transferred cents must equal the recorded amount in cents.
	wantCents := int64(out.Payout.Amount*b.bill.creditUSD*100 + 0.5)
	if gotCents != wantCents {
		t.Errorf("transferred %d cents, recorded amount %.4f (=%d cents) - must match", gotCents, out.Payout.Amount, wantCents)
	}
	// idempotency key must be the store payout id.
	if gotIdem != "payout:"+strconv.FormatInt(out.Payout.ID, 10) {
		t.Errorf("idempotency key = %q, want payout:%d", gotIdem, out.Payout.ID)
	}
}
