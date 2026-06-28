package store

import (
	"testing"
	"time"
)

// TestSortProviderTieBreaks exercises every comparator branch of sortProvider: primary
// EarningsUSD desc, then Model asc on an earnings tie, then NodeID asc on a model tie.
func TestSortProviderTieBreaks(t *testing.T) {
	rows := []ProviderModelMetric{
		{Model: "b", NodeID: "n2", EarningsUSD: 5}, // same earnings+model as next; NodeID tie-break
		{Model: "b", NodeID: "n1", EarningsUSD: 5},
		{Model: "a", NodeID: "n9", EarningsUSD: 5}, // same earnings; model tie-break puts "a" first
		{Model: "z", NodeID: "n0", EarningsUSD: 9}, // highest earnings -> first
	}
	sortProvider(rows)
	got := make([][2]string, len(rows))
	for i, r := range rows {
		got[i] = [2]string{r.Model, r.NodeID}
	}
	want := [][2]string{{"z", "n0"}, {"a", "n9"}, {"b", "n1"}, {"b", "n2"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v (full order %v)", i, got[i], want[i], got)
		}
	}
}

// TestSortUsageTieBreaks exercises sortUsage: SpendUSD desc, then Model asc on a tie.
func TestSortUsageTieBreaks(t *testing.T) {
	rows := []UsageModelMetric{
		{Model: "m", SpendUSD: 2},
		{Model: "a", SpendUSD: 2}, // same spend -> "a" sorts before "m"
		{Model: "z", SpendUSD: 7}, // highest spend -> first
	}
	sortUsage(rows)
	want := []string{"z", "a", "m"}
	for i, w := range want {
		if rows[i].Model != w {
			t.Errorf("row %d model = %q, want %q (order %+v)", i, rows[i].Model, w, rows)
		}
	}
}

// TestSeedLotsForTest covers the Mem.SeedLotsForTest seam: it wholesale-replaces the lot
// slice, advances the auto-increment lot id past the highest seeded id (so a later
// Finalize never reuses a seeded id), and the seeded lots drive EarningSplitOf - one lot
// already payable, one still held with a future release.
func TestSeedLotsForTest(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	m := NewMem()
	now := time.Now()
	m.SeedLotsForTest([]EarningLot{
		{ID: 7, Node: "n", AccountID: "acct", RequestID: "rp", Gross: 10, State: LotPayable, ReleaseAt: now.Add(-time.Hour).Unix()},
		{ID: 42, Node: "n", AccountID: "acct", RequestID: "rh", Gross: 4, State: LotHeld, ReleaseAt: now.Add(48 * time.Hour).Unix()},
	})

	s, err := m.EarningSplitOf("acct", now)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(s.Payable, 10) || !approx(s.Held, 4) {
		t.Fatalf("split = payable %v / held %v, want 10 / 4", s.Payable, s.Held)
	}

	// A fresh Finalize must mint a lot id strictly greater than the highest seeded id (42),
	// proving SeedLotsForTest advanced the internal counter.
	_ = m.BindNode("n2", "acct2")
	fundReal(m, "u", 100)
	if ok, _ := m.Hold("u", 5); !ok {
		t.Fatal("hold")
	}
	if _, err := m.Finalize("u", "n2", 5, 5, 5, rec("rnew")); err != nil {
		t.Fatal(err)
	}
	maxID := int64(0)
	for _, l := range m.lots {
		if l.ID > maxID {
			maxID = l.ID
		}
	}
	if maxID <= 42 {
		t.Errorf("max lot id after Finalize = %d, want > 42 (counter advanced past the seeded lots)", maxID)
	}
}
