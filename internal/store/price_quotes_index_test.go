package store

// price_quotes_index_test.go - the hold sweep prunes expired price quotes by locked_until
// (PruneExpiredPriceQuotes); without an index on that column every sweep seq-scans the table.
// Postgres only (the in-memory store has no query planner).

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriceQuotesHaveALockedUntilIndex(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL unset")
	}
	p := freshPostgres(t, dsn)
	var n int
	require.NoError(t, p.db.QueryRow(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'rogerai' AND tablename = 'price_quotes' AND indexdef ILIKE '%(locked_until)%'`).Scan(&n))
	require.Equal(t, 1, n, "price_quotes needs an index on locked_until for the prune")
}
