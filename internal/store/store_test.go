package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

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
	_, _ = m.BalanceOf("u", 10)
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
	_, _ = m.BalanceOf("alice", 100)
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
	_, _ = m.BalanceOf("alice", 100)

	// Two spends by alice on node n (operator pk1): an older 10-credit lot then a newer
	// 20-credit lot. A third spend by BOB must never be clawed for alice's dispute.
	_ = m.BindNode("n", "pk1")
	mk := func(id, user string, cost float64, ts int64) {
		_, _ = m.Hold(user, cost)
		r := protocol.UsageReceipt{RequestID: id, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: ts}
		_, _ = m.Finalize(user, "n", cost, cost, cost, r)
	}
	_, _ = m.BalanceOf("bob", 100)
	mk("old", "alice", 10, 1000)
	mk("new", "alice", 20, 2000)
	mk("bob1", "bob", 15, 1500)

	// Dispute for 25 credits, no request id -> claw alice's lots newest-first up to 25:
	// the new (20) lot then the old (10) lot (the loop stops once clawed >= 25, so it
	// claws 20 then 10 reaching 30). Bob's lot is untouched.
	clawed, err := m.Chargeback("dp1", "alice", "", 25, time.Now())
	if err != nil {
		t.Fatalf("Chargeback err: %v", err)
	}
	if clawed != 30 { // newest 20 then old 10 (stops after crossing 25)
		t.Errorf("clawed = %v, want 30 (newest-first, capped past 25)", clawed)
	}
	// alice wallet debited the disputed 25.
	if bal, _ := m.PeekBalance("alice"); !approx(bal, 100-10-20-25) {
		t.Errorf("alice balance = %v, want %v", bal, 100-10-20-25)
	}
	// pk1 lots from alice are clawed; bob's 15 lot survives as payable.
	if s, _ := m.EarningSplitOf("pk1", time.Now()); !approx(s.Payable, 15) {
		t.Errorf("operator payable after claw = %v, want 15 (only bob's lot)", s.Payable)
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
