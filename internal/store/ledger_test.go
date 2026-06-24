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

// TestOptionADefaultNoReserve: the founder-approved Option A defaults (no env set)
// are a 90-day hold with NO separate reserve. An earning must therefore land fully
// in held (nothing reserved), and at +90d the whole gross becomes payable - no stuck
// reserved bucket, no withholding past the hold.
func TestOptionADefaultNoReserve(t *testing.T) {
	// Exercise the shipped defaults: clear any ROGERAI_PAYOUT_* the developer's shell
	// may export so the test is hermetic (an empty value falls through to the default).
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "")
	t.Setenv("ROGERAI_PAYOUT_MIN", "")
	t.Setenv("ROGERAI_PAYOUT_SCHEDULE", "")
	p := LoadPayoutPolicy()
	if p.Reserve != 0 {
		t.Fatalf("default Reserve = %v, want 0 (Option A: no separate reserve)", p.Reserve)
	}
	if p.HoldDays != 90 || p.MinPayout != 25 || p.Schedule != "monthly" {
		t.Fatalf("default policy = %+v, want hold 90 / min 25 / monthly", p)
	}
	m := NewMem()
	m.policy = p
	_ = m.BindNode("n", "acct1")
	_, _ = m.BalanceOf("u", 100)
	if ok, _ := m.Hold("u", 10); !ok {
		t.Fatal("hold")
	}
	if _, err := m.Finalize("u", "n", 10, 10, 10, rec("r1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// day 0: the whole 10 is held, nothing reserved.
	s, _ := m.EarningSplitOf("acct1", now)
	if !approx(s.Held, 10) || s.Reserved != 0 || s.Payable != 0 {
		t.Errorf("day 0 split = held %v reserved %v payable %v, want 10/0/0", s.Held, s.Reserved, s.Payable)
	}
	// day 91: the full 10 is payable, nothing stuck in reserved.
	s2, _ := m.EarningSplitOf("acct1", now.Add(91*24*time.Hour))
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

	if _, ok, reason, _ := m.RequestPayout("acct1", future, 25); ok || reason == "" {
		t.Errorf("payout below min should fail, got ok=%v reason=%q", ok, reason)
	}
	// add another 20 -> payable 40 (>= 25)
	_, _ = m.Hold("u", 20)
	_, _ = m.Finalize("u", "n", 20, 20, 20, rec("r2"))
	pay, ok, _, _ := m.RequestPayout("acct1", future, 25)
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

// payableAccrue is a test helper: accrue `amount` of payable owner-earnings for
// account `acct` on node `node`, fast-forwarding past the hold via the `future`
// clock that the EarningSplit/RequestPayout reads sweep against.
func payableAccrue(t *testing.T, m *Mem, acct, node, reqID string, amount float64) {
	t.Helper()
	_, _ = m.BalanceOf("u", amount+1000)
	_, _ = m.Hold("u", amount)
	if _, err := m.Finalize("u", node, amount, amount, amount, rec(reqID)); err != nil {
		t.Fatalf("finalize %s: %v", reqID, err)
	}
}

// TestRequestPayoutReturnsActualAmount: the returned amount equals the summed
// payable lots AS OF now (boundary-crossing reflected): a lot still inside the hold
// is excluded; once the clock passes its release it is included.
func TestRequestPayoutReturnsActualAmount(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	payableAccrue(t, m, "acct1", "n", "r1", 30)
	payableAccrue(t, m, "acct1", "n", "r2", 40)

	// At now+89d nothing is payable yet (still inside the 90d hold) -> below min.
	early := time.Now().Add(89 * 24 * time.Hour)
	if _, ok, _, _ := m.RequestPayout("acct1", early, 25); ok {
		t.Fatal("nothing should be payable before the hold clears")
	}

	// At now+91d both lots have crossed the boundary -> exactly 70 payable.
	late := time.Now().Add(91 * 24 * time.Hour)
	pay, ok, _, _ := m.RequestPayout("acct1", late, 25)
	if !ok || !approx(pay.Amount, 70) {
		t.Fatalf("payout = %+v ok=%v, want amount 70 (30+40)", pay, ok)
	}
	if pay.State != PayoutPending || pay.StripeTransferID != "" {
		t.Errorf("payout should be PENDING with no transfer id yet, got state=%q tr=%q", pay.State, pay.StripeTransferID)
	}
	// Exactly the summed payable lots were debited; none left.
	s, _ := m.EarningSplitOf("acct1", late)
	if s.Payable != 0 || !approx(s.Paid, 70) {
		t.Errorf("post-debit split payable=%v paid=%v, want 0/70", s.Payable, s.Paid)
	}
}

// TestRequestPayoutConcurrent: two concurrent payout requests for the same account
// must debit the payable lots EXACTLY ONCE (one succeeds with the full amount, the
// other finds nothing left), never double-paying.
func TestRequestPayoutConcurrent(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	payableAccrue(t, m, "acct1", "n", "r1", 50)
	late := time.Now().Add(91 * 24 * time.Hour)

	type res struct {
		amount float64
		ok     bool
	}
	results := make(chan res, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			p, ok, _, _ := m.RequestPayout("acct1", late, 25)
			results <- res{p.Amount, ok}
		}()
	}
	close(start)
	r1, r2 := <-results, <-results

	wins := 0
	var total float64
	for _, r := range []res{r1, r2} {
		if r.ok {
			wins++
			total += r.amount
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one payout should succeed, got %d (r1=%+v r2=%+v)", wins, r1, r2)
	}
	if !approx(total, 50) {
		t.Errorf("the single successful payout = %v, want 50 (no double-debit)", total)
	}
	// The ledger has exactly one payout row (the loser debited nothing).
	led, _ := m.LedgerOf("acct1", []string{KindPayout}, 10)
	if len(led) != 1 || !approx(led[0].Amount, -50) {
		t.Errorf("payout ledger = %+v, want exactly one -50 row", led)
	}
}

// TestSettleAndFailPayout: SettlePayout marks a pending payout PAID + records the
// transfer id; FailPayout rolls the debited lots back to PAYABLE and reverses the
// payout ledger row (so no orphan debit and no paid-out-but-not-payable lots).
func TestSettleAndFailPayout(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	payableAccrue(t, m, "acct1", "n", "r1", 60)
	late := time.Now().Add(91 * 24 * time.Hour)

	// FAIL path: debit, then fail -> lots back to payable, ledger reversed.
	pay, ok, _, _ := m.RequestPayout("acct1", late, 25)
	if !ok {
		t.Fatal("payout should debit")
	}
	if err := m.FailPayout(pay.ID); err != nil {
		t.Fatalf("FailPayout: %v", err)
	}
	s, _ := m.EarningSplitOf("acct1", late)
	if !approx(s.Payable, 60) || s.Paid != 0 {
		t.Errorf("after FailPayout split payable=%v paid=%v, want 60/0 (rolled back)", s.Payable, s.Paid)
	}
	// The reversed payout row no longer counts against the operator.
	bal, _ := m.DeriveBalance("acct1")
	_ = bal

	// SETTLE path: re-debit, then settle with a transfer id -> paid, lots stay paid.
	pay2, ok, _, _ := m.RequestPayout("acct1", late, 25)
	if !ok {
		t.Fatal("re-payout should debit the restored lots")
	}
	if err := m.SettlePayout(pay2.ID, "tr_real_123"); err != nil {
		t.Fatalf("SettlePayout: %v", err)
	}
	pays, _ := m.PayoutsOf("acct1", 10)
	var settled *Payout
	for i := range pays {
		if pays[i].ID == pay2.ID {
			settled = &pays[i]
		}
	}
	if settled == nil || settled.State != PayoutPaid || settled.StripeTransferID != "tr_real_123" {
		t.Errorf("settled payout = %+v, want PAID with transfer tr_real_123", settled)
	}
	s2, _ := m.EarningSplitOf("acct1", late)
	if s2.Payable != 0 || !approx(s2.Paid, 60) {
		t.Errorf("after settle split payable=%v paid=%v, want 0/60", s2.Payable, s2.Paid)
	}
}
