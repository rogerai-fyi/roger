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
