package store

import (
	"testing"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
)

// A Tower operator earns a share of revenue on traffic relayed through their Tower via an
// explicit-account earning lot - they are not the serving node's owner, so the node-resolved
// mint cannot reach them. The lot must flow through the SAME wallet lifecycle as a node lot
// (payable, payout) and be clawed back by a refund/chargeback of the same request. Run on both
// stores so the SQL and mem mints stay in lockstep.
func TestAddOperatorLotParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("st-1", "station-owner")
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// A paid request: alice pays 100, the station owner earns 70 (30% platform fee).
			if ok, err := db.Hold("alice", 100); err != nil || !ok {
				t.Fatalf("Hold: ok=%v err=%v", ok, err)
			}
			if _, err := db.Finalize("alice", "st-1", 100, 100, 70, protocol.UsageReceipt{
				RequestID: "req-1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix(),
			}); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			// The Tower operator earns 10% of the platform's 30 revenue = 3, on the SAME requestID.
			if err := db.AddOperatorLot("tw-1", "tower-operator", "req-1", 3, now); err != nil {
				t.Fatalf("AddOperatorLot: %v", err)
			}

			// The tower operator's lot is payable to THEIR account, separate from the station's.
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); !approx(s.Payable, 3) {
				t.Errorf("[%s] tower-operator payable = %v, want 3", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); !approx(s.Payable, 70) {
				t.Errorf("[%s] station-owner payable = %v, want 70", name, s.Payable)
			}

			// A full refund of req-1 claws BOTH lots (same requestID): station 70 + tower 3.
			if _, _, err := db.RefundLineage("rf-1", []string{"req-1"}, "alice", "req-1", 100, now); err != nil {
				t.Fatalf("RefundLineage: %v", err)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] tower-operator payable after refund = %v, want 0 (clawed)", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] station-owner payable after refund = %v, want 0 (clawed)", name, s.Payable)
			}
		})
	}
}

// A no-op guard: empty account or non-positive gross mints nothing on either store.
func TestAddOperatorLotNoOps(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if err := db.AddOperatorLot("tw", "", "r", 5, now); err != nil {
				t.Fatalf("empty account: %v", err)
			}
			if err := db.AddOperatorLot("tw", "op", "r", 0, now); err != nil {
				t.Fatalf("zero gross: %v", err)
			}
			if s, _ := db.EarningSplitOf("op", now.Add(time.Hour)); s.Payable != 0 || s.Held != 0 {
				t.Errorf("[%s] nothing should have minted: %+v", name, s)
			}
		})
	}
}

// SettleEdge captures a billed edge attempt: the consumer is charged, and BOTH the Station owner
// and the Tower operator are credited from the SAME real-paid fraction, as clawable lots. Run on
// both stores.
func TestSettleEdgeParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// Hold the ceiling (100), then settle at actual cost 100. Station share 70 (30% fee),
			// Tower share = 10% of the platform's 30 = 3.
			if ok, err := db.HoldFor("alice", "req-1", 100); err != nil || !ok {
				t.Fatalf("HoldFor: ok=%v err=%v", ok, err)
			}
			bal, err := db.SettleEdge("alice", "st-1", "station-owner", "tw-1", "tower-operator",
				100, 70, 3, protocol.UsageReceipt{RequestID: "req-1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()})
			if err != nil {
				t.Fatalf("SettleEdge: %v", err)
			}
			if !approx(bal, 900) {
				t.Errorf("[%s] balance = %v, want 900 (charged 100)", name, bal)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); !approx(s.Payable, 70) {
				t.Errorf("[%s] station payable = %v, want 70", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); !approx(s.Payable, 3) {
				t.Errorf("[%s] tower payable = %v, want 3", name, s.Payable)
			}
			// Refund of req-1 claws both.
			if _, _, err := db.RefundLineage("rf-1", []string{"req-1"}, "alice", "req-1", 100, now); err != nil {
				t.Fatalf("RefundLineage: %v", err)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] station payable after refund = %v, want 0", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] tower payable after refund = %v, want 0", name, s.Payable)
			}
		})
	}
}

// Seed-funded (free) edge traffic charges the consumer's seed but mints NO earning for either the
// Station or the Tower - the "only real money pays" rule, applied to both shares from one seed
// consumption.
func TestSettleEdgeSeedFundedMintsNothing(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			// Seed the wallet with 100 free credits (no real money in).
			if _, _, err := db.SeedOnce("bob", 100); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.HoldFor("bob", "req-2", 100); err != nil || !ok {
				t.Fatalf("HoldFor: ok=%v err=%v", ok, err)
			}
			if _, err := db.SettleEdge("bob", "st-1", "station-owner", "tw-1", "tower-operator",
				100, 70, 3, protocol.UsageReceipt{RequestID: "req-2", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}); err != nil {
				t.Fatalf("SettleEdge: %v", err)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); s.Payable != 0 || s.Held != 0 {
				t.Errorf("[%s] station earned on seed money: %+v", name, s)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); s.Payable != 0 || s.Held != 0 {
				t.Errorf("[%s] tower earned on seed money: %+v", name, s)
			}
		})
	}
}

// SettleEdge charges ONLY against an existing reservation. With no prior hold - the attempt was
// authorized while edge billing was off, or its hold was already swept - it is a strict no-op:
// no debit, no refund, no lot. This closes the config-change hole where a wallet with no reserved
// hold could otherwise be credited held-cost (free money) or debited for work it never reserved.
func TestSettleEdgeWithoutAHoldIsANoOp(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// No HoldFor for req-x. SettleEdge must do nothing.
			if _, err := db.SettleEdge("alice", "st-1", "station-owner", "tw-1", "tower-operator",
				50, 35, 3, protocol.UsageReceipt{RequestID: "req-x", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}); err != nil {
				t.Fatalf("SettleEdge: %v", err)
			}
			if bal, _ := db.PeekBalance("alice"); !approx(bal, 1000) {
				t.Errorf("[%s] balance = %v, want 1000 (no debit/credit without a hold)", name, bal)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); s.Payable != 0 {
				t.Errorf("[%s] station minted without a hold: %+v", name, s)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now.Add(time.Hour)); s.Payable != 0 {
				t.Errorf("[%s] tower minted without a hold: %+v", name, s)
			}
		})
	}
}

// Review finding 3: an edge request has TWO lots (Station + Tower) but ONE consumer cost. A
// wallet-recency claw (voluntary refund / no request id) must count that cost ONCE and claw BOTH
// lots - not deduct the cost per-lot (draining the budget at 2x, stopping early, and leaving the
// Tower's share unrecovered while the platform over-absorbs the loss).
func TestWalletRecencyClawGroupsEdgeLotsPerRequest(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// OLDER normal request n1: cost 100, node owner earns 70 (one lot).
			_ = db.BindNode("n1node", "node-owner")
			if ok, _ := db.Hold("alice", 100); !ok {
				t.Fatal("hold n1")
			}
			if _, err := db.Finalize("alice", "n1node", 100, 100, 70, protocol.UsageReceipt{RequestID: "n1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: 1000}); err != nil {
				t.Fatal(err)
			}
			// NEWER edge request e1: cost 100, Station 70 + Tower 3 (two lots, one requestID).
			if ok, _ := db.HoldFor("alice", "e1", 100); !ok {
				t.Fatal("hold e1")
			}
			if _, err := db.SettleEdge("alice", "st", "station-owner", "tw", "tower-op", 100, 70, 3, protocol.UsageReceipt{RequestID: "e1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: 2000}); err != nil {
				t.Fatal(err)
			}

			// Voluntary claw of 100 (no request id, newest first) covers exactly e1's cost.
			clawed, err := db.Chargeback("dp1", "alice", "", 100, now)
			if err != nil {
				t.Fatalf("Chargeback: %v", err)
			}
			// BOTH e1 lots recovered (70+3=73); n1 untouched; platform eats the 27 fee (not 30).
			if !approx(clawed, 73) {
				t.Errorf("[%s] clawed = %v, want 73 (station 70 + tower 3 of the ONE refunded request)", name, clawed)
			}
			if s, _ := db.EarningSplitOf("station-owner", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] station payable = %v, want 0 (clawed)", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("tower-op", now.Add(time.Hour)); !approx(s.Payable, 0) {
				t.Errorf("[%s] tower payable = %v, want 0 (clawed together with the station lot)", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("node-owner", now.Add(time.Hour)); !approx(s.Payable, 70) {
				t.Errorf("[%s] node-owner payable = %v, want 70 (older unrelated request untouched)", name, s.Payable)
			}
			led, _ := db.LedgerOf("platform", []string{KindPlatformLoss}, 10)
			if len(led) != 1 || !approx(led[0].Amount, -27) {
				t.Errorf("[%s] platform_loss = %+v, want one -27 row (fee only, not 30)", name, led)
			}
		})
	}
}

// SettleEdge caps the capture at the recorded hold - and the SHARES must shrink with it, or
// the operators would be paid a percentage of money the consumer never actually paid (minted
// from the platform's pocket). Runs against Mem AND Postgres.
func TestSettleEdgeScalesSharesWithACappedCost(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			uid := name + "-" + time.Now().UTC().Format("150405.000000000")
			user, station, stAcct, twNode, twAcct := "u-"+uid, "n-"+uid, "sa-"+uid, "tower:tw-"+uid, "ta-"+uid
			if _, err := db.AddCredits(user, 100); err != nil {
				t.Fatal(err)
			}
			held, err := db.HoldFor(user, "req-"+uid, 10) // the reservation: 10
			if err != nil || !held {
				t.Fatalf("hold: %v %v", held, err)
			}

			// A (defensively impossible) cost of 20 with shares computed from it: 14 / 2.
			rec := protocol.UsageReceipt{RequestID: "req-" + uid, Model: "m", TS: time.Now().Unix()}
			if _, err := db.SettleEdge(user, station, stAcct, twNode, twAcct, 20, 14, 2, rec); err != nil {
				t.Fatal(err)
			}

			later := time.Now().Add(time.Hour)
			sSt, _ := db.EarningSplitOf(stAcct, later)
			sTw, _ := db.EarningSplitOf(twAcct, later)
			if !approx(sSt.Payable, 7) {
				t.Fatalf("%s: station share = %v, want 7 (scaled to the captured 10)", name, sSt.Payable)
			}
			if !approx(sTw.Payable, 1) {
				t.Fatalf("%s: tower share = %v, want 1 (scaled to the captured 10)", name, sTw.Payable)
			}
			bal, _ := db.PeekBalance(user)
			if !approx(bal, 90) {
				t.Fatalf("%s: consumer balance = %v, want 90 (paid only the reservation)", name, bal)
			}
		})
	}
}
