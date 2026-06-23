package store

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bownux/rogerai/internal/protocol"
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
