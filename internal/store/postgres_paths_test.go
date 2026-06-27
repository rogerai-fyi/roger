package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestEmptyRequestIDSettleParity covers the legacy no-idempotency-key settle path on BOTH
// backends: a receipt with an empty request id carries no claim, so the receipt is written
// fresh (Postgres fillEarnShare's empty-id INSERT arm) and the operator still earns. Two
// such settles are NOT deduped (no key), so they stack.
func TestEmptyRequestIDSettleParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Add(time.Hour)
			_ = db.BindNode("n-empty", "acct-empty")
			if _, err := db.AddCredits("u-empty", 100); err != nil {
				t.Fatal(err)
			}
			// Empty request id: no idempotency claim -> a fresh receipt + lot each call.
			rec := protocol.UsageReceipt{RequestID: "", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: 1000}
			if _, err := db.Settle("u-empty", "n-empty", 10, 7, rec); err != nil {
				t.Fatal(err)
			}
			if bal, _ := db.PeekBalance("u-empty"); !approx(bal, 90) {
				t.Errorf("[%s] balance = %v, want 90 (debited once)", name, bal)
			}
			if s, _ := db.EarningSplitOf("acct-empty", now); !approx(s.Payable, 7) {
				t.Errorf("[%s] payable = %v, want 7 (operator earned on the empty-req settle)", name, s.Payable)
			}
			// A second empty-request settle is NOT deduped -> balance drops again, earnings stack.
			if _, err := db.Settle("u-empty", "n-empty", 10, 7, rec); err != nil {
				t.Fatal(err)
			}
			if bal, _ := db.PeekBalance("u-empty"); !approx(bal, 80) {
				t.Errorf("[%s] balance after 2nd = %v, want 80 (no idem key -> not deduped)", name, bal)
			}
			if s, _ := db.EarningSplitOf("acct-empty", now); !approx(s.Payable, 14) {
				t.Errorf("[%s] payable after 2nd = %v, want 14 (stacked)", name, s.Payable)
			}
		})
	}
}

// TestSeedFundedSpendNoEarnParity locks P0-1 on BOTH backends through the real settle path:
// spend paid entirely from FREE seed credits debits the consumer but mints NO operator
// earning (realEarnShareTx draws the seed down and returns a zero owner share, so addLot is
// a no-op). The consumer's balance still drops by the full cost.
func TestSeedFundedSpendNoEarnParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Add(time.Hour)
			db.SetSeedLimit(10)
			_ = db.BindNode("n-seed", "acct-seed")
			// Wallet funded ONLY by free seed credits.
			if b, seeded, err := db.SeedOnce("u-seed", 50); err != nil || !seeded || !approx(b, 50) {
				t.Fatalf("[%s] SeedOnce = %v/%v/%v, want 50/true/nil", name, b, seeded, err)
			}
			// Spend $10 from seed: the operator earns nothing (seed-funded), consumer pays.
			if _, err := db.Settle("u-seed", "n-seed", 10, 7, protocol.UsageReceipt{
				RequestID: "r-seed", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: 1000,
			}); err != nil {
				t.Fatal(err)
			}
			if bal, _ := db.PeekBalance("u-seed"); !approx(bal, 40) {
				t.Errorf("[%s] balance = %v, want 40 (consumer paid from seed)", name, bal)
			}
			if s, _ := db.EarningSplitOf("acct-seed", now); !approx(s.Payable, 0) || !approx(s.Held, 0) {
				t.Errorf("[%s] operator split = payable %v / held %v, want 0/0 (seed-funded earns nothing)", name, s.Payable, s.Held)
			}
			if e, _ := db.EarningsOf("n-seed"); !approx(e, 0) {
				t.Errorf("[%s] node earnings = %v, want 0", name, e)
			}
		})
	}
}
