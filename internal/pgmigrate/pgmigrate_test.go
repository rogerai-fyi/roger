package pgmigrate

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// recorder counts attempts and fails the first n of them.
type recorder struct {
	calls   int
	failFor int
	err     error
}

func (r *recorder) Exec(string, ...any) (sql.Result, error) {
	r.calls++
	if r.calls <= r.failFor {
		return nil, r.err
	}
	return nil, nil
}

func TestAMigrationThatSucceedsRunsOnce(t *testing.T) {
	// The common case must not pay for the rare one.
	r := &recorder{}
	require.NoError(t, Apply(r, "CREATE TABLE IF NOT EXISTS x()"))
	require.Equal(t, 1, r.calls)
}

func TestALostCatalogRaceIsRetriedOnce(t *testing.T) {
	// Two instances starting together: one loses on a catalog unique-violation, and the
	// object exists by the time it sees the error. A rolling deploy is exactly this.
	r := &recorder{failFor: 1, err: errors.New(
		`ERROR: duplicate key value violates unique constraint "pg_type_typname_nsp_index"`)}
	require.NoError(t, Apply(r, "CREATE TABLE IF NOT EXISTS x()"))
	require.Equal(t, 2, r.calls, "one retry, not a loop")
}

func TestARealFailureIsReturnedRatherThanRetriedIntoSilence(t *testing.T) {
	// A permission problem, a missing schema, or a genuinely bad migration must reach the
	// operator. Retrying forever would turn a broken deploy into a hang.
	boom := errors.New("ERROR: permission denied for database roger")
	r := &recorder{failFor: 99, err: boom}

	err := Apply(r, "CREATE TABLE IF NOT EXISTS x()")
	require.ErrorIs(t, err, boom, "the second failure is surfaced as-is")
	require.Equal(t, 2, r.calls, "and it stops there")
}

// stmtRecorder keeps every statement it is handed, one Exec per entry.
type stmtRecorder struct{ got []string }

func (r *stmtRecorder) Exec(q string, _ ...any) (sql.Result, error) {
	r.got = append(r.got, q)
	return nil, nil
}

func TestEachStatementRunsInItsOwnExec(t *testing.T) {
	// One Exec of a multi-statement string is ONE implicit transaction, which holds every
	// lock it takes until the end. A migration that locks table A and then wants table B,
	// while a request holds B and wants A, deadlocks. One statement per Exec means the
	// migration never holds one table while waiting for another.
	r := &stmtRecorder{}
	require.NoError(t, Apply(r, `
ALTER TABLE a ADD COLUMN IF NOT EXISTS x INT;
CREATE INDEX IF NOT EXISTS b_idx ON b (y);
`))
	require.Equal(t, []string{
		"ALTER TABLE a ADD COLUMN IF NOT EXISTS x INT",
		"CREATE INDEX IF NOT EXISTS b_idx ON b (y)",
	}, r.got)
}

// perStmt fails the first failFirst[q] attempts of each statement q, and records every call.
type perStmt struct {
	failFirst map[string]int
	err       error
	got       []string
}

func (r *perStmt) Exec(q string, _ ...any) (sql.Result, error) {
	r.got = append(r.got, q)
	if r.failFirst[q] > 0 {
		r.failFirst[q]--
		return nil, r.err
	}
	return nil, nil
}

func TestEachStatementGetsItsOwnRetry(t *testing.T) {
	// Two instances starting together walk the same statement list side by side, so the
	// loser can lose a catalog race on one statement and then on a later one too. Each loss
	// means that object now exists, so each statement is retried on its own; retrying the
	// whole script once would let the second loss fail the deploy.
	race := errors.New(`ERROR: duplicate key value violates unique constraint "pg_type_typname_nsp_index"`)
	r := &perStmt{err: race, failFirst: map[string]int{
		"CREATE TABLE IF NOT EXISTS a()": 1,
		"CREATE TABLE IF NOT EXISTS c()": 1,
	}}
	require.NoError(t, Apply(r,
		"CREATE TABLE IF NOT EXISTS a(); CREATE TABLE IF NOT EXISTS b(); CREATE TABLE IF NOT EXISTS c()"))
	require.Equal(t, []string{
		"CREATE TABLE IF NOT EXISTS a()", "CREATE TABLE IF NOT EXISTS a()",
		"CREATE TABLE IF NOT EXISTS b()",
		"CREATE TABLE IF NOT EXISTS c()", "CREATE TABLE IF NOT EXISTS c()",
	}, r.got, "a lost race is retried where it happened, and statements already done are not rerun")
}

func TestAStatementThatFailsTwiceStopsTheMigration(t *testing.T) {
	boom := errors.New("ERROR: permission denied for schema rogerai")
	r := &perStmt{err: boom, failFirst: map[string]int{"CREATE TABLE IF NOT EXISTS b()": 99}}
	err := Apply(r, "CREATE TABLE IF NOT EXISTS a(); CREATE TABLE IF NOT EXISTS b(); CREATE TABLE IF NOT EXISTS c()")
	require.ErrorIs(t, err, boom, "the second failure is surfaced as-is")
	require.Equal(t, []string{
		"CREATE TABLE IF NOT EXISTS a()",
		"CREATE TABLE IF NOT EXISTS b()", "CREATE TABLE IF NOT EXISTS b()",
	}, r.got, "one retry, then stop: nothing after the failure runs")
}

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"only whitespace and separators", " ;\n ; ", nil},
		{"only comments", "-- a; b\n/* c; d */\n", nil},
		{"no trailing semicolon", "SELECT 1", []string{"SELECT 1"}},
		{"trailing semicolon", "SELECT 1;", []string{"SELECT 1"}},
		{"two statements", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"semicolon in a line comment", "-- one; two\nSELECT 1;",
			[]string{"-- one; two\nSELECT 1"}},
		{"comment between statements stays with the next",
			"SELECT 1;\n-- about two; really\nSELECT 2;",
			[]string{"SELECT 1", "-- about two; really\nSELECT 2"}},
		{"semicolon in a block comment", "SELECT /* a;b */ 1; SELECT 2",
			[]string{"SELECT /* a;b */ 1", "SELECT 2"}},
		{"semicolon in a string", "INSERT INTO t VALUES('a;b'); SELECT 2",
			[]string{"INSERT INTO t VALUES('a;b')", "SELECT 2"}},
		{"escaped quote in a string", "SELECT 'it''s; fine'; SELECT 2",
			[]string{"SELECT 'it''s; fine'", "SELECT 2"}},
		{"semicolon in a quoted identifier", `SELECT 1 AS "a;b"; SELECT 2`,
			[]string{`SELECT 1 AS "a;b"`, "SELECT 2"}},
		{"dollar-quoted body", "DO $$ BEGIN PERFORM 1; END $$; SELECT 2",
			[]string{"DO $$ BEGIN PERFORM 1; END $$", "SELECT 2"}},
		{"tagged dollar quote", "DO $fn$ BEGIN PERFORM '$$;'; END $fn$; SELECT 2",
			[]string{"DO $fn$ BEGIN PERFORM '$$;'; END $fn$", "SELECT 2"}},
		{"positional parameter is not a dollar quote", "SELECT $1; SELECT 2",
			[]string{"SELECT $1", "SELECT 2"}},
		{"unterminated string keeps the rest as one statement", "SELECT 'a; b",
			[]string{"SELECT 'a; b"}},
		{"line comment at end of input", "SELECT 1; -- done",
			[]string{"SELECT 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, splitStatements(c.in))
		})
	}
}
