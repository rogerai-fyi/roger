package store

import (
	"math"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func eq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func mkRec(id string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: id, Model: "m", TS: 1}
}

// TestHoldNeverGoesNegative: a Hold larger than the balance is refused (no debit), so
// a wallet can never be driven negative by the pre-auth - the core overdraft guard.
func TestHoldNeverGoesNegative(t *testing.T) {
	m := NewMem()
	m.AddCredits("u", 10)
	if ok, _ := m.Hold("u", 100); ok {
		t.Fatal("Hold above balance should be refused")
	}
	if bal, _ := m.BalanceOf("u", 0); !eq(bal, 10) {
		t.Fatalf("balance after refused hold = %v, want 10 (unchanged)", bal)
	}
	if ok, _ := m.Hold("u", 10); !ok { // exactly the balance is allowed
		t.Fatal("Hold of exactly the balance should be allowed")
	}
	if bal, _ := m.BalanceOf("u", 0); !eq(bal, 0) {
		t.Fatalf("balance after exact hold = %v, want 0", bal)
	}
}

// TestHoldFinalizeConservation: Hold(h) then Finalize(h, cost, ownerShare) leaves the
// wallet down by EXACTLY cost (the unused h-cost is refunded), the provider earns
// exactly ownerShare (real funds), and no credits leak. This is the money invariant a
// relay must satisfy end to end.
func TestHoldFinalizeConservation(t *testing.T) {
	m := NewMem()
	m.AddCredits("u", 10)
	const hold, cost, ownerShare = 5.0, 2.0, 1.4 // cost*(1-0.30)
	if ok, _ := m.Hold("u", hold); !ok {
		t.Fatal("hold failed")
	}
	if bal, _ := m.BalanceOf("u", 0); !eq(bal, 10-hold) {
		t.Fatalf("balance after hold = %v, want %v", bal, 10-hold)
	}
	bal, err := m.Finalize("u", "node1", hold, cost, ownerShare, mkRec("r1"))
	if err != nil {
		t.Fatal(err)
	}
	if !eq(bal, 10-cost) { // 10 - 2 = 8; the 3 unused hold was refunded
		t.Fatalf("balance after finalize = %v, want %v (down by exactly cost)", bal, 10-cost)
	}
	earn, _ := m.EarningsOf("node1")
	if !eq(earn, ownerShare) {
		t.Fatalf("provider earnings = %v, want %v (cost*(1-fee))", earn, ownerShare)
	}
	// Conservation: consumer paid `cost`; provider got `ownerShare`; platform kept the rest.
	if platform := cost - earn; !eq(platform, cost-ownerShare) {
		t.Fatalf("platform take = %v, want %v", platform, cost-ownerShare)
	}
}

// TestSettleDirectDebit: Settle (no prior hold) debits the wallet by cost and mints the
// provider's ownerShare, for real (non-seed) funds.
func TestSettleDirectDebit(t *testing.T) {
	m := NewMem()
	m.AddCredits("u", 10)
	bal, err := m.Settle("u", "node1", 2.0, 1.4, mkRec("r1"))
	if err != nil {
		t.Fatal(err)
	}
	if !eq(bal, 8) {
		t.Fatalf("balance after settle = %v, want 8", bal)
	}
	if earn, _ := m.EarningsOf("node1"); !eq(earn, 1.4) {
		t.Fatalf("earnings = %v, want 1.4", earn)
	}
}

// TestSeedSpendMintsNoEarning is the P0 money rule: spend covered by FREE seed credits
// records the consumer's metering but mints ZERO provider earning (you can't pay out
// money that was never paid in). Real-funded spend mints the full ownerShare.
func TestSeedSpendMintsNoEarning(t *testing.T) {
	m := NewMem()
	// Seeded wallet: BalanceOf with seed grants 100 free seed credits, no real money in.
	if bal, _ := m.BalanceOf("seeded", 100); !eq(bal, 100) {
		t.Fatalf("seed balance = %v, want 100", bal)
	}
	if _, err := m.Settle("seeded", "nodeSeed", 2.0, 1.4, mkRec("s1")); err != nil {
		t.Fatal(err)
	}
	if earn, _ := m.EarningsOf("nodeSeed"); !eq(earn, 0) {
		t.Fatalf("seed-funded spend minted %v earnings, want 0 (seed credits never pay out)", earn)
	}

	// Real-funded wallet: the same spend mints the full ownerShare.
	m.AddCredits("real", 100)
	if _, err := m.Settle("real", "nodeReal", 2.0, 1.4, mkRec("r1")); err != nil {
		t.Fatal(err)
	}
	if earn, _ := m.EarningsOf("nodeReal"); !eq(earn, 1.4) {
		t.Fatalf("real-funded spend minted %v earnings, want 1.4", earn)
	}
}

// TestReleaseHoldRefundsInFull: an aborted request (ReleaseHold) returns the entire
// pre-auth - the consumer is made whole, nothing is charged.
func TestReleaseHoldRefundsInFull(t *testing.T) {
	m := NewMem()
	m.AddCredits("u", 10)
	m.Hold("u", 4)
	if bal, _ := m.ReleaseHold("u", 4); !eq(bal, 10) {
		t.Fatalf("balance after release = %v, want 10 (full refund)", bal)
	}
}
