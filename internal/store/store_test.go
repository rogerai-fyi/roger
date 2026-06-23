package store

import (
	"testing"

	"github.com/bownux/rogerai/internal/protocol"
)

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
