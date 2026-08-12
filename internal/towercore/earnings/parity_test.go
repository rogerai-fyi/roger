package earnings

// parity_test.go runs one scenario through BOTH funding ledgers and requires the same answer.
// The idempotency guarantee - an attempt earns once, a payout counts once - is asserted
// against each store, because a ledger that double-counted on one deployment and not the other
// would pay operators differently depending on where their attempt happened to settle.
//
// Without ROGERAI_TEST_DATABASE_URL the durable half skips and the memory half still runs.

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

var privateOnce sync.Once

func privateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_earnings"
	privateOnce.Do(func() {
		admin, aerr := sql.Open("pgx", dsn)
		if aerr != nil {
			t.Fatalf("private db: open admin: %v", aerr)
		}
		defer admin.Close()
		if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
			!strings.Contains(cerr.Error(), "already exists") {
			t.Fatalf("private db: create %s: %v", name, cerr)
		}
	})
	u.Path = "/" + name
	return u.String()
}

func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_earnings, rogerai.tower_payouts`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func accrual(owner, attempt string, micros int64, at time.Time) Accrual {
	return Accrual{
		TowerID: "tw-1", Owner: owner, AttemptID: attempt, Model: "m",
		UsageIn: 10, UsageOut: 20, Micros: micros, Corroborated: true, At: at,
	}
}

// The load-bearing property: an attempt accrues once, however many times Accrue is called.
func TestParityAnAttemptEarnsExactlyOnce(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 5; i++ {
				require.NoError(t, s.Accrue(accrual("op-1", "att-1", 500, base)))
			}
			owed, err := s.OwedTo("op-1", base.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(500), owed.Accrued, "five writes, one attempt, one accrual")
			require.Equal(t, 1, owed.Attempts)
			require.Equal(t, int64(500), owed.Owed())
		})
	}
}

func TestParityOwedNetsPayoutsAndFloorsAtZero(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Accrue(accrual("op-1", "att-1", 800, base)))
			require.NoError(t, s.Accrue(accrual("op-1", "att-2", 200, base)))
			// A retried disbursement must not reduce the debt twice.
			for i := 0; i < 3; i++ {
				require.NoError(t, s.RecordPayout("op-1", "pay-1", 600, base))
			}
			owed, err := s.OwedTo("op-1", base.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(1000), owed.Accrued)
			require.Equal(t, int64(600), owed.Paid)
			require.Equal(t, int64(400), owed.Owed())

			// Overpayment (a bug elsewhere) surfaces as zero owed, never a negative debt.
			require.NoError(t, s.RecordPayout("op-1", "pay-2", 5000, base))
			owed, err = s.OwedTo("op-1", base.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(0), owed.Owed())
		})
	}
}

func TestParityOwedIsScopedToOwnerAndWindow(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Accrue(accrual("op-1", "old", 100, base.Add(-2*time.Hour))))
			require.NoError(t, s.Accrue(accrual("op-1", "new", 300, base)))
			require.NoError(t, s.Accrue(accrual("op-2", "other", 999, base)))

			owed, err := s.OwedTo("op-1", base.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(300), owed.Accrued, "the window excludes the old accrual")
			require.Equal(t, 1, owed.Attempts)
		})
	}
}

// Reap drops aged accruals AND aged payouts - a payout row past the window is as stale as the
// accrual it settled, and leaving it behind would slowly leak the payout table.
func TestParityReapDropsOldAccrualsAndPayouts(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Accrue(accrual("op-1", "old", 100, base.Add(-48*time.Hour))))
			require.NoError(t, s.Accrue(accrual("op-1", "keep", 300, base)))
			require.NoError(t, s.RecordPayout("op-1", "old-pay", 50, base.Add(-48*time.Hour)))
			require.NoError(t, s.RecordPayout("op-1", "keep-pay", 20, base))

			n, err := s.Reap(base.Add(-24 * time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(1), n, "one accrual reaped")

			owed, err := s.OwedTo("op-1", base.Add(-72*time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(300), owed.Accrued, "the aged accrual is gone")
			require.Equal(t, int64(20), owed.Paid, "the aged payout is gone too")
		})
	}
}

func TestAccrualIsValidatedBeforeItIsStored(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		for mname, mutate := range map[string]func(*Accrual){
			"no tower":   func(a *Accrual) { a.TowerID = "" },
			"no owner":   func(a *Accrual) { a.Owner = "" },
			"no attempt": func(a *Accrual) { a.AttemptID = "" },
			"neg micros": func(a *Accrual) { a.Micros = -1 },
			"neg usage":  func(a *Accrual) { a.UsageIn = -1 },
			"no time":    func(a *Accrual) { a.At = time.Time{} },
		} {
			t.Run(name+"/"+mname, func(t *testing.T) {
				a := accrual("op-1", "att-1", 100, base)
				mutate(&a)
				require.Error(t, s.Accrue(a))
			})
		}
	}
}

func TestAPayoutIsValidated(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.RecordPayout("", "pay-1", 100, base))
			require.Error(t, s.RecordPayout("op-1", "", 100, base))
			require.Error(t, s.RecordPayout("op-1", "pay-1", -1, base))
			require.NoError(t, s.RecordPayout("op-1", "pay-1", 100, base))
		})
	}
}

func TestNewPGStoreNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
}
