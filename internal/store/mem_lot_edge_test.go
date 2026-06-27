package store

import (
	"testing"
	"time"
)

// TestStagedLotReserveTailSplit covers the "reserve tail" arm that the coupled-reserve
// Finalize path can't produce on its own: a PAYABLE lot whose reserve_release_at is still
// in the future keeps its reserve in Reserved (not Payable), arms NextRelease, and a
// payout sweeps only the released net (a net-zero lot is skipped). Staged via the
// SeedLotsForTest seam so the reserve clock can be set independently of the lot's release.
func TestStagedLotReserveTailSplit(t *testing.T) {
	m := NewMem()
	now := time.Now()
	past := now.Add(-time.Hour).Unix()
	future := now.Add(24 * time.Hour).Unix()
	m.SeedLotsForTest([]EarningLot{
		// Released net 6, reserve 4 still locked until `future`.
		{ID: 1, Node: "n", AccountID: "acct-rt", RequestID: "r1", Gross: 10, Reserve: 4, State: LotPayable, ReleaseAt: past, ReserveReleaseAt: future},
		// Net zero (gross==reserve), reserve still locked: contributes nothing payable.
		{ID: 2, Node: "n", AccountID: "acct-rt", RequestID: "r2", Gross: 4, Reserve: 4, State: LotPayable, ReleaseAt: past, ReserveReleaseAt: future},
	})

	s, err := m.EarningSplitOf("acct-rt", now)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(s.Payable, 6) || !approx(s.Reserved, 8) {
		t.Fatalf("split = payable %v / reserved %v, want 6 / 8 (reserve still locked)", s.Payable, s.Reserved)
	}
	if s.NextRelease != future {
		t.Errorf("NextRelease = %d, want %d (the locked reserve's release)", s.NextRelease, future)
	}

	// RequestPayout sweeps only the released net 6; the net-zero lot is skipped.
	p, ok, _, err := m.RequestPayout("acct-rt", now, 1)
	if err != nil || !ok {
		t.Fatalf("RequestPayout ok=%v err=%v", ok, err)
	}
	if !approx(p.Amount, 6) {
		t.Errorf("payout amount = %v, want 6 (released net only)", p.Amount)
	}
}

// TestStagedLotRollupsAndSchedule covers EarningRollups (clawed lots excluded; equal-gross
// rows tie-break by key) and ReleaseSchedule (a net-zero held lot is skipped), staged via
// SeedLotsForTest.
func TestStagedLotRollupsAndSchedule(t *testing.T) {
	m := NewMem()
	now := time.Now()
	future := now.Add(48 * time.Hour).Unix()
	m.SeedLotsForTest([]EarningLot{
		{ID: 1, Node: "nA", AccountID: "acct-rl", RequestID: "rA", Gross: 7, State: LotHeld, ReleaseAt: future},
		{ID: 2, Node: "nB", AccountID: "acct-rl", RequestID: "rB", Gross: 7, State: LotHeld, ReleaseAt: future},             // equal gross -> key tie-break
		{ID: 3, Node: "nC", AccountID: "acct-rl", RequestID: "rC", Gross: 4, Reserve: 4, State: LotHeld, ReleaseAt: future}, // net zero -> skipped by schedule
		{ID: 4, Node: "nA", AccountID: "acct-rl", RequestID: "rD", Gross: 5, State: LotClawed, ReleaseAt: future},           // clawed -> excluded from rollups
	})

	byModel, byNode, err := m.EarningRollups("acct-rl")
	if err != nil {
		t.Fatal(err)
	}
	// Clawed lot excluded: total non-clawed gross = 7+7+4 = 18 across one (empty) model key.
	var totModel float64
	for _, r := range byModel {
		totModel += r.Amount
	}
	if !approx(totModel, 18) {
		t.Errorf("byModel total = %v, want 18 (clawed 5 excluded)", totModel)
	}
	// byNode: nA(7), nB(7), nC(4). The 7/7 tie breaks on key ascending -> nA before nB.
	if len(byNode) != 3 {
		t.Fatalf("byNode = %+v, want 3 rows", byNode)
	}
	if byNode[0].Key != "nA" || byNode[1].Key != "nB" {
		t.Errorf("byNode order = %q,%q, want nA,nB (equal gross -> key asc)", byNode[0].Key, byNode[1].Key)
	}
	if byNode[2].Key != "nC" || !approx(byNode[2].Amount, 4) {
		t.Errorf("byNode[2] = %+v, want nC(4)", byNode[2])
	}

	// ReleaseSchedule: the net-zero held lot (nC) is skipped; nA+nB (14) bucket on one day.
	rel, err := m.ReleaseSchedule("acct-rl", now)
	if err != nil {
		t.Fatal(err)
	}
	var totRel float64
	var lots int
	for _, b := range rel {
		totRel += b.Amount
		lots += b.LotCount
	}
	if !approx(totRel, 14) || lots != 2 {
		t.Errorf("release schedule total=%v lots=%d, want 14 / 2 (net-zero lot skipped)", totRel, lots)
	}
}

// TestPayoutEdgeCasesMem covers the PayoutsOf limit cap and FailPayout on an already-
// settled payout (a no-op rollback), on the reference Mem store.
func TestPayoutEdgeCasesMem(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	now := time.Now().Add(time.Hour)
	_ = m.BindNode("n", "acct-pe")
	fundReal(m, "u", 100)

	// Two separate finalized lots -> two separate payouts.
	finalizeOne(t, m, "u", "n", "r1", "model", 5)
	p1, ok, _, err := m.RequestPayout("acct-pe", now, 1)
	if err != nil || !ok {
		t.Fatalf("RequestPayout 1 ok=%v err=%v", ok, err)
	}
	finalizeOne(t, m, "u", "n", "r2", "model", 3)
	if _, ok, _, err := m.RequestPayout("acct-pe", now, 1); err != nil || !ok {
		t.Fatalf("RequestPayout 2 ok=%v err=%v", ok, err)
	}

	// PayoutsOf with limit 1 returns exactly one of the two payouts.
	if got, _ := m.PayoutsOf("acct-pe", 1); len(got) != 1 {
		t.Errorf("PayoutsOf(limit=1) = %d, want 1", len(got))
	}
	if got, _ := m.PayoutsOf("acct-pe", 0); len(got) != 2 {
		t.Errorf("PayoutsOf(all) = %d, want 2", len(got))
	}

	// Settle p1, then FailPayout(p1) must be a no-op (already settled, not pending).
	if err := m.SettlePayout(p1.ID, "tr_x"); err != nil {
		t.Fatal(err)
	}
	if err := m.FailPayout(p1.ID); err != nil {
		t.Errorf("FailPayout(settled) = %v, want nil (no-op rollback)", err)
	}
	// p1 stays PAID (the fail did not roll a settled payout back).
	ps, _ := m.PayoutsOf("acct-pe", 0)
	for _, p := range ps {
		if p.ID == p1.ID && p.State != PayoutPaid {
			t.Errorf("p1 state = %q, want paid (FailPayout must not roll back a settled payout)", p.State)
		}
	}
}
