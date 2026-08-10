package attach

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// A DATABASE THAT CANNOT ANSWER MUST SAY SO.
//
// Every method here has an error branch, and each one decides the same thing: whether an
// operator is told their attachment was refused or that Core cannot answer right now. In
// this path the difference is expensive - "refused" sends somebody off to regenerate keys
// that were never the problem, and to burn an invitation trying again.
//
// Closing the pool underneath a live store is the cheapest way to reach all of them at once
// and is the same trick internal/store uses.

func TestPGStoreNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err, "a durable registry cannot be built on a nil handle")
	require.Contains(t, err.Error(), "database handle")
}

func TestAClosedPoolReportsAnOutageOnEveryPath(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	seed := Authorization{
		ID: "auth-closed", Network: net, StationID: "st-closed", Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ac", SessionKey: "Kc",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, pg.PutAuthorization(seed))

	require.NoError(t, db.Close())

	require.ErrorIs(t, pg.PutAuthorization(seed), ErrUnavailable)

	_, _, err = pg.Authorization("auth-closed")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = pg.Admit("auth-closed", Attachment{StationID: "st-closed", State: StateQuarantine})
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = pg.ByStation("st-closed")
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = pg.ByAssertionKey("Ac")
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = pg.BySessionKey("Kc")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = pg.SetState("st-closed", StateRevoked)
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = pg.Reap(now, 24*time.Hour)
	require.ErrorIs(t, err, ErrUnavailable)

	// And none of them is a refusal - an operator must never be told their keys are wrong
	// because the pool went away.
	_, _, err = pg.Authorization("auth-closed")
	require.NotErrorIs(t, err, ErrRejected)
}

// A schema that cannot be created is a startup failure, not a silently degraded registry.
// The broker must refuse to come up rather than run with joined attachment quietly missing.
func TestAnUnusableDatabaseFailsTheConstructor(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewPGStore(db)
	require.Error(t, err, "a registry that cannot apply its schema must not report success")
}

// Admit against an invitation that does not exist is a REFUSAL, not an outage: the row is
// simply absent, and the caller turns that into "no such invitation".
func TestAdmitOnAnUnknownInvitationIsNotAnOutage(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)

	won, err := pg.Admit("auth-does-not-exist", Attachment{StationID: "st-x", State: StateQuarantine})
	require.NoError(t, err, "an absent row is not a database failure")
	require.False(t, won)
}

// The partial unique index is the database's own enforcement of one-live-Station-per-key.
// Application ordering handles the ordinary case; this is what holds when two transactions
// check at the same instant, so it is worth proving the constraint exists and bites.
func TestTheLiveKeyIndexRefusesASecondHolder(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.station_authorizations, rogerai.station_attachments`)
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	insert := func(stationID, state string) error {
		_, err := db.Exec(`
			INSERT INTO rogerai.station_attachments
			  (station_id,owner,assertion_key,session_key,origin_kind,origin_tower,epoch,
			   ceiling_hash,state,attached_at,auth_id)
			VALUES ($1,$2,'shared_A','shared_K','joined',$3,1,'',$4,$5,'')`,
			stationID, owner, tower, state, now)
		return err
	}
	require.NoError(t, insert("st-a", StateQuarantine))
	require.Error(t, insert("st-b", StateActive),
		"the database itself must refuse a second LIVE holder of the same keys")

	// Retire the first, and the keys are free again - the index is partial for exactly this.
	_, err = pg.SetState("st-a", StateRevoked)
	require.NoError(t, err)
	require.NoError(t, insert("st-b", StateActive),
		"a revoked Station must not hold its keys hostage")
}
