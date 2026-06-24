package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
