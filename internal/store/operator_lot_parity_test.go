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
			if s, _ := db.EarningSplitOf("tower-operator", now); !approx(s.Payable, 3) {
				t.Errorf("[%s] tower-operator payable = %v, want 3", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("station-owner", now); !approx(s.Payable, 70) {
				t.Errorf("[%s] station-owner payable = %v, want 70", name, s.Payable)
			}

			// A full refund of req-1 claws BOTH lots (same requestID): station 70 + tower 3.
			if _, _, err := db.RefundLineage("rf-1", []string{"req-1"}, "alice", "req-1", 100, now); err != nil {
				t.Fatalf("RefundLineage: %v", err)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now); !approx(s.Payable, 0) {
				t.Errorf("[%s] tower-operator payable after refund = %v, want 0 (clawed)", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("station-owner", now); !approx(s.Payable, 0) {
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
			if s, _ := db.EarningSplitOf("op", now); s.Payable != 0 || s.Held != 0 {
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
			if s, _ := db.EarningSplitOf("station-owner", now); !approx(s.Payable, 70) {
				t.Errorf("[%s] station payable = %v, want 70", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now); !approx(s.Payable, 3) {
				t.Errorf("[%s] tower payable = %v, want 3", name, s.Payable)
			}
			// Refund of req-1 claws both.
			if _, _, err := db.RefundLineage("rf-1", []string{"req-1"}, "alice", "req-1", 100, now); err != nil {
				t.Fatalf("RefundLineage: %v", err)
			}
			if s, _ := db.EarningSplitOf("station-owner", now); !approx(s.Payable, 0) {
				t.Errorf("[%s] station payable after refund = %v, want 0", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now); !approx(s.Payable, 0) {
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
			if s, _ := db.EarningSplitOf("station-owner", now); s.Payable != 0 || s.Held != 0 {
				t.Errorf("[%s] station earned on seed money: %+v", name, s)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now); s.Payable != 0 || s.Held != 0 {
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
			if s, _ := db.EarningSplitOf("station-owner", now); s.Payable != 0 {
				t.Errorf("[%s] station minted without a hold: %+v", name, s)
			}
			if s, _ := db.EarningSplitOf("tower-operator", now); s.Payable != 0 {
				t.Errorf("[%s] tower minted without a hold: %+v", name, s)
			}
		})
	}
}
