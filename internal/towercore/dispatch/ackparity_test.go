package dispatch

// ackparity_test.go runs one scenario through BOTH acknowledgement stores and requires the
// same answer, for the same reason parity_test.go does it for attempts: the two are written
// differently - a held mutex over a map against an ON CONFLICT DO NOTHING - so agreement is a
// result rather than the same code asserted twice.
//
// It matters here because the rule being enforced is "first write wins", and a first-write
// rule that holds in memory and not in the database is worse than none: an operator's
// evidence could be revised after the fact in production while every test passed.
//
// Without ROGERAI_TEST_DATABASE_URL the durable half skips and the memory half still runs.

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func ackStores(t *testing.T) map[string]AckStore {
	t.Helper()
	out := map[string]AckStore{"mem": NewAckMemStore()}

	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewAckPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_acks`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func anAck(t *testing.T, attemptID string, out int64) Ack {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	a, err := SignAck(priv, "roger-public", attemptID, []byte("the answer"),
		Usage{In: 5, Out: out}, time.Unix(1_700_000_001, 0), time.Unix(1_700_000_009, 0))
	require.NoError(t, err)
	return a
}

func TestAckParityAnAcknowledgementRoundTrips(t *testing.T) {
	for name, s := range ackStores(t) {
		t.Run(name, func(t *testing.T) {
			a := anAck(t, "att-1", 42)
			require.NoError(t, s.Put("att-1", a))

			got, found, err := s.Get("att-1")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, a.AttemptID, got.AttemptID)
			require.Equal(t, a.ResponseDigest, got.ResponseDigest)
			require.Equal(t, a.Usage, got.Usage)
			require.Equal(t, a.FirstByte.Unix(), got.FirstByte.Unix())
			require.Equal(t, a.Completed.Unix(), got.Completed.Unix())
			// THE SIGNED OBJECT SURVIVES. A digest we merely copied out would be our word for
			// what the consumer said; the point of keeping this is that a settlement can be
			// re-checked later by somebody who was not here.
			require.Equal(t, a.Signed, got.Signed)
		})
	}
}

// FIRST WRITE WINS. A second acknowledgement is either a retry (identical, nothing to do) or
// an attempt to revise evidence after the fact, which must not be possible. Both are handled
// by refusing to overwrite, and the caller is not told which - "your first one stands" is the
// whole answer either way.
func TestAckParityEvidenceCannotBeRevisedAfterTheFact(t *testing.T) {
	for name, s := range ackStores(t) {
		t.Run(name, func(t *testing.T) {
			first := anAck(t, "att-1", 10)
			require.NoError(t, s.Put("att-1", first))
			// A second, claiming far less usage - which is what a consumer wanting a smaller
			// bill would file.
			require.NoError(t, s.Put("att-1", anAck(t, "att-1", 1)))

			got, found, err := s.Get("att-1")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, int64(10), got.Usage.Out, "the first acknowledgement must stand")
		})
	}
}

func TestAckParityAnAbsentAcknowledgementIsNotAnError(t *testing.T) {
	for name, s := range ackStores(t) {
		t.Run(name, func(t *testing.T) {
			// This is the ORDINARY case, not an exceptional one: most consumers never ack.
			got, found, err := s.Get("att-nobody")
			require.NoError(t, err)
			require.False(t, found)
			require.Empty(t, got.AttemptID)
		})
	}
}

func TestAckParityAnAcknowledgementNeedsAnAttempt(t *testing.T) {
	for name, s := range ackStores(t) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.Put("", anAck(t, "att-1", 1)))
		})
	}
}

func TestAckParityOldEvidenceIsReaped(t *testing.T) {
	for name, s := range ackStores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Put("att-1", anAck(t, "att-1", 1)))

			// Nothing recorded in the future is touched.
			n, err := s.Reap(time.Now().Add(-time.Hour))
			require.NoError(t, err)
			require.Zero(t, n)
			_, found, err := s.Get("att-1")
			require.NoError(t, err)
			require.True(t, found)

			// And an attempt that never settled cannot settle later, so it goes.
			n, err = s.Reap(time.Now().Add(time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
			_, found, err = s.Get("att-1")
			require.NoError(t, err)
			require.False(t, found)
		})
	}
}

func TestADurableAckStoreNeedsADatabase(t *testing.T) {
	_, err := NewAckPGStore(nil)
	require.ErrorContains(t, err, "needs a database handle")
}
