package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rec is a tiny receipt helper for these tests.
func rec(id string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: id, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: time.Now().Unix()}
}

// TestLedgerBalanceDerivation: every wallet mutation appends a ledger row, so the
// re-derived balance must equal the cached balance (the nightly drift check).
func TestLedgerBalanceDerivation(t *testing.T) {
	m := NewMem()
	_, _ = m.BalanceOf("u", 0)
	_, _ = m.AddCredits("u", 50)
	if ok, _ := m.Hold("u", 10); !ok {
		t.Fatal("hold should succeed")
	}
	// finalize: capture 4 of the 10 hold (refund 6) -> balance 50-4 = 46
	if _, err := m.Finalize("u", "n", 10, 4, 2.8, rec("r1")); err != nil {
		t.Fatal(err)
	}
	// a released hold nets to zero
	if ok, _ := m.Hold("u", 5); !ok {
		t.Fatal("hold2")
	}
	_, _ = m.ReleaseHold("u", 5)

	cached, _ := m.BalanceOf("u", 0)
	derived, _ := m.DeriveBalance("u")
	if !approx(cached, 46) {
		t.Errorf("cached balance = %v, want 46", cached)
	}
	if !approx(cached, derived) {
		t.Errorf("ledger drift: cached=%v derived=%v", cached, derived)
	}
}

// TestLedgerIdempotency: CreditOnce + a chargeback are idempotent on their keys, so
// a Stripe redelivery never double-applies.
func TestLedgerIdempotency(t *testing.T) {
	m := NewMem()
	c1, b1, _ := m.CreditOnce("stripe:sess_1", "u", 10)
	c2, b2, _ := m.CreditOnce("stripe:sess_1", "u", 10) // redelivery
	if !c1 || c2 {
		t.Errorf("credited flags = %v,%v want true,false", c1, c2)
	}
	if !approx(b1, 10) || !approx(b2, 10) {
		t.Errorf("balances = %v,%v want 10,10 (no double-credit)", b1, b2)
	}
	// exactly one topup ledger row for that idem key
	led, _ := m.LedgerOf("u", []string{KindTopup}, 100)
	if len(led) != 1 {
		t.Errorf("topup rows = %d, want 1 (idempotent)", len(led))
	}
}

// TestHoldPromotionAt90d: an earning lot is held until release_at, then a sweep (on
// read) promotes the non-reserve part to payable. Inject the clock to fast-forward.
func TestHoldPromotionAt90d(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0.10")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	_, _ = m.BalanceOf("u", 100)
	if ok, _ := m.Hold("u", 10); !ok {
		t.Fatal("hold")
	}
	// owner share 10 -> lot gross=10, reserve=1
	if _, err := m.Finalize("u", "n", 10, 10, 10, rec("r1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s, _ := m.EarningSplitOf("acct1", now)
	if !approx(s.Held, 9) || !approx(s.Reserved, 1) || s.Payable != 0 {
		t.Errorf("day 0 split = held %v reserved %v payable %v, want 9/1/0", s.Held, s.Reserved, s.Payable)
	}
	// fast-forward 91 days: the 9 (gross-reserve) AND the 1 reserve both release at +90d
	future := now.Add(91 * 24 * time.Hour)
	s2, _ := m.EarningSplitOf("acct1", future)
	if s2.Held != 0 || s2.Reserved != 0 || !approx(s2.Payable, 10) {
		t.Errorf("day 91 split = held %v reserved %v payable %v, want 0/0/10", s2.Held, s2.Reserved, s2.Payable)
	}
}

// TestPayoutMinAndPromotion: a payout below the minimum is rejected; above it, the
// payable lots are paid and a payout ledger row is written.
func TestPayoutMin(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0.10")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	_, _ = m.BalanceOf("u", 1000)
	// one earning of 20 -> after 91d, payable 20 (< 25 minimum)
	_, _ = m.Hold("u", 20)
	_, _ = m.Finalize("u", "n", 20, 20, 20, rec("r1"))
	future := time.Now().Add(91 * 24 * time.Hour)

	if _, ok, reason, _ := m.RequestPayout("acct1", future, 25, "tr_1"); ok || reason == "" {
		t.Errorf("payout below min should fail, got ok=%v reason=%q", ok, reason)
	}
	// add another 20 -> payable 40 (>= 25)
	_, _ = m.Hold("u", 20)
	_, _ = m.Finalize("u", "n", 20, 20, 20, rec("r2"))
	pay, ok, _, _ := m.RequestPayout("acct1", future, 25, "tr_1")
	if !ok || !approx(pay.Amount, 40) {
		t.Fatalf("payout = %+v ok=%v, want amount 40", pay, ok)
	}
	// nothing payable left
	s, _ := m.EarningSplitOf("acct1", future)
	if s.Payable != 0 || !approx(s.Paid, 40) {
		t.Errorf("post-payout split payable=%v paid=%v, want 0/40", s.Payable, s.Paid)
	}
	led, _ := m.LedgerOf("acct1", []string{KindPayout}, 10)
	if len(led) != 1 || !approx(led[0].Amount, -40) {
		t.Errorf("payout ledger = %+v, want one -40 row", led)
	}
}

// TestDisputeClawback: a chargeback debits the consumer wallet AND claws back the
// operator's still-held earnings from the same request.
func TestDisputeClawback(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	_, _ = m.BalanceOf("u", 100)
	_, _ = m.Hold("u", 10)
	_, _ = m.Finalize("u", "n", 10, 10, 10, rec("r1")) // owner held 10 (request r1)

	now := time.Now()
	clawed, err := m.Chargeback("dp_1", "u", "r1", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(clawed, 10) {
		t.Errorf("clawed = %v, want 10", clawed)
	}
	// idempotent: a redelivered dispute event claws nothing more
	clawed2, _ := m.Chargeback("dp_1", "u", "r1", 10, now)
	if clawed2 != 0 {
		t.Errorf("redelivered dispute clawed %v, want 0", clawed2)
	}
	// consumer wallet debited once by the chargeback
	bal, _ := m.BalanceOf("u", 0)
	derived, _ := m.DeriveBalance("u")
	if !approx(bal, derived) {
		t.Errorf("post-chargeback drift: cached=%v derived=%v", bal, derived)
	}
	// the operator lot is clawed -> nothing held/payable
	s, _ := m.EarningSplitOf("acct1", now.Add(91*24*time.Hour))
	if s.Held+s.Payable+s.Reserved != 0 {
		t.Errorf("clawed earnings still showing: %+v", s)
	}
}

// TestDeleteGuard: an account with held earnings cannot be deleted until resolved.
func TestAccountAnonymize(t *testing.T) {
	m := NewMem()
	_ = m.BindOwner(Owner{GitHubID: 1, Login: "octocat", Pubkey: "pk1", Email: "a@b.c"})
	if _, ok, _ := m.OwnerByLogin("octocat"); !ok {
		t.Fatal("owner should resolve by login")
	}
	if _, _, _ = m.UpdateAccount("octocat", "new@x.y"); true {
		if o, _, _ := m.OwnerByLogin("octocat"); o.Email != "new@x.y" {
			t.Errorf("email not updated: %q", o.Email)
		}
	}
	ok, _ := m.DeleteAccount("octocat")
	if !ok {
		t.Fatal("delete should succeed")
	}
	// a deleted (anonymized) login no longer resolves
	if _, ok, _ := m.OwnerByLogin("octocat"); ok {
		t.Error("anonymized login should not resolve")
	}
}
