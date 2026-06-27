package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// finalizeOne runs one Hold+Finalize for `share` credits on `node` under `model`,
// through the Store interface (so it drives Mem and Postgres identically). The wallet
// is assumed already funded. cost == share keeps the math easy to reason about.
func finalizeOne(t *testing.T, db Store, user, node, reqID, model string, share float64) {
	t.Helper()
	if ok, err := db.Hold(user, share); err != nil || !ok {
		t.Fatalf("Hold(%g) ok=%v err=%v", share, ok, err)
	}
	if _, err := db.Finalize(user, node, share, share, share, protocol.UsageReceipt{
		RequestID: reqID, Model: model, TS: 1,
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

// TestPayoutLifecycleParity drives the full payout lifecycle on BOTH backends:
// finalize earnings -> EarningSplitOfNode -> RequestPayout -> PayoutsOf -> PayoutLots
// (with model lineage) -> SettlePayout (paid + transfer id), then a second payout that
// is FailPayout'd, returning its lots to payable so they can be requested again.
func TestPayoutLifecycleParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Add(time.Hour) // past the wall-clock lot stamp so hold=0 promotes
			_ = db.BindNode("nodeA", "acct1")
			_ = db.BindNode("nodeB", "acct1")
			if _, err := db.AddCredits("u", 1000); err != nil {
				t.Fatal(err)
			}
			finalizeOne(t, db, "u", "nodeA", "r1", "gpt-oss", 5)
			finalizeOne(t, db, "u", "nodeA", "r2", "gpt-oss", 3)
			finalizeOne(t, db, "u", "nodeB", "r3", "gemma", 4)

			// EarningSplitOfNode: nodeA's two lots are payable now (hold=0).
			split, err := db.EarningSplitOfNode("nodeA", now)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(split.Payable, 8) || !approx(split.Held, 0) {
				t.Errorf("nodeA split = payable %v / held %v, want 8/0", split.Payable, split.Held)
			}

			// RequestPayout sweeps acct1's 12 payable into one pending payout.
			pay, ok, reason, err := db.RequestPayout("acct1", now, 1)
			if err != nil || !ok {
				t.Fatalf("RequestPayout ok=%v reason=%q err=%v", ok, reason, err)
			}
			if !approx(pay.Amount, 12) || pay.State != PayoutPending {
				t.Fatalf("payout = %+v, want amount 12 pending", pay)
			}

			ps, _ := db.PayoutsOf("acct1", 0)
			if len(ps) != 1 || ps[0].ID != pay.ID {
				t.Fatalf("PayoutsOf = %+v, want the one payout", ps)
			}

			// PayoutLots resolves the funding lots + model lineage, owner-scoped.
			lots, found, err := db.PayoutLots("acct1", pay.ID)
			if err != nil || !found || len(lots) != 3 {
				t.Fatalf("PayoutLots found=%v lots=%d err=%v, want 3 lots", found, len(lots), err)
			}
			models := map[string]bool{}
			var total float64
			for _, l := range lots {
				models[l.Model] = true
				total += l.Gross
			}
			if !approx(total, 12) || !models["gpt-oss"] || !models["gemma"] {
				t.Errorf("lots total=%v models=%v, want 12 over gpt-oss+gemma", total, models)
			}
			if _, found2, _ := db.PayoutLots("acctOTHER", pay.ID); found2 {
				t.Error("cross-account PayoutLots must reject (found=false)")
			}

			// SettlePayout marks it PAID with a transfer id (idempotent).
			if err := db.SettlePayout(pay.ID, "tr_abc"); err != nil {
				t.Fatal(err)
			}
			if err := db.SettlePayout(pay.ID, "tr_abc"); err != nil {
				t.Fatalf("SettlePayout must be idempotent: %v", err)
			}
			ps, _ = db.PayoutsOf("acct1", 0)
			if ps[0].State != PayoutPaid || ps[0].StripeTransferID != "tr_abc" {
				t.Errorf("settled payout = %+v, want paid + transfer tr_abc", ps[0])
			}

			// FailPayout rolls a pending payout back: its lots return to PAYABLE.
			finalizeOne(t, db, "u", "nodeA", "r4", "gpt-oss", 6)
			pay2, ok, _, _ := db.RequestPayout("acct1", now, 1)
			if !ok || !approx(pay2.Amount, 6) {
				t.Fatalf("second payout = %+v ok=%v, want amount 6", pay2, ok)
			}
			if err := db.FailPayout(pay2.ID); err != nil {
				t.Fatal(err)
			}
			// The 6 is payable again -> a fresh request succeeds with the same amount.
			pay3, ok, _, _ := db.RequestPayout("acct1", now, 1)
			if !ok || !approx(pay3.Amount, 6) {
				t.Fatalf("post-fail re-request = %+v ok=%v, want amount 6 returned to payable", pay3, ok)
			}
		})
	}
}

// TestReleaseScheduleEarningRollupsParity covers the held-earning views on BOTH
// backends: with a 120-day hold the finalized lots stay HELD; EarningRollups groups
// them per model and per node (highest first), and ReleaseSchedule buckets the still-
// held lots by their release day.
func TestReleaseScheduleEarningRollupsParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("nodeA", "acct1")
			_ = db.BindNode("nodeB", "acct1")
			if _, err := db.AddCredits("u", 1000); err != nil {
				t.Fatal(err)
			}
			finalizeOne(t, db, "u", "nodeA", "r1", "gpt-oss", 5)
			finalizeOne(t, db, "u", "nodeA", "r2", "gpt-oss", 3)
			finalizeOne(t, db, "u", "nodeB", "r3", "gemma", 4)

			byModel, byNode, err := db.EarningRollups("acct1")
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

			// All three lots are held with a ~120-day-out release on the same calendar day.
			rel, err := db.ReleaseSchedule("acct1", now)
			if err != nil {
				t.Fatal(err)
			}
			var total float64
			var lots int
			for _, b := range rel {
				total += b.Amount
				lots += b.LotCount
			}
			if !approx(total, 12) || lots != 3 {
				t.Errorf("release ladder total=%v lots=%d, want 12 / 3", total, lots)
			}
		})
	}
}

// TestSeedPathParity covers the starter-seed accounting on BOTH backends: the cap
// gates how many distinct wallets get a seed (SeedOnce), the cap is shared with the
// auto-seed path (BalanceOf), an already-seeded wallet is never re-seeded, PeekBalance
// reads without seeding, and SeedStatus reports the counter.
func TestSeedPathParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			db.SetSeedLimit(2)
			const seed = 50.0

			if b, seeded, _ := db.SeedOnce("a", seed); !seeded || !approx(b, seed) {
				t.Fatalf("a under limit -> bal=%g seeded=%v, want %g/true", b, seeded, seed)
			}
			// BalanceOf auto-seeds the 2nd distinct wallet (shares the same cap).
			if b, _ := db.BalanceOf("b", seed); !approx(b, seed) {
				t.Fatalf("b under limit via BalanceOf -> %g, want %g", b, seed)
			}
			// Cap now exhausted: the 3rd wallet gets nothing on either path.
			if b, seeded, _ := db.SeedOnce("c", seed); seeded || !approx(b, 0) {
				t.Fatalf("c at limit -> bal=%g seeded=%v, want 0/false", b, seeded)
			}
			if b, _ := db.BalanceOf("c", seed); !approx(b, 0) {
				t.Fatalf("capped c via BalanceOf -> %g, want 0", b)
			}
			// Already-seeded wallet: never re-seeded.
			if b, seeded, _ := db.SeedOnce("a", seed); seeded || !approx(b, seed) {
				t.Fatalf("re-seed of a -> bal=%g seeded=%v, want %g/false", b, seeded, seed)
			}

			// PeekBalance reads without seeding (an unknown wallet is 0).
			if b, _ := db.PeekBalance("a"); !approx(b, seed) {
				t.Errorf("PeekBalance(a) = %g, want %g", b, seed)
			}
			if b, _ := db.PeekBalance("never"); !approx(b, 0) {
				t.Errorf("PeekBalance(unknown) = %g, want 0", b)
			}

			// SeedStatus: exactly the 2 granted, none remaining.
			seeded, limit, remaining, err := db.SeedStatus()
			if err != nil {
				t.Fatal(err)
			}
			if seeded != 2 || limit != 2 || remaining != 0 {
				t.Errorf("SeedStatus = seeded %d / limit %d / remaining %d, want 2/2/0", seeded, limit, remaining)
			}
		})
	}
}

// TestChargeLinkHealthParity covers the Stripe charge<->wallet linkage, the health
// probe, and the open-dispute count on BOTH backends.
func TestChargeLinkHealthParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.LinkCharge("cs_1", "pi_1", "ch_1", "u_gh_5", 42); err != nil {
				t.Fatalf("LinkCharge: %v", err)
			}
			// Both the payment-intent and the charge id resolve to the wallet+credits.
			for _, ref := range []string{"pi_1", "ch_1"} {
				w, c, ok, err := db.WalletByCharge(ref)
				if err != nil || !ok || w != "u_gh_5" || !approx(c, 42) {
					t.Errorf("WalletByCharge(%q) = %q,%v,%v,%v want u_gh_5,42,true,nil", ref, w, c, ok, err)
				}
			}
			if _, _, ok, _ := db.WalletByCharge("unknown"); ok {
				t.Error("unknown ref must resolve ok=false")
			}

			// A fresh account has no open disputes.
			if n, err := db.OpenDisputeCount("acct-none"); err != nil || n != 0 {
				t.Errorf("OpenDisputeCount = %d (err %v), want 0", n, err)
			}

			// Health probe succeeds against a live store.
			if err := db.Healthy(); err != nil {
				t.Errorf("Healthy = %v, want nil", err)
			}
		})
	}
}
