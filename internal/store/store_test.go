package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestSeedFundedSpendDoesNotEarn locks the P0-1 invariant: spend paid from FREE seed
// credits records the metering receipt but mints NO operator earning lot (an operator
// must not be able to cash out another account's free seed credits); spend paid from
// REAL (cleared-topup) credits earns the operator normally; a MIXED-funding spend earns
// only on the real remainder. Consumer spend is unchanged in every case.
func TestSeedFundedSpendDoesNotEarn(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	m.policy = LoadPayoutPolicy()
	_ = m.BindNode("n", "acct1")

	// SEED-funded wallet: $10 of free seed, spend $4 (owner share $2.8) -> NO lot.
	_, seeded, _ := m.SeedOnce("seedy", 10)
	if !seeded {
		t.Fatal("seed should have applied")
	}
	if ok, _ := m.Hold("seedy", 4); !ok {
		t.Fatal("hold seedy")
	}
	if _, err := m.Finalize("seedy", "n", 4, 4, 2.8, rec("s1")); err != nil {
		t.Fatal(err)
	}
	// consumer spend recorded in full
	if s, _ := m.SpendOf("seedy"); !approx(s, 4) {
		t.Errorf("seed-funded spend = %v, want 4 (consumer still pays)", s)
	}
	// operator earns NOTHING on seed-funded traffic
	if s, _ := m.EarningSplitOf("acct1", time.Now()); s.Held+s.Payable+s.Reserved != 0 {
		t.Errorf("seed-funded earnings = %+v, want all zero (no payable lot)", s)
	}
	// the receipt still exists (metering preserved) with a ZERO owner share
	rn, _ := m.RecentByNode("n", 10)
	if len(rn) != 1 || rn[0].RequestID != "s1" || rn[0].OwnerShare != 0 {
		t.Errorf("seed-funded receipt = %+v, want one s1 entry with owner_share 0", rn)
	}

	// REAL-funded wallet: $10 real topup, spend $4 (owner share $2.8) -> lot of 2.8.
	fundReal(m, "rich", 10)
	if ok, _ := m.Hold("rich", 4); !ok {
		t.Fatal("hold rich")
	}
	if _, err := m.Finalize("rich", "n", 4, 4, 2.8, rec("r1")); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.EarningSplitOf("acct1", time.Now()); !approx(s.Payable, 2.8) {
		t.Errorf("real-funded earnings = %v, want payable 2.8", s.Payable)
	}

	// MIXED-funding wallet: $3 seed + $7 real (=$10), spend $5 (owner share $5*0.7=3.5).
	// $3 of the cost is seed-funded (drained first), $2 real -> earn only the real
	// fraction 2/5 of 3.5 = 1.4 on node n2 (operator acct2).
	_ = m.BindNode("n2", "acct2")
	_, _, _ = m.SeedOnce("mix", 3)
	fundReal(m, "mix", 7)
	if ok, _ := m.Hold("mix", 5); !ok {
		t.Fatal("hold mix")
	}
	if _, err := m.Finalize("mix", "n2", 5, 5, 3.5, rec("m1")); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.EarningSplitOf("acct2", time.Now()); !approx(s.Payable, 1.4) {
		t.Errorf("mixed-funding earnings = %v, want payable 1.4 (only the real 2/5)", s.Payable)
	}
}

// The core security property of #23: under heavy concurrency, holds serialize so a
// wallet can never be overdrawn (no negative balance = no free inference).
func TestHoldNeverOverdraws(t *testing.T) {
	m := NewMem()
	_, _ = m.BalanceOf("u", 1.0) // 1.0 credit
	var ok int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if held, _ := m.Hold("u", 0.3); held {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 3 { // floor(1.0 / 0.3) = 3
		t.Errorf("successful holds = %d, want 3", ok)
	}
	if bal, _ := m.BalanceOf("u", 0); bal < 0 || !approx(bal, 0.1) {
		t.Errorf("balance = %v, want 0.1 and never negative", bal)
	}
}

func TestHoldFinalizeRelease(t *testing.T) {
	m := NewMem()
	fundReal(m, "u", 10)                    // REAL credits: only real-funded spend earns the operator (P0-1)
	if held, _ := m.Hold("u", 2.0); !held { // balance 8, reserved 2
		t.Fatal("hold should succeed")
	}
	// capture 0.5 of the 2.0 hold, refund 1.5 → balance 9.5
	bal, _ := m.Finalize("u", "n", 2.0, 0.5, 0.35, protocol.UsageReceipt{RequestID: "r", Model: "m", TS: 1})
	if !approx(bal, 9.5) {
		t.Errorf("finalize balance = %v, want 9.5", bal)
	}
	if e, _ := m.EarningsOf("n"); !approx(e, 0.35) {
		t.Errorf("earnings = %v, want 0.35", e)
	}
	if s, _ := m.SpendOf("u"); !approx(s, 0.5) {
		t.Errorf("spend = %v, want 0.5", s)
	}
	// release path: a failed request returns the full hold
	_, _ = m.Hold("u", 2.0) // balance 7.5
	if bal, _ := m.ReleaseHold("u", 2.0); !approx(bal, 9.5) {
		t.Errorf("release balance = %v, want 9.5", bal)
	}
}

func TestMarkProcessedIdempotent(t *testing.T) {
	m := NewMem()
	if first, _ := m.MarkProcessed("evt_1"); !first {
		t.Error("first occurrence should report firstTime=true")
	}
	if first, _ := m.MarkProcessed("evt_1"); first {
		t.Error("duplicate should report firstTime=false (no double-credit)")
	}
	if first, _ := m.MarkProcessed("evt_2"); !first {
		t.Error("a new key should report firstTime=true")
	}
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

func TestDashboardEntries(t *testing.T) {
	m := NewMem()
	fundReal(m, "alice", 100) // REAL credits: only real-funded spend earns the operator (P0-1)
	settle := func(reqID, node string, cost float64, ts int64) {
		rec := protocol.UsageReceipt{RequestID: reqID, Model: "m", PromptTokens: 10, CompletionTokens: 20, TS: ts}
		if _, err := m.Settle("alice", node, cost, cost*0.7, rec); err != nil {
			t.Fatal(err)
		}
	}
	settle("r1", "nodeA", 1.0, 100)
	settle("r2", "nodeB", 2.0, 200)
	settle("r3", "nodeA", 0.5, 300)

	// spend = sum of costs
	if s, _ := m.SpendOf("alice"); s != 3.5 {
		t.Errorf("spend = %v want 3.5", s)
	}
	// balance debited
	if b, _ := m.BalanceOf("alice", 100); b != 100-3.5 {
		t.Errorf("balance = %v want 96.5", b)
	}
	// earnings per node = owner share (float tolerance)
	if e, _ := m.EarningsOf("nodeA"); !approx(e, (1.0+0.5)*0.7) {
		t.Errorf("nodeA earnings = %v", e)
	}

	// recent by user: newest first, limit respected
	rec, _ := m.RecentByUser("alice", 2)
	if len(rec) != 2 || rec[0].RequestID != "r3" || rec[1].RequestID != "r2" {
		t.Errorf("recent-by-user = %+v", rec)
	}
	// recent by node: only nodeA's two entries
	rn, _ := m.RecentByNode("nodeA", 10)
	if len(rn) != 2 || rn[0].RequestID != "r3" || rn[1].RequestID != "r1" {
		t.Errorf("recent-by-node = %+v", rn)
	}
	if !approx(rn[0].OwnerShare, 0.5*0.7) {
		t.Errorf("owner share on entry = %v", rn[0].OwnerShare)
	}
	// unknown user → empty, no spend
	if rec, _ := m.RecentByUser("nobody", 10); len(rec) != 0 {
		t.Errorf("unknown user recent = %+v", rec)
	}
}

func TestAddCredits(t *testing.T) {
	m := NewMem()
	if b, _ := m.AddCredits("u", 10); b != 10 {
		t.Errorf("after +10 got %v", b)
	}
	if b, _ := m.AddCredits("u", 5); b != 15 {
		t.Errorf("after +5 got %v", b)
	}
}

func TestOwnerBinding(t *testing.T) {
	m := NewMem()
	if _, ok, _ := m.OwnerByPubkey("pk1"); ok {
		t.Error("no owner should exist yet")
	}
	if err := m.BindOwner(Owner{GitHubID: 42, Login: "octocat", Pubkey: "pk1"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	o, ok, _ := m.OwnerByPubkey("pk1")
	if !ok || o.GitHubID != 42 || o.Login != "octocat" {
		t.Errorf("owner = %+v ok=%v, want octocat/42", o, ok)
	}
	// a different pubkey is independent
	if _, ok, _ := m.OwnerByPubkey("pk2"); ok {
		t.Error("pk2 should not be bound")
	}
	// re-bind (refresh) updates the login but keeps the binding
	if err := m.BindOwner(Owner{GitHubID: 42, Login: "octocat-renamed", Pubkey: "pk1"}); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if o, _, _ := m.OwnerByPubkey("pk1"); o.Login != "octocat-renamed" {
		t.Errorf("rebind login = %q, want octocat-renamed", o.Login)
	}
}

// TestChargebackByWalletRecency verifies the dispute clawback path used when the
// dispute carries no request id: Chargeback debits the consumer wallet and claws the
// operator lots attributed to that consumer (via the receipts), NEWEST FIRST, capped
// at the disputed amount. It is idempotent on the Stripe dispute id.
func TestChargebackByWalletRecency(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0") // lots become payable immediately
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	fundReal(m, "alice", 100) // REAL credits: only real-funded spend earns the operator (P0-1)

	// Two spends by alice on node n (operator pk1): an older 10-credit lot then a newer
	// 20-credit lot. A third spend by BOB must never be clawed for alice's dispute.
	_ = m.BindNode("n", "pk1")
	mk := func(id, user string, cost float64, ts int64) {
		_, _ = m.Hold(user, cost)
		r := protocol.UsageReceipt{RequestID: id, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: ts}
		_, _ = m.Finalize(user, "n", cost, cost, cost, r)
	}
	fundReal(m, "bob", 100) // REAL credits (P0-1)
	mk("old", "alice", 10, 1000)
	mk("new", "alice", 20, 2000)
	mk("bob1", "bob", 15, 1500)

	// Dispute for 25 credits, no request id -> claw alice's lots newest-first up to EXACTLY
	// 25 (fee=0 here, so cost==gross): the new (20) lot whole, then the old (10) lot
	// PRO-RATA for the remaining 5. Total clawed == 25 (no overshoot). Bob's lot untouched.
	clawed, err := m.Chargeback("dp1", "alice", "", 25, time.Now())
	if err != nil {
		t.Fatalf("Chargeback err: %v", err)
	}
	if !approx(clawed, 25) { // 20 whole + 5 partial = exactly the disputed amount
		t.Errorf("clawed = %v, want 25 (newest 20 whole + 5 pro-rata on the old lot, no overshoot)", clawed)
	}
	// alice wallet debited the disputed 25.
	if bal, _ := m.PeekBalance("alice"); !approx(bal, 100-10-20-25) {
		t.Errorf("alice balance = %v, want %v", bal, 100-10-20-25)
	}
	// pk1 payable = bob's 15 + the old lot's un-clawed remainder (10-5=5) = 20.
	if s, _ := m.EarningSplitOf("pk1", time.Now()); !approx(s.Payable, 20) {
		t.Errorf("operator payable after partial claw = %v, want 20 (bob 15 + old-lot remainder 5)", s.Payable)
	}

	// Idempotent: a redelivery of dp1 changes nothing.
	balBefore, _ := m.PeekBalance("alice")
	clawed2, _ := m.Chargeback("dp1", "alice", "", 25, time.Now())
	if clawed2 != 0 {
		t.Errorf("redelivered dispute clawed = %v, want 0 (idempotent)", clawed2)
	}
	if bal, _ := m.PeekBalance("alice"); bal != balBefore {
		t.Errorf("redelivered dispute changed balance %v -> %v", balBefore, bal)
	}
}

// TestChargebackNoOverClawWithFee locks the fee-aware claw cap: the dispute amount is in
// CONSUMER dollars, so the claw must stop once the clawed lots' consumer COST covers it -
// recovering only the operator's SHARE of the disputed spend, never 1/(1-fee)x more. With a
// 30% platform fee, a $100 dispute claws exactly the operator's $70 share of ONE $100-cost
// lot and books the $30 fee as PlatformLoss, leaving the consumer's OTHER lot intact. The
// old loop (stopping on operator gross >= amount) clawed BOTH lots (140), zeroed the
// operator, and recorded no platform loss - this test fails on that behavior.
func TestChargebackNoOverClawWithFee(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	fundReal(m, "alice", 1000) // real credits so the operator earns the full owner share (P0-1)
	_ = m.BindNode("n", "pk1")

	// Two $100-cost requests by alice; the operator earns 70 each (30% platform fee). Think
	// of them as funded by two different $100 top-ups: only one top-up is disputed.
	mk := func(id string, ts int64) {
		_, _ = m.Hold("alice", 100)
		r := protocol.UsageReceipt{RequestID: id, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: ts}
		_, _ = m.Finalize("alice", "n", 100, 100, 70, r)
	}
	mk("a1", 1000) // older
	mk("a2", 2000) // newer

	res, err := m.ChargebackLineage("dp", "alice", "", 100, time.Now())
	if err != nil {
		t.Fatalf("ChargebackLineage err: %v", err)
	}
	if !approx(res.Clawed, 70) {
		t.Errorf("clawed = %v, want 70 (operator share of the ONE disputed lot, not over-clawed to 140)", res.Clawed)
	}
	if !approx(res.PlatformLoss, 30) {
		t.Errorf("platform loss = %v, want 30 (the platform's fee on the disputed spend)", res.PlatformLoss)
	}
	if !approx(res.Clawed+res.PlatformLoss, 100) { // conservation: recovery + loss == amount
		t.Errorf("clawed(%v)+platformLoss(%v) != disputed amount 100", res.Clawed, res.PlatformLoss)
	}
	// The consumer's OTHER lot (a1) must survive - the claw must not reach into it.
	if s, _ := m.EarningSplitOf("pk1", time.Now()); !approx(s.Payable, 70) {
		t.Errorf("operator payable after claw = %v, want 70 (a1 survives; old over-claw would zero it)", s.Payable)
	}
}

// TestReserveReleasesWithLot pins the Option-A reserve behavior so the half-wired
// "reserve tail" can't silently regress: with a reserve fraction configured, the reserve
// releases TOGETHER with the lot at HoldDays - the reserve_release audit row is emitted at
// promotion, the FULL gross (incl. reserve) becomes payable, and a payout pays it all (the
// reserve is never stranded). A separate later reserve tail is intentionally not supported.
func TestReserveReleasesWithLot(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")  // release immediately
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0.10") // 10% reserve slice
	m := NewMem()
	fundReal(m, "alice", 100) // real credits so the operator earns (P0-1)
	_ = m.BindNode("n", "pk1")
	_, _ = m.Hold("alice", 100)
	r := protocol.UsageReceipt{RequestID: "r1", Model: "m", TS: 1}
	_, _ = m.Finalize("alice", "n", 100, 100, 50, r) // ownerShare 50 -> reserve 5, main 45

	now := time.Now()
	// Promotion releases BOTH the main slice and the reserve (coupled), so the operator's
	// full 50 gross is payable.
	if s, _ := m.EarningSplitOf("pk1", now); !approx(s.Payable, 50) {
		t.Fatalf("payable = %v, want 50 (gross incl. the released reserve)", s.Payable)
	}
	// The reserve_release audit row was recorded exactly once (never silently dropped).
	if led, _ := m.LedgerOf("pk1", []string{KindReserveRelease}, 10); len(led) != 1 || !approx(led[0].Amount, 5) {
		t.Fatalf("reserve_release ledger rows = %+v, want one row of +5", led)
	}
	// A payout pays the FULL 50 - the reserve is not stranded by the lot going Paid.
	if p, ok, _, _ := m.RequestPayout("pk1", now, 0); !ok || !approx(p.Amount, 50) {
		t.Fatalf("payout amount = %v ok=%v, want 50 (reserve paid, not stranded)", p.Amount, ok)
	}
}

// TestSettleFinalizeIdempotentOnRequestID is the regression for the double-finalize
// lot<->ledger drift: a second Settle/Finalize for the SAME requestID must be a no-op - no
// double refund/debit, no doubled earnings, no duplicate earning lot. The broker's `settled`
// flag normally prevents a second call, but the store primitive must not drift if it recurs.
func TestSettleFinalizeIdempotentOnRequestID(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	// Runs against Mem AND (when ROGERAI_TEST_DATABASE_URL is set) the real Postgres store -
	// the production money path must be idempotent too, not just the reference.
	for name, db := range metricsStores(t) {
		t.Run(name, func(t *testing.T) {
			// Unique ids per (store,run) so the shared Postgres DB has no cross-run carryover.
			uid := name + "-" + time.Now().UTC().Format("150405.000000000")
			u, v, node, op := "u-"+uid, "v-"+uid, "n-"+uid, "op-"+uid
			_ = db.BindNode(node, op)
			_, _ = db.AddCredits(u, 10)
			_, _ = db.Hold(u, 5)
			rec := protocol.UsageReceipt{RequestID: "r1-" + uid, Model: "m", TS: 1}
			bal1, _ := db.Finalize(u, node, 5, 2, 1.4, rec)
			bal2, _ := db.Finalize(u, node, 5, 2, 1.4, rec) // duplicate -> must be a no-op
			if !approx(bal1, 8) || !approx(bal2, 8) {
				t.Fatalf("[%s] balances = %v, %v want 8, 8 (a duplicate Finalize must not double-refund)", name, bal1, bal2)
			}
			if e, _ := db.EarningsOf(node); !approx(e, 1.4) {
				t.Fatalf("[%s] earnings = %v, want 1.4 (not doubled by the duplicate Finalize)", name, e)
			}
			if s, _ := db.EarningSplitOf(op, time.Now()); !approx(s.Payable, 1.4) {
				t.Fatalf("[%s] operator payable = %v, want 1.4 (exactly one lot, no drift)", name, s.Payable)
			}
			// Settle (direct-debit path) is idempotent too.
			_, _ = db.AddCredits(v, 10)
			r2 := protocol.UsageReceipt{RequestID: "r2-" + uid, Model: "m", TS: 2}
			sb1, _ := db.Settle(v, node, 3, 2.1, r2)
			sb2, _ := db.Settle(v, node, 3, 2.1, r2) // duplicate -> no-op
			if !approx(sb1, 7) || !approx(sb2, 7) {
				t.Fatalf("[%s] settle balances = %v, %v want 7, 7 (a duplicate Settle must not double-debit)", name, sb1, sb2)
			}
		})
	}
}

// TestChargebackPartialDisputeProRata is the regression for the partial-dispute over-claw:
// a dispute SMALLER than a single lot's consumer cost must recover only the operator's
// PROPORTIONAL share of the disputed amount (not the whole lot), with conservation intact.
// Before the fix, the whole $140 lot was clawed for a $100 dispute -> recovered > disputed,
// no platform-loss row, conservation broken.
func TestChargebackPartialDisputeProRata(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	fundReal(m, "alice", 1000)
	_ = m.BindNode("n1", "op1")
	// One $200-cost request; operator earns $140 (30% fee).
	_, _ = m.Hold("alice", 200)
	if _, err := m.Finalize("alice", "n1", 200, 200, 140, protocol.UsageReceipt{RequestID: "r_big", Model: "m", TS: 1}); err != nil {
		t.Fatal(err)
	}
	res, err := m.ChargebackLineage("dp", "alice", "", 100, time.Now()) // dispute < the one lot's cost
	if err != nil {
		t.Fatal(err)
	}
	if !approx(res.Clawed, 70) { // 140 * (100/200) = the operator's share of the disputed $100
		t.Fatalf("clawed = %v, want 70 (pro-rata operator share of the $100 dispute, not the whole $140 lot)", res.Clawed)
	}
	if !approx(res.PlatformLoss, 30) {
		t.Fatalf("platform loss = %v, want 30 (the fee share of the disputed $100)", res.PlatformLoss)
	}
	if res.Clawed > 100+1e-9 {
		t.Fatalf("recovered %v exceeds the disputed 100 (over-claw)", res.Clawed)
	}
	if !approx(res.Clawed+res.PlatformLoss, 100) { // conservation
		t.Fatalf("clawed(%v)+loss(%v) != disputed 100", res.Clawed, res.PlatformLoss)
	}
	// The operator keeps the remaining $70 (lot reduced by the clawed share, not wiped).
	if s, _ := m.EarningSplitOf("op1", time.Now()); !approx(s.Payable, 70) {
		t.Fatalf("operator payable after partial claw = %v, want 70", s.Payable)
	}
}

// TestLinkChargeWalletByCharge verifies the checkout->charge mapping: a charge can be
// resolved by EITHER the payment_intent or the charge id; an unknown ref reports
// ok=false; and the mapping is idempotent on the session id.
func TestLinkChargeWalletByCharge(t *testing.T) {
	m := NewMem()
	if err := m.LinkCharge("cs_1", "pi_1", "ch_1", "u_gh_5", 42); err != nil {
		t.Fatalf("LinkCharge: %v", err)
	}
	for _, ref := range []string{"pi_1", "ch_1"} {
		w, c, ok, err := m.WalletByCharge(ref)
		if err != nil || !ok || w != "u_gh_5" || c != 42 {
			t.Errorf("WalletByCharge(%q) = %q,%v,%v,%v want u_gh_5,42,true,nil", ref, w, c, ok, err)
		}
	}
	if _, _, ok, _ := m.WalletByCharge("pi_unknown"); ok {
		t.Error("unknown ref must resolve ok=false")
	}
	if _, _, ok, _ := m.WalletByCharge(""); ok {
		t.Error("empty ref must resolve ok=false")
	}
}

// TestSeedCapGrantsUnderLimit: the first SEED_LIMIT distinct wallets get the seed via
// SeedOnce; wallets at/after the limit get 0. Already-seeded wallets are unaffected.
func TestSeedCapGrantsUnderLimit(t *testing.T) {
	m := NewMem()
	m.SetSeedLimit(3)
	const seed = 100.0
	// First 3 wallets: seeded.
	for i := 0; i < 3; i++ {
		w := "u" + string(rune('A'+i))
		bal, seeded, _ := m.SeedOnce(w, seed)
		if !seeded || bal != seed {
			t.Fatalf("wallet %s under limit should be seeded %g, got bal=%g seeded=%v", w, seed, bal, seeded)
		}
	}
	// 4th+ wallets: no seed (cap exhausted), wallet exists at 0.
	for i := 3; i < 6; i++ {
		w := "u" + string(rune('A'+i))
		bal, seeded, _ := m.SeedOnce(w, seed)
		if seeded || bal != 0 {
			t.Fatalf("wallet %s at/after limit should get 0, got bal=%g seeded=%v", w, bal, seeded)
		}
		// And it must NOT be seeded later by BalanceOf either (cap is global).
		if b, _ := m.BalanceOf(w, seed); b != 0 {
			t.Fatalf("capped wallet %s must stay 0 via BalanceOf, got %g", w, b)
		}
	}
	// An already-seeded wallet is never re-seeded or clawed back.
	if b, seeded, _ := m.SeedOnce("uA", seed); b != seed || seeded {
		t.Fatalf("re-seed of uA must be a no-op at %g, got bal=%g seeded=%v", seed, b, seeded)
	}
}

// TestSeedCapBalanceOfPath: the auto-seed path (BalanceOf) honors the same cap.
func TestSeedCapBalanceOfPath(t *testing.T) {
	m := NewMem()
	m.SetSeedLimit(2)
	const seed = 50.0
	if b, _ := m.BalanceOf("a", seed); b != seed {
		t.Fatalf("a under limit -> %g, got %g", seed, b)
	}
	if b, _ := m.BalanceOf("b", seed); b != seed {
		t.Fatalf("b under limit -> %g, got %g", seed, b)
	}
	if b, _ := m.BalanceOf("c", seed); b != 0 {
		t.Fatalf("c at limit -> 0, got %g", b)
	}
	// derive-balance must match the cached balance for a capped wallet (no orphan row).
	if d, _ := m.DeriveBalance("c"); d != 0 {
		t.Fatalf("capped wallet c DeriveBalance should be 0, got %g", d)
	}
}

// TestSeedCapUnlimited: limit<=0 disables the cap (every new wallet seeded).
func TestSeedCapUnlimited(t *testing.T) {
	m := NewMem()
	m.SetSeedLimit(0)
	for i := 0; i < 50; i++ {
		w := "w" + string(rune(i))
		if b, _, _ := m.SeedOnce(w, 10); b != 10 {
			t.Fatalf("unlimited: wallet %d should be seeded 10, got %g", i, b)
		}
	}
}

// TestSeedCapAtomicNoDoubleGrant: a burst of concurrent first-seeds across MANY
// distinct wallets must grant EXACTLY `limit` of them (no over-grant under load).
func TestSeedCapAtomicNoDoubleGrant(t *testing.T) {
	m := NewMem()
	const limit = 10
	const seed = 7.0
	m.SetSeedLimit(limit)
	const n = 200
	var wg sync.WaitGroup
	var granted int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := "wallet-" + string(rune('a'+i%26)) + "-" + itoa(i)
			if _, seeded, _ := m.SeedOnce(w, seed); seeded {
				atomic.AddInt64(&granted, 1)
			}
		}(i)
	}
	wg.Wait()
	if granted != limit {
		t.Fatalf("expected exactly %d seeded under concurrency, got %d", limit, granted)
	}
	if m.seedCount != limit {
		t.Fatalf("durable seedCount should equal limit %d, got %d", limit, m.seedCount)
	}
}

// itoa is a tiny int->string for unique test wallet ids (avoids strconv import churn).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
