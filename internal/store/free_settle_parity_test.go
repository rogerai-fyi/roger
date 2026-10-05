package store

// free_settle_parity_test.go - the store primitive behind
// features/money/free_settle_wallet_row.feature. A $0 Settle for a wallet that has never had
// a row (signed self-use, a free grant, the $0 lineage receipt of a voided attempt) must
// record its receipt and create a ZERO-BALANCE wallet row, never a seed (founder ruling
// 2026-10-01). Driven through the Store interface on BOTH backends (parityStores), with real
// Postgres when ROGERAI_TEST_DATABASE_URL is set - no mocks.

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func zeroRec(id string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: id, NodeID: "n1", Model: "m", PromptTokens: 10, CompletionTokens: 5, TS: 1}
}

// seedRowsOf counts a wallet's "seed:<wallet>" ledger rows.
func seedRowsOf(t *testing.T, db Store, wallet string) int {
	t.Helper()
	rows, err := db.LedgerOf(wallet, []string{KindAdjustment}, 1000)
	require.NoError(t, err)
	n := 0
	for _, r := range rows {
		if r.IdemKey == "seed:"+wallet {
			n++
		}
	}
	return n
}

func seededCount(t *testing.T, db Store) int {
	t.Helper()
	seeded, _, _, err := db.SeedStatus()
	require.NoError(t, err)
	return seeded
}

// TestZeroSettleNewWalletParity: the settle succeeds, the receipt is recorded, the balance is
// exactly 0, the ledger derives to 0, and nothing about the seed moved.
func TestZeroSettleNewWalletParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			bal, err := db.Settle("w-new", "n1", 0, 0, zeroRec("r1"))
			require.NoError(t, err, "a $0 settle for a wallet with no row must succeed")
			require.Zero(t, bal)

			es, err := db.RecentByUser("w-new", 10)
			require.NoError(t, err)
			require.Len(t, es, 1, "the $0 receipt is recorded")
			require.Equal(t, "r1", es[0].RequestID)
			require.Zero(t, es[0].Cost)
			require.Zero(t, es[0].OwnerShare)

			peek, err := db.PeekBalance("w-new")
			require.NoError(t, err)
			require.Zero(t, peek)
			derived, err := db.DeriveBalance("w-new")
			require.NoError(t, err)
			require.Zero(t, derived, "the ledger derives to the stored balance")

			require.Zero(t, seedRowsOf(t, db, "w-new"), "a $0 settle never seeds")
			require.Zero(t, seededCount(t, db), "the seeded-wallet counter is untouched")

			earned, err := db.EarningsOf("n1")
			require.NoError(t, err)
			require.Zero(t, earned, "the operator earns nothing on a $0 settle")
			spend, err := db.SpendOf("w-new")
			require.NoError(t, err)
			require.Zero(t, spend)
		})
	}
}

// TestZeroSettleRedeliveryIsIdempotentParity: a redelivered $0 settle for the new wallet is a
// clean no-op - one receipt, one spend row, balance unchanged - and a second request adds one.
func TestZeroSettleRedeliveryIsIdempotentParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				bal, err := db.Settle("w-dup", "n1", 0, 0, zeroRec("r1"))
				require.NoError(t, err, "delivery %d", i+1)
				require.Zero(t, bal)
			}
			es, err := db.RecentByUser("w-dup", 10)
			require.NoError(t, err)
			require.Len(t, es, 1, "three deliveries of one request are one receipt")
			rows, err := db.LedgerOf("w-dup", []string{KindSpend}, 100)
			require.NoError(t, err)
			require.Len(t, rows, 1, "one spend row for the one request")

			_, err = db.Settle("w-dup", "n1", 0, 0, zeroRec("r2"))
			require.NoError(t, err)
			es, err = db.RecentByUser("w-dup", 10)
			require.NoError(t, err)
			require.Len(t, es, 2)
			peek, err := db.PeekBalance("w-dup")
			require.NoError(t, err)
			require.Zero(t, peek)
		})
	}
}

// TestZeroSettleLeavesTheSeedForFirstUseParity: the row a $0 settle creates neither consumes
// nor blocks the wallet's one-time starter seed - the first seeding read grants it, once.
func TestZeroSettleLeavesTheSeedForFirstUseParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			_, err := db.Settle("w-seed", "n1", 0, 0, zeroRec("r1"))
			require.NoError(t, err)
			require.Zero(t, seedRowsOf(t, db, "w-seed"))

			bal, err := db.BalanceOf("w-seed", 25)
			require.NoError(t, err)
			require.Equal(t, 25.0, bal, "the first seeding read still grants the seed")
			require.Equal(t, 1, seedRowsOf(t, db, "w-seed"))
			require.Equal(t, 1, seededCount(t, db))

			bal, err = db.BalanceOf("w-seed", 25)
			require.NoError(t, err)
			require.Equal(t, 25.0, bal, "and only once")
			bal, seeded, err := db.SeedOnce("w-seed", 25)
			require.NoError(t, err)
			require.False(t, seeded)
			require.Equal(t, 25.0, bal)
			require.Equal(t, 1, seedRowsOf(t, db, "w-seed"))
			require.Equal(t, 1, seededCount(t, db))

			// A later $0 settle leaves the seeded balance alone.
			bal, err = db.Settle("w-seed", "n1", 0, 0, zeroRec("r2"))
			require.NoError(t, err)
			require.Equal(t, 25.0, bal)
			derived, err := db.DeriveBalance("w-seed")
			require.NoError(t, err)
			require.Equal(t, 25.0, derived)
		})
	}
}

// TestZeroSettleThenSeedOnceParity: the login path (SeedOnce) grants the seed to a wallet
// first touched by a $0 settle, exactly as the read path does.
func TestZeroSettleThenSeedOnceParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			_, err := db.Settle("w-login", "n1", 0, 0, zeroRec("r1"))
			require.NoError(t, err)
			bal, seeded, err := db.SeedOnce("w-login", 25)
			require.NoError(t, err)
			require.True(t, seeded)
			require.Equal(t, 25.0, bal)
			require.Equal(t, 1, seedRowsOf(t, db, "w-login"))
		})
	}
}

// TestZeroSettleDoesNotSpendTheSeedCapParity: with a seed cap of one wallet, a $0 settle for
// wallet A takes no slot - wallet B still receives the one seed, and A (now capped out) has a
// zero-balance row and no seed.
func TestZeroSettleDoesNotSpendTheSeedCapParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			db.SetSeedLimit(1)
			_, err := db.Settle("w-a", "n1", 0, 0, zeroRec("r1"))
			require.NoError(t, err)
			require.Zero(t, seededCount(t, db))

			bal, err := db.BalanceOf("w-b", 25)
			require.NoError(t, err)
			require.Equal(t, 25.0, bal, "the cap's one seed is still available after a $0 settle elsewhere")

			bal, err = db.BalanceOf("w-a", 25)
			require.NoError(t, err)
			require.Zero(t, bal, "the cap is exhausted: A is created at 0")
			require.Zero(t, seedRowsOf(t, db, "w-a"))
			require.Equal(t, 1, seededCount(t, db))
		})
	}
}

// TestZeroSettleConcurrentNewWalletParity: two broker instances (two handles on one store)
// settling $0 for the SAME brand-new wallet at once all succeed, write one receipt each, and
// leave one wallet at balance 0.
func TestZeroSettleConcurrentNewWalletParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			handles := []Store{db, db}
			if pg, ok := db.(*Postgres); ok {
				second, err := NewPostgres(storePrivateDSN(t, os.Getenv("ROGERAI_TEST_DATABASE_URL")))
				require.NoError(t, err)
				t.Cleanup(func() { _ = second.Close() })
				handles = []Store{pg, second}
			}
			const n = 16
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, errs[i] = handles[i%2].Settle("w-race", "n1", 0, 0, zeroRec("race-"+string(rune('a'+i))))
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				require.NoError(t, err, "settle %d", i)
			}
			es, err := db.RecentByUser("w-race", 100)
			require.NoError(t, err)
			require.Len(t, es, n, "every concurrent settle recorded its receipt")
			peek, err := db.PeekBalance("w-race")
			require.NoError(t, err)
			require.Zero(t, peek)
			require.Zero(t, seedRowsOf(t, db, "w-race"))
			if pg, ok := db.(*Postgres); ok {
				var rows int
				require.NoError(t, pg.db.QueryRow(`SELECT count(*) FROM rogerai.wallet WHERE usr=$1`, "w-race").Scan(&rows))
				require.Equal(t, 1, rows, "one wallet row, whichever instance created it")
			}
		})
	}
}

// TestZeroSettleRowAndReceiptAreOneTransaction (Postgres): when the receipt cannot be written
// the wallet row the settle would have created is rolled back with it - a wallet row never
// outlives a settle that recorded nothing. The failure is a REAL database error: a token
// count past the receipts table's INT column.
func TestZeroSettleRowAndReceiptAreOneTransaction(t *testing.T) {
	pg := pgOnly(t)
	rec := zeroRec("r-overflow")
	rec.PromptTokens = 1 << 40 // out of range for receipts.prompt_tokens (INT)
	_, err := pg.Settle("w-atomic", "n1", 0, 0, rec)
	require.Error(t, err, "the receipt insert must fail")

	var rows int
	require.NoError(t, pg.db.QueryRow(`SELECT count(*) FROM rogerai.wallet WHERE usr=$1`, "w-atomic").Scan(&rows))
	require.Zero(t, rows, "no orphan wallet row survives the rolled-back settle")
	require.NoError(t, pg.db.QueryRow(`SELECT count(*) FROM rogerai.receipts WHERE usr=$1`, "w-atomic").Scan(&rows))
	require.Zero(t, rows)

	// The same wallet settles cleanly afterwards.
	bal, err := pg.Settle("w-atomic", "n1", 0, 0, zeroRec("r-ok"))
	require.NoError(t, err)
	require.Zero(t, bal)
	require.NoError(t, pg.db.QueryRow(`SELECT count(*) FROM rogerai.wallet WHERE usr=$1 AND balance=0 AND seed_remaining=0`, "w-atomic").Scan(&rows))
	require.Equal(t, 1, rows)
}
