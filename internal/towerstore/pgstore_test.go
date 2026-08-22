package towerstore

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
)

// The database-backed Tower store. It lives OUTSIDE internal/tower on purpose: that
// package is covered by a gate test that fails if any file in it gains the ability to
// reach the network, and a database driver dials. Keeping the driver here is what lets
// the standalone core stay provably egress-free while still having durable storage.
//
// These tests cover everything that does not need a live database. The round trip
// against real PostgreSQL runs in the store suite the coverage gate already stands up.

func TestOpenRefusesADestinationOutsideThePrivateAllowlist(t *testing.T) {
	// The spec permits a local PostgreSQL, but every resolved address must stay inside
	// the operator's declared private range - otherwise "standalone talks to nothing"
	// stops being true the moment someone points it at a hosted database.
	for name, dsn := range map[string]string{
		"public host":       "postgres://u:p@db.example.com:5432/tower",
		"public IP":         "postgres://u:p@93.184.216.34:5432/tower",
		"instance metadata": "postgres://u:p@169.254.169.254:5432/tower",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(dsn, nil)
			require.Error(t, err, "%s must be refused", dsn)
			require.Contains(t, err.Error(), "allowlist")
		})
	}
}

func TestOpenAcceptsALoopbackOrPrivateDestination(t *testing.T) {
	// Accepted by the allowlist. (Connecting is a separate matter - these tests do not
	// require a live database, so Open must not dial eagerly.)
	for _, dsn := range []string{
		"postgres://u:p@127.0.0.1:5432/tower",
		"postgres://u:p@10.1.2.3:5432/tower",
		"postgres://u:p@[::1]:5432/tower",
	} {
		_, err := Open(dsn, nil)
		require.NoError(t, err, "%s is a private destination", dsn)
	}
}

func TestOpenRejectsAMalformedDSN(t *testing.T) {
	for _, dsn := range []string{"", "not a url", "postgres://", "://x"} {
		_, err := Open(dsn, nil)
		require.Error(t, err, "%q must be refused", dsn)
	}
}

// A hostname is refused rather than resolved: resolving it is already the DNS lookup the
// standalone contract forbids, and a name that resolves somewhere private today can
// resolve elsewhere tomorrow.
// "localhost" is accepted as the constant it is - substituted for the loopback literal,
// never resolved - because every PostgreSQL DSN a person writes says localhost. Every
// OTHER hostname is still refused rather than looked up.
func TestLocalhostIsAcceptedButNoOtherHostname(t *testing.T) {
	_, err := Open("postgres://u:p@localhost:5432/tower", nil)
	require.NoError(t, err, "the documented local-database path must work")

	for _, host := range []string{"db.internal", "postgres.local", "localhost.evil.example", "LOCALHOST"} {
		_, err := Open("postgres://u:p@"+host+":5432/tower", nil)
		require.Error(t, err, "%s must be refused without a lookup", host)
		require.Contains(t, err.Error(), "literal IP")
	}
}

// The store must satisfy the seam the Tower core defines, or it cannot be plugged in.
func TestPGStoreImplementsTheTowerStore(t *testing.T) {
	s, err := Open("postgres://u:p@127.0.0.1:5432/tower", nil)
	require.NoError(t, err)
	var _ tower.Store = s
}

// --- against a real PostgreSQL --------------------------------------------
//
// Skipped unless ROGERAI_TEST_DATABASE_URL is set, which the coverage gate does when it
// stands up its throwaway container. A JSON round trip is easy to get subtly wrong -
// a dropped verifier secret or a revision that does not advance - so this exercises the
// real driver rather than trusting the shape.

func realDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL; skipping the live-database round trip")
	}
	return dsn
}

func freshStore(t *testing.T) *PGStore {
	t.Helper()
	s, err := Open(realDSN(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	db, err := s.connect()
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM tower_admission`)
	require.NoError(t, err)
	return s
}

func TestLiveRoundTripPreservesEveryFieldThatMatters(t *testing.T) {
	s := freshStore(t)

	snap, err := s.Load()
	require.NoError(t, err)
	require.NotEmpty(t, snap.HMACKey, "a fresh database mints the verifier secret")
	require.Zero(t, snap.Revision)

	snap.Operator = &tower.Credential{ClientKeyHash: "k1", Role: tower.RoleLocalOperator}
	snap.Stations = map[string]*tower.Station{"st-1": {ID: "st-1", KeyHash: "sk", Models: []string{"m"}}}
	rev, err := s.Save(snap)
	require.NoError(t, err)
	require.Equal(t, int64(1), rev)

	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, snap.HMACKey, got.HMACKey, "losing the verifier secret kills every open invitation")
	require.Equal(t, "k1", got.Operator.ClientKeyHash)
	require.Len(t, got.Stations, 1)
	require.Equal(t, int64(1), got.Revision)
}

// The reason a database store exists at all: several processes. A stale write must lose
// rather than silently overwrite whatever the other one did.
func TestLiveStaleWriteIsRefused(t *testing.T) {
	s := freshStore(t)

	first, err := s.Load()
	require.NoError(t, err)
	second, err := s.Load() // another process, same revision
	require.NoError(t, err)

	first.Operator = &tower.Credential{ClientKeyHash: "winner"}
	_, err = s.Save(first)
	require.NoError(t, err)

	second.Operator = &tower.Credential{ClientKeyHash: "loser"}
	_, err = s.Save(second)
	require.ErrorIs(t, err, tower.ErrStaleWrite)

	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "winner", got.Operator.ClientKeyHash)
}

// A Tower plugged into the database store behaves exactly as one on files: this is the
// property that makes the seam worth having.
func TestLiveStoreDrivesTheWholeAdmissionFlow(t *testing.T) {
	s := freshStore(t)
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	st = st.WithStore(s)

	inv, code, err := st.CreateInvitation("client-key", time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, "client-key")
	require.NoError(t, err)
	_, err = st.AttachStation("st-1", "sk", []string{"llama-8b"})
	require.NoError(t, err)

	rec, err := st.Route("client-key", "llama-8b")
	require.NoError(t, err)
	require.Equal(t, "st-1", rec.StationID)

	// A "restart": a new State over the same database sees the same network.
	again, err := tower.Open(dir)
	require.NoError(t, err)
	again = again.WithStore(s)
	op, err := again.LocalOperator()
	require.NoError(t, err)
	require.Equal(t, "client-key", op.ClientKeyHash)

	// And the consumed code is still consumed.
	_, err = again.ConsumeInvitation(inv.ID, code, "client-key")
	require.Error(t, err, "persistence is what stops a replay across restarts")
}

// --- failure paths ---------------------------------------------------------

func TestConnectFailsWhenNothingIsListening(t *testing.T) {
	// A private address with no database behind it: the store must report that it cannot
	// reach the database, not pretend the state is empty.
	s, err := Open("postgres://u:p@127.0.0.1:1/tower", nil)
	require.NoError(t, err, "the destination is private, so Open accepts it")

	_, err = s.Load()
	require.Error(t, err, "Load must fail rather than return an empty snapshot")
	require.Contains(t, err.Error(), "cannot reach")

	_, err = s.Save(&tower.Snapshot{})
	require.Error(t, err, "Save must fail rather than silently drop the write")
}

func TestCloseIsSafeBeforeAndAfterConnecting(t *testing.T) {
	s, err := Open("postgres://u:p@127.0.0.1:5432/tower", nil)
	require.NoError(t, err)
	require.NoError(t, s.Close(), "closing a store that never dialled must be harmless")

	live := freshStore(t) // skips without a database
	require.NoError(t, live.Close())
	require.NoError(t, live.Close(), "closing twice must be harmless")
}

// Corrupt stored state must not read as empty state: that would re-mint the verifier
// secret and orphan every credential already issued.
func TestLiveCorruptStateIsReported(t *testing.T) {
	s := freshStore(t)
	db, err := s.connect()
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tower_admission (id, revision, state) VALUES (1, 1, '"not an object"'::jsonb)`)
	require.NoError(t, err)

	_, err = s.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unreadable")
}

func TestLiveSchemaIsIdempotent(t *testing.T) {
	s := freshStore(t)
	_, err := s.connect()
	require.NoError(t, err)
	// Re-opening applies the schema again; starting a Tower against an existing database
	// must never destroy what is there.
	other, err := Open(realDSN(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	_, err = other.connect()
	require.NoError(t, err)
}

// Using a store after it is closed must surface the failure, not silently drop the write.
// A Tower that thinks it saved an admission when it did not is the worst outcome here.
func TestLiveUseAfterCloseFailsLoudly(t *testing.T) {
	s := freshStore(t)
	snap, err := s.Load()
	require.NoError(t, err)
	require.NoError(t, s.Close())

	_, err = s.Save(snap)
	require.Error(t, err, "a write against a closed pool must fail")

	_, err = s.Load()
	require.Error(t, err, "a read against a closed pool must fail")
}

func TestCheckDestinationRejectsAnUnparseableURL(t *testing.T) {
	require.Error(t, checkDestination("postgres://%zz", nil))
	require.Error(t, checkDestination("", nil))
}
