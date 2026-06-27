package store

import (
	"testing"
	"time"
)

// TestAppendLedgerIdemDedup locks the free idempotency on m.appendLedgerLocked: a second
// append with the same idem key is a no-op (no duplicate ledger row).
func TestAppendLedgerIdemDedup(t *testing.T) {
	m := NewMem()
	m.mu.Lock()
	m.appendLedgerLocked("h", "consumer", KindTopup, 5, "dup-key", StatePosted, "ref", 1)
	m.appendLedgerLocked("h", "consumer", KindTopup, 5, "dup-key", StatePosted, "ref", 1) // dup -> dropped
	m.mu.Unlock()
	rows, _ := m.LedgerOf("h", nil, 0)
	if len(rows) != 1 {
		t.Errorf("ledger rows = %d, want 1 (duplicate idem key deduped)", len(rows))
	}
}

// TestSeedStatusUnlimitedAndOverflow covers SeedStatus's unlimited (limit<=0 -> remaining
// -1) arm and the remaining-clamp arm (seeded already past a later, smaller limit -> 0).
func TestSeedStatusUnlimitedAndOverflow(t *testing.T) {
	m := NewMem()
	// Fresh store, no limit set: unlimited -> remaining -1.
	if s, l, r, _ := m.SeedStatus(); s != 0 || l != 0 || r != -1 {
		t.Errorf("unlimited SeedStatus = %d/%d/%d, want 0/0/-1", s, l, r)
	}
	// Seed two wallets under a generous cap, then lower the cap below the count.
	m.SetSeedLimit(5)
	if _, seeded, _ := m.SeedOnce("w1", 10); !seeded {
		t.Fatal("w1 should seed under the cap")
	}
	if _, seeded, _ := m.SeedOnce("w2", 10); !seeded {
		t.Fatal("w2 should seed under the cap")
	}
	m.SetSeedLimit(1) // now seeded(2) > limit(1)
	if s, l, r, _ := m.SeedStatus(); s != 2 || l != 1 || r != 0 {
		t.Errorf("overflow SeedStatus = %d/%d/%d, want 2/1/0 (remaining clamped)", s, l, r)
	}
}

// TestSeedOnceAlreadySeeded covers the grantSeed already-seeded short-circuit: a second
// SeedOnce for the same wallet does not re-credit and reports seeded=false.
func TestSeedOnceAlreadySeeded(t *testing.T) {
	m := NewMem()
	if b, seeded, _ := m.SeedOnce("w", 10); !seeded || !approx(b, 10) {
		t.Fatalf("first SeedOnce = %v/%v, want 10/true", b, seeded)
	}
	if b, seeded, _ := m.SeedOnce("w", 10); seeded || !approx(b, 10) {
		t.Errorf("second SeedOnce = %v/%v, want 10/false (already seeded)", b, seeded)
	}
}

// TestAccountLookupMissesMem covers the not-found arms of the owner-mutation helpers: an
// unknown login is a clean no-op (UpdateAccount false / SetConnect nil), and LedgerOf
// honours the limit cap.
func TestAccountLookupMissesMem(t *testing.T) {
	m := NewMem()
	if o, ok, _ := m.UpdateAccount("ghost", "e@x.com"); ok || o.Login != "" {
		t.Errorf("UpdateAccount(ghost) = %+v ok=%v, want zero/false", o, ok)
	}
	if err := m.SetConnect("ghost", "acct", "active"); err != nil {
		t.Errorf("SetConnect(ghost) = %v, want nil (no-op)", err)
	}

	// LedgerOf limit cap: two topups, ask for one.
	if _, err := m.AddCredits("u", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddCredits("u", 7); err != nil {
		t.Fatal(err)
	}
	if rows, _ := m.LedgerOf("u", nil, 1); len(rows) != 1 {
		t.Errorf("LedgerOf(limit=1) = %d rows, want 1", len(rows))
	}
}

// TestSettlePayoutUnknownMem covers SettlePayout on an unknown payout id (a clean no-op).
func TestSettlePayoutUnknownMem(t *testing.T) {
	m := NewMem()
	if err := m.SettlePayout(999999, "tr"); err != nil {
		t.Errorf("SettlePayout(unknown) = %v, want nil (no-op)", err)
	}
}

// TestPendingReversalEdgesMem covers RecordPendingReversal's empty-key no-op, the
// OpenPendingReversals sort+limit cap with multiple rows, and MarkReversalAttempt on an
// unknown key (no-op).
func TestPendingReversalEdgesMem(t *testing.T) {
	m := NewMem()
	// Empty key: nothing recorded.
	if err := m.RecordPendingReversal(PendingReversal{Key: ""}); err != nil {
		t.Fatal(err)
	}
	// Two real reversals, the second created earlier -> sorts first by created_at asc.
	_ = m.RecordPendingReversal(PendingReversal{Key: "k-late", DisputeID: "d1", Amount: 1, CreatedAt: 2000})
	_ = m.RecordPendingReversal(PendingReversal{Key: "k-early", DisputeID: "d2", Amount: 2, CreatedAt: 1000})
	open, _ := m.OpenPendingReversals(0)
	if len(open) != 2 || open[0].Key != "k-early" {
		t.Fatalf("open (sorted) = %+v, want k-early first", open)
	}
	// Limit cap returns just the oldest.
	if lim, _ := m.OpenPendingReversals(1); len(lim) != 1 || lim[0].Key != "k-early" {
		t.Errorf("open(limit=1) = %+v, want only k-early", lim)
	}
	// Unknown key attempt: no-op, no error.
	if err := m.MarkReversalAttempt("ghost", false, "boom", 3, time.Now()); err != nil {
		t.Errorf("MarkReversalAttempt(unknown) = %v, want nil", err)
	}
}
