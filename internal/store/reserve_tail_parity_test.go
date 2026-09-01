package store

// The Option B remnant round-trip, on BOTH backends (Mem and, when
// ROGERAI_TEST_DATABASE_URL is set - as the coverage gate always does - the real
// Postgres store): a payout pays the principal, the remnant reserve survives as
// reserved money, a premature second payout is refused, and the reserve pays out
// whole after its tail. The audit's catch behind this pin: the PG payout CTE once
// flipped a net-zero remnant to 'paid' without paying it - reserve silently lost.

import (
	"testing"
	"time"
)

func TestReserveTailPayoutParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			uid := name + "-" + time.Now().UTC().Format("150405.000000000")
			node, op := "n-rsv-"+uid, "op-rsv-"+uid
			db.SetPayoutPolicy(PayoutPolicy{HoldDays: 30, Reserve: 0.10, ReserveDays: 90, MinPayout: 25, Schedule: "monthly"})
			if err := db.BindNode(node, op); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := db.AddOperatorLot(node, op, "rq-rsv-"+uid, 400, now.Add(-31*24*time.Hour)); err != nil {
				t.Fatal(err)
			}

			p, ok, _, err := db.RequestPayout(op, now, 25)
			if err != nil || !ok || !approx(p.Amount, 360) {
				t.Fatalf("first payout = %v ok=%v err=%v, want 360 (the principal)", p.Amount, ok, err)
			}
			s, _ := db.EarningSplitOf(op, now)
			if !approx(s.Reserved, 40) || s.Payable > 1e-9 || !approx(s.Paid, 360) {
				t.Fatalf("post-payout split = %+v, want reserved 40 / payable 0 / paid 360", s)
			}
			if _, ok, reason, _ := db.RequestPayout(op, now, 25); ok {
				t.Fatalf("second payout inside the tail was allowed (reason %q)", reason)
			}

			later := now.Add(60 * 24 * time.Hour) // day 91: the tail has cleared
			p2, ok, _, err := db.RequestPayout(op, later, 25)
			if err != nil || !ok || !approx(p2.Amount, 40) {
				t.Fatalf("post-tail payout = %v ok=%v err=%v, want the 40 remnant, whole", p2.Amount, ok, err)
			}
			s2, _ := db.EarningSplitOf(op, later)
			if s2.Reserved > 1e-9 || !approx(s2.Paid, 400) {
				t.Fatalf("final split = %+v, want reserved 0 / paid 400 (nothing lost, nothing double-paid)", s2)
			}
		})
	}
}
