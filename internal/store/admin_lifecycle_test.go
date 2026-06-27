package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestAdminAbuseBannedOwnerOrderParity locks the banned-owner ordering in the abuse
// rollup with TWO banned owners (so the sort comparator actually runs), on BOTH backends.
func TestAdminAbuseBannedOwnerOrderParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			_ = db.BanOwner("acct-zzz", "late", "{}")
			_ = db.BanOwner("acct-aaa", "early", "{}")
			a, err := db.AdminAbuse()
			if err != nil {
				t.Fatal(err)
			}
			if len(a.BannedOwners) != 2 {
				t.Fatalf("[%s] banned owners = %d, want 2", name, len(a.BannedOwners))
			}
			if a.BannedOwners[0].AccountID != "acct-aaa" || a.BannedOwners[1].AccountID != "acct-zzz" {
				t.Errorf("[%s] banned owner order = %q,%q, want acct-aaa,acct-zzz (account-id asc)",
					name, a.BannedOwners[0].AccountID, a.BannedOwners[1].AccountID)
			}
			_ = db.Close()
		})
	}
}

// TestAdminFinancialsLotLifecycleParity walks one operator's earnings through EVERY lot
// state and exercises the super-admin financial rollup at each stage on BOTH backends:
//   - HELD  (a 120-day hold, read at "now"): gross-minus-reserve in Held, reserve in Reserved.
//   - PAYABLE (read 200 days out, after promotion): the reserve releases into Payable.
//   - PAID    (after RequestPayout sweeps an account's payable lots): Paid.
//   - CLAWED  (after a dispute claws the remaining account's lot): Clawed drops out of
//     OperatorEarned, and the uncovered disputed remainder books a PlatformLoss.
//
// This covers the LotHeld/LotPayable/LotPaid/LotClawed arms and the topup/platform-loss
// ledger reads of AdminFinancials (Mem and Postgres), plus AdminPayoutQueue's held/paid
// columns and promoteLots.
func TestAdminFinancialsLotLifecycleParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120") // lots stay HELD at "now"
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0.10")  // 10% reserve slice
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			A, B := "acct-life-A", "acct-life-B"
			_ = db.BindOwner(Owner{Pubkey: A, GitHubID: 101, Login: "life-a"})
			_ = db.BindOwner(Owner{Pubkey: B, GitHubID: 102, Login: "life-b"})
			_ = db.BindNode("nA", A)
			_ = db.BindNode("nB", B)

			u := "u-life"
			if _, err := db.AddCredits(u, 1000); err != nil { // TopupVolume 1000
				t.Fatal(err)
			}
			// Three $100 spends, operator share 70 each: two on A, one on B.
			settle := func(node, req string) {
				if _, err := db.Settle(u, node, 100, 70, protocol.UsageReceipt{
					RequestID: req, Model: "m", PromptTokens: 10, CompletionTokens: 20, TS: 1000,
				}); err != nil {
					t.Fatalf("Settle %s: %v", req, err)
				}
			}
			settle("nA", "rA1")
			settle("nA", "rA2")
			settle("nB", "rB1")

			// --- Stage 1: all HELD (read at now, release ~120 days out). reserve = 7/lot. ---
			f, err := db.AdminFinancials(now)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(f.Held, 3*63) || !approx(f.Reserved, 3*7) || !approx(f.Payable, 0) {
				t.Errorf("[%s] stage1 = held %v / reserved %v / payable %v, want 189/21/0", name, f.Held, f.Reserved, f.Payable)
			}
			if !approx(f.OperatorEarned, 210) || !approx(f.ConsumerSpend, 300) || !approx(f.PlatformFee, 90) {
				t.Errorf("[%s] stage1 money = earned %v / spend %v / fee %v, want 210/300/90", name, f.OperatorEarned, f.ConsumerSpend, f.PlatformFee)
			}
			if !approx(f.TopupVolume, 1000) {
				t.Errorf("[%s] stage1 topup = %v, want 1000", name, f.TopupVolume)
			}
			// AdminPayoutQueue while the lots are still HELD: held columns populated, none payable.
			qh, err := db.AdminPayoutQueue(now, 0)
			if err != nil {
				t.Fatal(err)
			}
			var heldTotal float64
			for _, r := range qh {
				heldTotal += r.Held
				if r.Payable != 0 {
					t.Errorf("[%s] stage1 queue row %+v has payable, want 0 (all held)", name, r)
				}
			}
			if !approx(heldTotal, 3*63) {
				t.Errorf("[%s] stage1 queue held total = %v, want 189", name, heldTotal)
			}

			// --- Stage 2: 200 days out -> all PAYABLE, reserve released into Payable. ---
			future := now.Add(200 * 24 * time.Hour)
			f2, err := db.AdminFinancials(future)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(f2.Payable, 210) || !approx(f2.Held, 0) || !approx(f2.Reserved, 0) {
				t.Errorf("[%s] stage2 = payable %v / held %v / reserved %v, want 210/0/0", name, f2.Payable, f2.Held, f2.Reserved)
			}

			// AdminPayoutQueue mid-lifecycle: A owes 140 payable, B owes 70 (A sorts first).
			q, err := db.AdminPayoutQueue(future, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(q) != 2 || q[0].AccountID != A || !approx(q[0].Payable, 140) || q[1].AccountID != B || !approx(q[1].Payable, 70) {
				t.Fatalf("[%s] queue = %+v, want A(140) then B(70)", name, q)
			}
			// The limit cap returns only the top row (A).
			if qlim, _ := db.AdminPayoutQueue(future, 1); len(qlim) != 1 || qlim[0].AccountID != A {
				t.Errorf("[%s] queue(limit=1) = %+v, want only A", name, qlim)
			}

			// --- Stage 3: pay A out -> A's two lots PAID. ---
			if _, ok, _, err := db.RequestPayout(A, future, 1); err != nil || !ok {
				t.Fatalf("[%s] RequestPayout(A) ok=%v err=%v", name, ok, err)
			}
			// AdminAllPayouts + AdminActivity honour the limit cap (one row each).
			if ap, err := db.AdminAllPayouts(1); err != nil || len(ap) != 1 {
				t.Errorf("[%s] AdminAllPayouts(1) = %d rows (err %v), want 1", name, len(ap), err)
			}
			if act, err := db.AdminActivity(1); err != nil || len(act) != 1 {
				t.Errorf("[%s] AdminActivity(1) = %d rows (err %v), want 1", name, len(act), err)
			}
			f3, err := db.AdminFinancials(future)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(f3.Paid, 140) || !approx(f3.Payable, 70) {
				t.Errorf("[%s] stage3 = paid %v / payable %v, want 140/70", name, f3.Paid, f3.Payable)
			}
			if !approx(f3.OperatorEarned, 210) {
				t.Errorf("[%s] stage3 earned = %v, want 210 (paid+payable both count)", name, f3.OperatorEarned)
			}

			// --- Stage 4: dispute B's $100 charge -> B's lot CLAWED, $30 fee a platform loss. ---
			res, err := db.ChargebackLineage("disp-life", u, "rB1", 100, future)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(res.Clawed, 70) || !approx(res.PlatformLoss, 30) {
				t.Errorf("[%s] chargeback = clawed %v / loss %v, want 70/30", name, res.Clawed, res.PlatformLoss)
			}
			f4, err := db.AdminFinancials(future)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(f4.Clawed, 70) || !approx(f4.Payable, 0) {
				t.Errorf("[%s] stage4 = clawed %v / payable %v, want 70/0", name, f4.Clawed, f4.Payable)
			}
			if !approx(f4.PlatformLoss, 30) {
				t.Errorf("[%s] stage4 platform loss = %v, want 30", name, f4.PlatformLoss)
			}
			// Clawed gross no longer counts as operator-earned: 140 paid (A) remain.
			if !approx(f4.OperatorEarned, 140) {
				t.Errorf("[%s] stage4 earned = %v, want 140 (clawed lot dropped)", name, f4.OperatorEarned)
			}
			// PlatformFee = consumer spend 300 - operator earned 140 = 160 (clamped >= 0).
			if !approx(f4.PlatformFee, 160) {
				t.Errorf("[%s] stage4 fee = %v, want 160", name, f4.PlatformFee)
			}
		})
	}
}
