package link

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// The production Mirror, against real PostgreSQL. A JSON-free single-row upsert is easy
// to get subtly wrong - a dropped field orphans the relay plane, a lost LastSeen makes
// every record permanently stale - so the round trip runs against the real driver the
// way the other durable towercore stores do.

func pgMirror(t *testing.T) *PGMirror {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the durable link-mirror tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	m, err := NewPGMirror(db)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM rogerai.tower_link_mirror`)
	require.NoError(t, err)
	return m
}

func TestPGMirrorRoundTripsEveryField(t *testing.T) {
	m := pgMirror(t)
	seen := time.Now().UTC().Truncate(time.Microsecond)
	rec := Record{SessionID: "sess-1", Version: 1, LastSeen: seen,
		Relay: RelayPlane{Endpoint: "hub.example.net:8444", TLSSPKI: "aa11"}}
	require.NoError(t, m.Put("tw-1", rec))

	got, ok, err := m.Get("tw-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rec.SessionID, got.SessionID)
	require.Equal(t, rec.Version, got.Version)
	require.True(t, got.LastSeen.Equal(seen), "LastSeen %v != %v - a lost timestamp makes every record stale", got.LastSeen, seen)
	require.Equal(t, rec.Relay, got.Relay, "a dropped plane strands every consumer of this Tower")

	// Upsert refreshes in place: last write wins, which is what a heartbeat wants.
	rec.LastSeen = seen.Add(time.Minute)
	rec.SessionID = "sess-2"
	require.NoError(t, m.Put("tw-1", rec))
	got, _, err = m.Get("tw-1")
	require.NoError(t, err)
	require.Equal(t, "sess-2", got.SessionID)

	all, err := m.All()
	require.NoError(t, err)
	require.Len(t, all, 1)

	// The compare half: a superseded session id deletes nothing.
	require.NoError(t, m.Del("tw-1", "sess-1"))
	_, still, serr := m.Get("tw-1")
	require.NoError(t, serr)
	require.True(t, still, "a stale close must not dim the newer row")

	require.NoError(t, m.Del("tw-1", "sess-2"))
	_, ok, err = m.Get("tw-1")
	require.NoError(t, err)
	require.False(t, ok, "a deleted record is the close's tombstone; it must be GONE, not stale")
	require.NoError(t, m.Del("tw-1", "sess-2"), "deleting an absent record is not an error")
}

// The whole point of the mirror, on the real store: two Sessions (two instances) over one
// database agree about one Tower.
func TestPGMirrorCarriesALinkAcrossInstances(t *testing.T) {
	m := pgMirror(t)
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-pg", "hub.example.net:8444")

	require.True(t, b.Live("tw-pg"), "instance B has never met the Tower and must still see it live")
	p, has := b.RelayPlane("tw-pg")
	require.True(t, has)
	require.Equal(t, "hub.example.net:8444", p.Endpoint)
	require.NoError(t, b.Heartbeat(sess, "tw-pg"), "the heartbeat adopts on B from the shared record")

	a.Close(sess, "tw-pg")
	require.False(t, a.Live("tw-pg"))
	require.False(t, b.Live("tw-pg"), "the close's tombstone reaches the adopting instance")
}
