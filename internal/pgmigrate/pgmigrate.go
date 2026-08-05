// Package pgmigrate applies a schema at startup, tolerating the one race PostgreSQL
// genuinely has and refusing to hide anything else.
//
// It exists because three subsystems each apply their own DDL - the money store, the Tower
// admission registry, and a standalone Tower's local store - and the reasoning below is
// subtle enough that three copies of it would eventually become three different behaviours.
//
// THE RACE. CREATE TABLE and CREATE INDEX with IF NOT EXISTS are NOT atomic against a
// concurrent CREATE. Two instances starting at the same moment can both find an object
// absent and both try to create it; the loser gets a unique-violation on a system catalog
// (pg_type, pg_class, pg_namespace). That is not a real failure - the object exists by the
// time the loser sees the error - so one retry settles it.
//
// A rolling deploy that starts two pods together is exactly this situation, and it is the
// worst possible moment for a broker to refuse to start.
//
// WHAT THIS DELIBERATELY DOES NOT DO. It does not retry forever, and it does not swallow
// the error. A second failure is returned as-is, because the failures that are NOT this
// race - a permission problem, a missing schema, a genuinely bad migration - must reach the
// operator rather than being retried into silence. One retry distinguishes "somebody beat
// me to it" from "this cannot work", and nothing more.
package pgmigrate

import "database/sql"

// Execer is the subset of *sql.DB a migration needs, so a caller holding a transaction or
// a wrapper can use this too.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Apply runs the DDL, retrying once if the first attempt fails.
//
// The retry is unconditional rather than matched on a SQLSTATE. Matching would mean
// enumerating which catalog a given PostgreSQL version happens to collide on, which changes
// between versions and between DDL statements - and getting that list wrong fails a deploy
// for a reason nobody would look for. A single blind retry of an idempotent migration is
// safe by construction: every statement is IF NOT EXISTS, so running it twice does nothing
// the first run did not already do.
func Apply(db Execer, ddl string) error {
	if _, err := db.Exec(ddl); err != nil {
		if _, retry := db.Exec(ddl); retry != nil {
			return retry
		}
	}
	return nil
}
