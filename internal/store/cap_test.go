package store

import (
	"os"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// capStores runs the monthly-cap parity suite against Mem always, and Postgres when
// ROGERAI_TEST_DATABASE_URL is set, so the cap setting + the month-to-date ledger sum
// behave identically on both backends.
func capStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMem()}
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		out["postgres"] = freshPostgres(t, dsn)
	}
	return out
}

// spendAt records a captured spend for `user` at unix time `ts` (via Settle, which
// appends the posted KindSpend ledger row MonthSpendOf sums). The wallet is funded
// first so the debit lands.
func spendAt(t *testing.T, db Store, user string, cost float64, ts int64) {
	t.Helper()
	if _, err := db.AddCredits(user, cost); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Settle(user, "node-x", cost, 0, protocol.UsageReceipt{
		RequestID: "r-" + time.Unix(ts, 0).UTC().Format("20060102-150405.000000000"),
		Model:     "m", TS: ts,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMonthlyCapStoreParity(t *testing.T) {
	for name, db := range capStores(t) {
		t.Run(name, func(t *testing.T) {
			u := "u_gh_42"

			// Default cap = unlimited (opt-in feature, env unset).
			if cap, _ := db.MonthlyCapOf(u); cap != 0 {
				t.Errorf("default cap = %v, want 0 (unlimited)", cap)
			}

			// Set / read / clear the cap.
			if err := db.SetMonthlyCap(u, 25); err != nil {
				t.Fatal(err)
			}
			if cap, _ := db.MonthlyCapOf(u); !approx(cap, 25) {
				t.Errorf("cap after set = %v, want 25", cap)
			}
			if err := db.SetMonthlyCap(u, 0); err != nil { // clear -> unlimited
				t.Fatal(err)
			}
			if cap, _ := db.MonthlyCapOf(u); cap != 0 {
				t.Errorf("cap after clear = %v, want 0", cap)
			}
			if err := db.SetMonthlyCap(u, 25); err != nil {
				t.Fatal(err)
			}

			// Month-to-date sums ONLY the current calendar month. Spend in THIS month
			// counts; spend last month (and earlier) does not - the boundary test.
			now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
			start, _ := monthRange(now)
			lastMonthEnd := start - 1                                  // 2026-05-31 23:59:59 UTC
			thisMonthStart := start                                    // exactly 2026-06-01 00:00:00
			midMonth := now.Unix()                                     // 2026-06-15
			spendAt(t, db, u, 3.00, lastMonthEnd)                      // previous month -> excluded
			spendAt(t, db, u, 4.00, thisMonthStart)                    // first instant of month -> included
			spendAt(t, db, u, 5.50, midMonth)                          // mid month -> included
			if mtd, _ := db.MonthSpendOf(u, now); !approx(mtd, 9.50) { // 4.00 + 5.50
				t.Errorf("month-to-date = %v, want 9.50 (last month's 3.00 excluded)", mtd)
			}

			// A different calendar month sees a clean slate (no carryover).
			nextMonth := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			if mtd, _ := db.MonthSpendOf(u, nextMonth); !approx(mtd, 0) {
				t.Errorf("next-month MTD = %v, want 0 (month rolls over)", mtd)
			}

			// A separate wallet is isolated.
			if mtd, _ := db.MonthSpendOf("u_gh_99", now); !approx(mtd, 0) {
				t.Errorf("other wallet MTD = %v, want 0", mtd)
			}
		})
	}
}

// TestDefaultMonthlyCapEnv covers the env-seeded starting cap for an un-set wallet
// and the explicit-unlimited override (a stored 0 is NOT re-defaulted).
func TestDefaultMonthlyCapEnv(t *testing.T) {
	t.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", "50")
	m := NewMem()
	if cap, _ := m.MonthlyCapOf("u_new"); !approx(cap, 50) {
		t.Errorf("un-set wallet cap = %v, want the env default 50", cap)
	}
	// An account that explicitly chose unlimited stores 0 and is NOT re-defaulted.
	if err := m.SetMonthlyCap("u_optout", 0); err != nil {
		t.Fatal(err)
	}
	if cap, _ := m.MonthlyCapOf("u_optout"); cap != 0 {
		t.Errorf("explicit-unlimited cap = %v, want 0 (not re-defaulted)", cap)
	}
}
