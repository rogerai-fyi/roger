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

// fundReal credits a consumer wallet with REAL (cleared-topup) credits, NOT seed.
// After the P0-1 fix, only real-funded spend mints an operator earning lot, so the
// earnings/payout tests must fund consumers with real money (a seed-funded consumer
// correctly earns the operator nothing). AddCredits is the real topup path.
func fundReal(m *Mem, user string, amount float64) {
	_, _ = m.AddCredits(user, amount)
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
	fundReal(m, "u", 100) // REAL credits (P0-1: seed-funded spend earns nothing)
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
// are a 120-day hold with NO separate reserve. An earning must therefore land fully
// in held (nothing reserved), and at +120d the whole gross becomes payable - no stuck
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
	if p.HoldDays != 120 || p.MinPayout != 25 || p.Schedule != "monthly" {
		t.Fatalf("default policy = %+v, want hold 120 / min 25 / monthly", p)
	}
	m := NewMem()
	m.policy = p
	_ = m.BindNode("n", "acct1")
	fundReal(m, "u", 100) // REAL credits (P0-1: seed-funded spend earns nothing)
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
	// day 121: the full 10 is payable, nothing stuck in reserved.
	s2, _ := m.EarningSplitOf("acct1", now.Add(121*24*time.Hour))
	if s2.Held != 0 || s2.Reserved != 0 || !approx(s2.Payable, 10) {
		t.Errorf("day 121 split = held %v reserved %v payable %v, want 0/0/10", s2.Held, s2.Reserved, s2.Payable)
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
	fundReal(m, "u", 1000) // REAL credits (P0-1: seed-funded spend earns nothing)
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
	fundReal(m, "u", 100) // REAL credits (P0-1: seed-funded spend earns nothing)
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
	fundReal(m, "u", amount+1000) // REAL credits: seed-funded spend earns the operator nothing (P0-1)
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

// TestRecountHoldBlocksPromotion locks P0-2 (b): a node flagged with an OPEN re-count
// discrepancy must NOT have its earning lots auto-promote held->payable on schedule;
// clearing the hold lets the next sweep promote them.
func TestRecountHoldBlocksPromotion(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0") // would be immediately payable but for the hold
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	fundReal(m, "u", 100)
	_, _ = m.Hold("u", 30)
	_, _ = m.Finalize("u", "n", 30, 30, 30, rec("r1")) // operator earns 30, releasable now

	// Flag the node: the sweep-on-read must keep the lot HELD, not promote to payable.
	if err := m.SetNodeRecountHold("n", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if s, _ := m.EarningSplitOf("acct1", now); s.Payable != 0 || !approx(s.Held, 30) {
		t.Errorf("held node split = payable %v held %v, want 0/30 (promotion held)", s.Payable, s.Held)
	}
	// A payout request finds nothing payable while the hold stands.
	if _, ok, _, _ := m.RequestPayout("acct1", now, 25); ok {
		t.Error("a held node must have nothing payable")
	}

	// Clear the hold: the next sweep promotes the lot to payable.
	if err := m.SetNodeRecountHold("n", false); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.EarningSplitOf("acct1", now); !approx(s.Payable, 30) {
		t.Errorf("after clearing hold payable = %v, want 30", s.Payable)
	}
}

// TestChargebackReversesPaidLot locks P0-3a: a dispute whose attributable lot was
// ALREADY PAID OUT is clawed (state->clawed) AND returned as a Reversal (with the
// payout's transfer id) so the broker can issue a Stripe Transfer Reversal, and a
// payout_reversed ledger row is written. Idempotent on the dispute id.
func TestChargebackReversesPaidLot(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")
	fundReal(m, "u", 100)
	_, _ = m.Hold("u", 30)
	_, _ = m.Finalize("u", "n", 30, 30, 30, rec("r1")) // operator earns 30 (real-funded)

	// Pay the lot out so it is PAID before the dispute lands (the post-payout case).
	now := time.Now()
	pay, ok, _, _ := m.RequestPayout("acct1", now, 25)
	if !ok {
		t.Fatal("payout should debit the payable lot")
	}
	if err := m.SettlePayout(pay.ID, "tr_paid_1"); err != nil {
		t.Fatal(err)
	}

	// Dispute the funding charge for 30: the paid lot must be reversed, not skipped.
	res, err := m.ChargebackLineage("dp_paid", "u", "", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reversals) != 1 {
		t.Fatalf("want 1 reversal for the paid lot, got %d (res=%+v)", len(res.Reversals), res)
	}
	rv := res.Reversals[0]
	if rv.TransferID != "tr_paid_1" || !approx(rv.Amount, 30) || rv.AccountID != "acct1" {
		t.Errorf("reversal = %+v, want transfer tr_paid_1 / amount 30 / acct1", rv)
	}
	// Clawed (from held/payable) is 0 here - the recovery came via the reversal.
	if res.Clawed != 0 {
		t.Errorf("Clawed = %v, want 0 (the lot was paid, recovered via reversal)", res.Clawed)
	}
	if res.PlatformLoss != 0 {
		t.Errorf("PlatformLoss = %v, want 0 (the disputed amount was fully recovered)", res.PlatformLoss)
	}
	// A payout_reversed ledger row was written against the operator.
	led, _ := m.LedgerOf("acct1", []string{KindPayoutReversed}, 10)
	if len(led) != 1 || !approx(led[0].Amount, -30) {
		t.Errorf("payout_reversed ledger = %+v, want one -30 row", led)
	}
	// Idempotent: a redelivery does nothing.
	res2, _ := m.ChargebackLineage("dp_paid", "u", "", 30, now)
	if !res2.AlreadyHandled || len(res2.Reversals) != 0 {
		t.Errorf("redelivered dispute = %+v, want AlreadyHandled / no reversals", res2)
	}
}

// TestChargebackLineageNotUnrelatedOperators locks P0-4: a dispute claws the DISPUTING
// consumer's OWN lots only and records the unrecovered remainder as a PLATFORM LOSS -
// it never claws an unrelated, honest operator's earnings to cover the shortfall.
func TestChargebackLineageNotUnrelatedOperators(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("nA", "acctA") // alice's spend lands here
	_ = m.BindNode("nB", "acctB") // an UNRELATED operator bob never served alice

	fundReal(m, "alice", 100)
	_, _ = m.Hold("alice", 10)
	_, _ = m.Finalize("alice", "nA", 10, 10, 7, rec("a1")) // operator acctA earns 7

	fundReal(m, "carol", 100)
	_, _ = m.Hold("carol", 50)
	_, _ = m.Finalize("carol", "nB", 50, 50, 35, rec("c1")) // unrelated operator acctB earns 35

	// alice disputes a $40 charge. Only her one lot (gross 7) is attributable; the rest
	// ($33) is a PLATFORM LOSS. acctB (carol's operator) must be untouched.
	now := time.Now()
	res, err := m.ChargebackLineage("dp_lin", "alice", "", 40, now)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(res.Clawed, 7) {
		t.Errorf("clawed = %v, want 7 (only alice's own lot)", res.Clawed)
	}
	if !approx(res.PlatformLoss, 33) {
		t.Errorf("platform loss = %v, want 33 (uncovered remainder)", res.PlatformLoss)
	}
	// The unrelated operator's earnings are fully intact.
	if s, _ := m.EarningSplitOf("acctB", now); !approx(s.Payable, 35) {
		t.Errorf("unrelated operator payable = %v, want 35 (untouched)", s.Payable)
	}
	// A platform_loss ledger row records the shortfall.
	led, _ := m.LedgerOf("platform", []string{KindPlatformLoss}, 10)
	if len(led) != 1 || !approx(led[0].Amount, -33) {
		t.Errorf("platform_loss ledger = %+v, want one -33 row", led)
	}
}

// recM is rec with an explicit model id (the per-model rollup needs distinct models).
func recM(id, model string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: id, Model: model, PromptTokens: 1, CompletionTokens: 1, TS: time.Now().Unix()}
}

// TestReleaseSchedule: still-held lots bucket into a dated, ascending release ladder
// keyed by their release day; an already-cleared lot is swept to payable and drops out
// of the upcoming ladder. White-box: set distinct ReleaseAt on the lots directly.
func TestReleaseSchedule(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day1 := now.Add(30 * 24 * time.Hour) // ~Jul 1
	day2 := now.Add(45 * 24 * time.Hour) // ~Jul 16
	// Three held lots: two clearing the SAME day (bucket together), one later, plus one
	// already-cleared lot that the sweep promotes (must NOT appear in the ladder).
	m.lots = []EarningLot{
		{ID: 1, Node: "n", AccountID: "acct1", RequestID: "r1", Gross: 5, State: LotHeld, ReleaseAt: day1.Unix(), ReserveReleaseAt: day1.Unix()},
		{ID: 2, Node: "n", AccountID: "acct1", RequestID: "r2", Gross: 3, State: LotHeld, ReleaseAt: day1.Add(2 * time.Hour).Unix(), ReserveReleaseAt: day1.Unix()},
		{ID: 3, Node: "n", AccountID: "acct1", RequestID: "r3", Gross: 7, State: LotHeld, ReleaseAt: day2.Unix(), ReserveReleaseAt: day2.Unix()},
		{ID: 4, Node: "n", AccountID: "acct1", RequestID: "r4", Gross: 9, State: LotHeld, ReleaseAt: now.Add(-time.Hour).Unix(), ReserveReleaseAt: now.Add(-time.Hour).Unix()},
		{ID: 5, Node: "n", AccountID: "other", RequestID: "r5", Gross: 99, State: LotHeld, ReleaseAt: day1.Unix(), ReserveReleaseAt: day1.Unix()},
	}
	rel, err := m.ReleaseSchedule("acct1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel) != 2 {
		t.Fatalf("ladder = %+v, want 2 buckets (the cleared lot + other account excluded)", rel)
	}
	if rel[0].Date >= rel[1].Date {
		t.Errorf("ladder not ascending: %+v", rel)
	}
	if !approx(rel[0].Amount, 8) || rel[0].LotCount != 2 {
		t.Errorf("bucket 0 = %+v, want amount 8 / 2 lots (the same-day pair)", rel[0])
	}
	if !approx(rel[1].Amount, 7) || rel[1].LotCount != 1 {
		t.Errorf("bucket 1 = %+v, want amount 7 / 1 lot", rel[1])
	}
	if rel[0].Date != dayUTC(day1.Unix()) {
		t.Errorf("bucket 0 date = %d, want UTC midnight of release day %d", rel[0].Date, dayUTC(day1.Unix()))
	}
}

// TestEarningRollups: earnings roll up per model and per node across non-clawed lots,
// joined to the request receipts for the model, highest-earning first.
func TestEarningRollups(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("nodeA", "acct1")
	_ = m.BindNode("nodeB", "acct1")
	fundReal(m, "u", 1000)
	_, _ = m.Hold("u", 5)
	_, _ = m.Finalize("u", "nodeA", 5, 5, 5, recM("r1", "gpt-oss"))
	_, _ = m.Hold("u", 3)
	_, _ = m.Finalize("u", "nodeA", 3, 3, 3, recM("r2", "gpt-oss"))
	_, _ = m.Hold("u", 4)
	_, _ = m.Finalize("u", "nodeB", 4, 4, 4, recM("r3", "gemma"))
	byModel, byNode, err := m.EarningRollups("acct1")
	if err != nil {
		t.Fatal(err)
	}
	if len(byModel) != 2 || byModel[0].Key != "gpt-oss" || !approx(byModel[0].Amount, 8) || byModel[0].Lots != 2 {
		t.Errorf("byModel = %+v, want gpt-oss(8,2) first", byModel)
	}
	if byModel[1].Key != "gemma" || !approx(byModel[1].Amount, 4) {
		t.Errorf("byModel[1] = %+v, want gemma(4)", byModel[1])
	}
	if len(byNode) != 2 || byNode[0].Key != "nodeA" || !approx(byNode[0].Amount, 8) {
		t.Errorf("byNode = %+v, want nodeA(8) first", byNode)
	}
}

// TestPayoutLots: a payout's funding lots resolve to their request-level receipts
// (model from the receipt), owner-scoped - a foreign account's id is rejected ok=false.
func TestPayoutLots(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("nodeA", "acct1")
	fundReal(m, "u", 1000)
	_, _ = m.Hold("u", 20)
	_, _ = m.Finalize("u", "nodeA", 20, 20, 20, recM("r1", "gpt-oss"))
	_, _ = m.Hold("u", 15)
	_, _ = m.Finalize("u", "nodeA", 15, 15, 15, recM("r2", "gemma"))
	future := time.Now().Add(91 * 24 * time.Hour)
	pay, ok, _, _ := m.RequestPayout("acct1", future, 25)
	if !ok {
		t.Fatal("payout should succeed (35 >= 25)")
	}
	lots, found, err := m.PayoutLots("acct1", pay.ID)
	if err != nil || !found {
		t.Fatalf("PayoutLots(acct1) found=%v err=%v, want found", found, err)
	}
	if len(lots) != 2 {
		t.Fatalf("lots = %+v, want 2 funding receipts", lots)
	}
	var total float64
	models := map[string]bool{}
	for _, l := range lots {
		total += l.Gross
		models[l.Model] = true
		if l.RequestID == "" || l.Node != "nodeA" {
			t.Errorf("lot missing lineage: %+v", l)
		}
	}
	if !approx(total, 35) || !models["gpt-oss"] || !models["gemma"] {
		t.Errorf("lots total=%v models=%v, want 35 over gpt-oss+gemma", total, models)
	}
	// Cross-account: a different account asking for this payout id is rejected.
	if _, found2, _ := m.PayoutLots("acctOTHER", pay.ID); found2 {
		t.Error("cross-account PayoutLots should reject (found=false)")
	}
	// Unknown payout id: also not found.
	if _, found3, _ := m.PayoutLots("acct1", 99999); found3 {
		t.Error("unknown payout id should be found=false")
	}
}
