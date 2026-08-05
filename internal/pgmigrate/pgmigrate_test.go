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
