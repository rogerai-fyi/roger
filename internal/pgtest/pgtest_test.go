package pgtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sharedOrSkip is the one place these tests read the shared server directly: they test
// the thing that hides it from everyone else.
func sharedOrSkip(t *testing.T) string {
	t.Helper()
	shared := os.Getenv(Env)
	if shared == "" {
		t.Skip(Env + " not set; skipping the private-database tests")
	}
	return shared
}

func dbName(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	return strings.TrimPrefix(u.Path, "/")
}

func queryInt(t *testing.T, dsn, q string, args ...any) int {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(q, args...).Scan(&n))
	return n
}

// forget makes the next privateFor(shared, key) behave as a new process would.
func forget(shared, key string) {
	mu.Lock()
	defer mu.Unlock()
	delete(created, shared+"\x00"+key)
}

func TestWithoutAServerThereIsNoDatabase(t *testing.T) {
	t.Setenv(Env, "")
	require.Equal(t, "", DSN(t))
	dsn, err := Private()
	require.NoError(t, err)
	require.Equal(t, "", dsn)
}

func TestThePackageGetsItsOwnDatabaseWithTheSchema(t *testing.T) {
	shared := sharedOrSkip(t)
	dsn := DSN(t)
	require.NotEqual(t, dbName(t, shared), dbName(t, dsn), "the private database must not BE the shared one")
	require.True(t, strings.HasPrefix(dbName(t, dsn), dbName(t, shared)+"_pgtest_"),
		"the name says which package it belongs to: %s", dbName(t, dsn))
	require.Equal(t, 1, queryInt(t, dsn,
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name='rogerai'`))
	// Once per process: every test in the binary shares the one database.
	require.Equal(t, dsn, DSN(t))
}

func TestTwoPackagesCannotSeeEachOthersTables(t *testing.T) {
	shared := sharedOrSkip(t)
	a, err := privateFor(shared, "/src/internal/alpha")
	require.NoError(t, err)
	b, err := privateFor(shared, "/src/internal/beta")
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	db, err := sql.Open("pgx", a)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE rogerai.only_in_alpha (id INT)`)
	require.NoError(t, err)

	require.Equal(t, 0, queryInt(t, b,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='only_in_alpha'`))
}

func TestTheSameDirectoryNameInTwoPlacesIsTwoDatabases(t *testing.T) {
	shared := sharedOrSkip(t)
	a, err := privateFor(shared, "/src/internal/store")
	require.NoError(t, err)
	b, err := privateFor(shared, "/src/cmd/store")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

func TestEachRunGetsAFreshDatabaseAndLeavesTheLastOneAlone(t *testing.T) {
	shared := sharedOrSkip(t)
	first, err := privateFor(shared, "/src/internal/rerun")
	require.NoError(t, err)
	db, err := sql.Open("pgx", first)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE rogerai.leftover (id INT)`)
	require.NoError(t, err)

	forget(shared, "/src/internal/rerun") // a second process for the same package, e.g. another session's run
	second, err := privateFor(shared, "/src/internal/rerun")
	require.NoError(t, err)
	require.NotEqual(t, first, second, "a second run must never be handed - or reset - the first one's database")
	require.Equal(t, 0, queryInt(t, second,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='leftover'`))
	// The first run, still connected, is untouched.
	require.Equal(t, 1, queryInt(t, first,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='leftover'`))
}

func TestARefusalNamesTheProblem(t *testing.T) {
	cases := map[string]struct {
		shared, key, want string
	}{
		"not a URL":         {"postgres://%zz", "/x", "must be a postgres:// URL"},
		"keyword DSN":       {"host=127.0.0.1 dbname=roger_test", "/x", "must be a postgres:// URL"},
		"no database named": {"postgres://u@127.0.0.1:1/", "/x", "must be a postgres:// URL"},
		"name too long": {"postgres://u@127.0.0.1:1/" + strings.Repeat("d", 60), "/x",
			"exceeds 63 bytes"},
		// Port 1 refuses connections, so this is the server being unreachable.
		"server unreachable": {"postgres://u@127.0.0.1:1/roger_test?connect_timeout=2", "/x", "create roger_test_x_"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := privateFor(tc.shared, tc.key)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestAProvisioningFailureFailsTheTest(t *testing.T) {
	t.Setenv(Env, "postgres://u@127.0.0.1:1/")
	rec := &recordingTB{TB: t}
	_ = DSN(rec)
	require.Contains(t, rec.fatal, "pgtest: ")
}

func TestLabelKeepsOnlyIdentifierCharacters(t *testing.T) {
	require.Equal(t, "roger_tower", label("Roger-Tower"))
	require.Equal(t, "abcdefghijklmnop", label("abcdefghijklmnopqrstuvwxyz"))
}

// recordingTB captures Fatalf instead of stopping the test, so the refusal path itself
// can be asserted.
type recordingTB struct {
	testing.TB
	fatal string
}

func (r *recordingTB) Fatalf(format string, args ...any) { r.fatal = fmt.Sprintf(format, args...) }
