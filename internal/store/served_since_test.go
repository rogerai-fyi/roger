package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/pgtest"
	"rogerai.fm/roger/v7/internal/protocol"
)

// What a node served and earned since a time is counted by the store, exactly, however many
// receipts there are (features/serving/probe_accounting.feature): reading the newest N rows and
// adding them up undercounted a busy node, and the overflow showed as Core's checks.
func TestServedSinceCountsEveryReceipt(t *testing.T) {
	run := func(t *testing.T, s Store) {
		node := fmt.Sprintf("ss-%d", time.Now().UnixNano())
		user := "u_ss_" + node
		_, err := s.BalanceOf(user, 0)
		require.NoError(t, err)
		_, err = s.AddCredits(user, 1000)
		require.NoError(t, err)
		since := time.Now().Unix() - 10
		old := protocol.UsageReceipt{RequestID: node + "-old", CompletionTokens: 7, TS: since - 100}
		_, err = s.Settle(user, node, 0.01, 0.009, old)
		require.NoError(t, err)
		const n = 2105
		for i := 0; i < n; i++ {
			rec := protocol.UsageReceipt{RequestID: fmt.Sprintf("%s-%d", node, i), CompletionTokens: 3, TS: since + 1}
			_, err := s.Settle(user, node, 0.002, 0.0018, rec)
			require.NoError(t, err)
		}
		served, tokens, earned, err := s.ServedSince(node, since)
		require.NoError(t, err)
		require.Equal(t, int64(n), served)
		require.Equal(t, int64(3*n), tokens)
		require.InDelta(t, 0.0018*n, earned, 1e-6)
		served, _, _, err = s.ServedSince("no-such-node", 0)
		require.NoError(t, err)
		require.Zero(t, served)
	}
	t.Run("mem", func(t *testing.T) { run(t, NewMem()) })
	t.Run("postgres", func(t *testing.T) {
		dsn := pgtest.DSN(t)
		if dsn == "" {
			t.Skip("ROGERAI_TEST_DATABASE_URL not set")
		}
		pg, err := NewPostgres(dsn)
		require.NoError(t, err)
		defer pg.Close()
		run(t, pg)
	})
}
