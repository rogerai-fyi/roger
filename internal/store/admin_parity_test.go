package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestAdminFinancialsMarketPayouts covers the super-admin money + market + payout
// rollups on BOTH backends end-to-end: two topups/settles across two accounts build
// payable lots; the financial split, the receipt-derived market totals (with the
// window filter), the payout queue ordering, and then a RequestPayout that flips
// payable->paid and surfaces as a pending payout.
func TestAdminFinancialsMarketPayouts(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0") // earnings become payable immediately
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")   // no reserve slice
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Lots are stamped with the real wall clock at Settle time; advance the admin
			// clock past it so the hold=0 lots promote held->payable on read.
			now := time.Now().Add(time.Hour)
			A, B := "acct-A", "acct-B"
			_ = db.BindOwner(Owner{Pubkey: A, GitHubID: 1, Login: "a"})
			_ = db.BindOwner(Owner{Pubkey: B, GitHubID: 2, Login: "b"})
			_ = db.BindNode("nA", A)
			_ = db.BindNode("nB", B)

			u := "u-adm"
			if _, err := db.AddCredits(u, 100); err != nil { // TopupVolume 100
				t.Fatal(err)
			}
			// Two settles: A earns 7 on a 10 spend; B earns 14 on a 20 spend.
			if _, err := db.Settle(u, "nA", 10, 7, protocol.UsageReceipt{
				RequestID: "r1", Model: "m", PromptTokens: 100, CompletionTokens: 200, TS: 1000,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Settle(u, "nB", 20, 14, protocol.UsageReceipt{
				RequestID: "r2", Model: "m", PromptTokens: 50, CompletionTokens: 60, TS: 2000,
			}); err != nil {
				t.Fatal(err)
			}

			// --- AdminFinancials (pre-payout) ---
			f, err := db.AdminFinancials(now)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(f.ConsumerSpend, 30) || !approx(f.OperatorEarned, 21) || !approx(f.PlatformFee, 9) {
				t.Errorf("money split = spend %v / earned %v / fee %v, want 30/21/9", f.ConsumerSpend, f.OperatorEarned, f.PlatformFee)
			}
			if !approx(f.Payable, 21) || !approx(f.Paid, 0) || !approx(f.Held, 0) {
				t.Errorf("lot split = payable %v / paid %v / held %v, want 21/0/0", f.Payable, f.Paid, f.Held)
			}
			if !approx(f.TopupVolume, 100) || !approx(f.WalletBalance, 70) || f.WalletCount != 1 {
				t.Errorf("topup %v / wallet bal %v / count %d, want 100/70/1", f.TopupVolume, f.WalletBalance, f.WalletCount)
			}
			if f.OwnerCount != 2 || f.NodeBindings != 2 {
				t.Errorf("owners %d / bindings %d, want 2/2", f.OwnerCount, f.NodeBindings)
			}

			// --- AdminMarketTotals (all-time + windowed) ---
			mt, err := db.AdminMarketTotals(1500, 3000) // window catches only r2 (ts 2000)
			if err != nil {
				t.Fatal(err)
			}
			if mt.Requests != 2 || mt.TokensIn != 150 || mt.TokensOut != 260 {
				t.Errorf("all-time market = req %d in %d out %d, want 2/150/260", mt.Requests, mt.TokensIn, mt.TokensOut)
			}
			if mt.WindowRequests != 1 || mt.WindowTokensIn != 50 || mt.WindowTokensOut != 60 {
				t.Errorf("windowed market = req %d in %d out %d, want 1/50/60", mt.WindowRequests, mt.WindowTokensIn, mt.WindowTokensOut)
			}

			// --- AdminPayoutQueue (pre-payout): B (14) sorts before A (7) ---
			q, err := db.AdminPayoutQueue(now, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(q) != 2 || q[0].AccountID != B || !approx(q[0].Payable, 14) || q[1].AccountID != A || !approx(q[1].Payable, 7) {
				t.Fatalf("payout queue = %+v, want B(14) then A(7)", q)
			}

			// --- RequestPayout: B's payable (14) -> pending payout, lots flip to paid ---
			po, ok, reason, err := db.RequestPayout(B, now, 1)
			if err != nil || !ok {
				t.Fatalf("RequestPayout(B) ok=%v reason=%q err=%v", ok, reason, err)
			}
			if !approx(po.Amount, 14) || po.State != PayoutPending {
				t.Fatalf("payout = %+v, want amount 14 pending", po)
			}

			// AdminAllPayouts surfaces the one pending payout for B.
			all, err := db.AdminAllPayouts(0)
			if err != nil || len(all) != 1 || all[0].AccountID != B || all[0].State != PayoutPending {
				t.Fatalf("AdminAllPayouts = %+v (err %v), want one pending payout for B", all, err)
			}

			// Queue now: B has 0 payable + 14 pending; A still 7 payable.
			q2, _ := db.AdminPayoutQueue(now, 0)
			var bRow, aRow AdminPayoutQueueRow
			for _, r := range q2 {
				switch r.AccountID {
				case B:
					bRow = r
				case A:
					aRow = r
				}
			}
			if !approx(bRow.Payable, 0) || !approx(bRow.Pending, 14) || !approx(bRow.Paid, 14) {
				t.Errorf("B row post-payout = %+v, want payable 0 / pending 14 / paid 14", bRow)
			}
			if !approx(aRow.Payable, 7) {
				t.Errorf("A row payable = %v, want 7 (untouched)", aRow.Payable)
			}

			// --- AdminActivity: cross-account ledger stream, newest-first ---
			act, err := db.AdminActivity(0)
			if err != nil || len(act) == 0 {
				t.Fatalf("AdminActivity = %d rows (err %v), want > 0", len(act), err)
			}
			for i := 1; i < len(act); i++ {
				if act[i-1].ID < act[i].ID {
					t.Errorf("AdminActivity not newest-first at %d", i)
				}
			}
		})
	}
}

// TestAdminAbuseRollup covers the super-admin safety rollup on BOTH backends: strikes
// (total + distinct struck accounts), a banned owner carrying its strike count, the
// CSAM queue depth, report count, banned-node count, and the dispute count.
func TestAdminAbuseRollup(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			// Two accounts struck; only one banned.
			_, _ = db.OwnerStrike("acct-x", StrikeEmptyOutput, "{}", "kx1")
			_, _ = db.OwnerStrike("acct-x", StrikeRecountDiscrepancy, "{}", "kx2")
			_, _ = db.OwnerStrike("acct-y", StrikeEmptyOutput, "{}", "ky1")
			_ = db.BanOwner("acct-x", "abuse", "{}")

			// CSAM: one queued incident. A report. A banned node.
			if _, err := db.PreserveCSAM(CSAMIncident{Pseudonym: "p", IP: "1.1.1.1", Category: "S4", Content: []byte("c")}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AddReport(Report{Category: "abuse", NodeID: "n1", IP: "2.2.2.2"}); err != nil {
				t.Fatal(err)
			}
			_ = db.BanNode("n1", "threshold")

			// A dispute (chargeback) so the dispute count is non-zero.
			u := "u-cb"
			_, _ = db.AddCredits(u, 50)
			_ = db.BindNode("ncb", "acct-cb")
			_, _ = db.Settle(u, "ncb", 10, 7, protocol.UsageReceipt{RequestID: "rcb", Model: "m", TS: 1})
			if _, err := db.ChargebackLineage("disp-1", u, "rcb", 10, now); err != nil {
				t.Fatal(err)
			}

			a, err := db.AdminAbuse()
			if err != nil {
				t.Fatal(err)
			}
			if a.TotalStrikes != 3 || a.StruckAccounts != 2 {
				t.Errorf("strikes = total %d / accounts %d, want 3/2", a.TotalStrikes, a.StruckAccounts)
			}
			if len(a.BannedOwners) != 1 || a.BannedOwners[0].AccountID != "acct-x" || a.BannedOwners[0].Strikes != 2 {
				t.Errorf("banned owners = %+v, want one (acct-x, 2 strikes)", a.BannedOwners)
			}
			if a.CSAMQueued != 1 || a.CSAMTotal != 1 {
				t.Errorf("csam = queued %d / total %d, want 1/1", a.CSAMQueued, a.CSAMTotal)
			}
			if a.ReportCount != 1 || a.BannedNodes != 1 {
				t.Errorf("reports %d / banned nodes %d, want 1/1", a.ReportCount, a.BannedNodes)
			}
			if a.DisputeCount != 1 {
				t.Errorf("dispute count = %d, want 1", a.DisputeCount)
			}
		})
	}
}
