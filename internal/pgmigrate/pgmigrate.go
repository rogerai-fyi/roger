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
//
// ONE STATEMENT PER TRANSACTION. A multi-statement string sent in one Exec runs as ONE
// implicit transaction, and every lock its DDL takes (ALTER TABLE takes the strongest there
// is, even when IF NOT EXISTS makes it a no-op) is held until the end. So a migration that
// locked table A and then wanted table B, while a live request held B and wanted A,
// deadlocked: a rolling deploy starting one instance while another served traffic was
// enough. Running each statement on its own means the migration never holds a lock on one
// table while it waits for another, so it cannot be one half of a lock cycle whatever order
// the request paths take their tables in. Every statement is idempotent, so losing the
// all-or-nothing of one transaction costs nothing: a migration interrupted part-way is
// finished by the next start.
package pgmigrate

import (
	"database/sql"
	"strings"
)

// Execer is the subset of *sql.DB a migration needs, so a caller holding a transaction or
// a wrapper can use this too.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Apply runs the DDL one statement at a time, retrying a failed statement once.
//
// The retry is unconditional rather than matched on a SQLSTATE. Matching would mean
// enumerating which catalog a given PostgreSQL version happens to collide on, which changes
// between versions and between DDL statements - and getting that list wrong fails a deploy
// for a reason nobody would look for. A single blind retry of an idempotent migration is
// safe by construction: every statement is IF NOT EXISTS, so running it twice does nothing
// the first run did not already do.
//
// The retry is per statement. Two instances starting together walk the same list side by
// side, so the loser can lose the race on one statement and then on a later one as well;
// each loss means that object now exists, so each statement gets its own single retry.
// Retrying the whole script once instead would let the second loss fail the start.
func Apply(db Execer, ddl string) error {
	for _, stmt := range splitStatements(ddl) {
		if _, err := db.Exec(stmt); err != nil {
			if _, retry := db.Exec(stmt); retry != nil {
				return retry
			}
		}
	}
	return nil
}

// splitStatements cuts a script at the semicolons that end statements, skipping those inside
// comments, string literals, quoted identifiers and dollar-quoted bodies. Pieces holding only
// whitespace and comments are dropped. A comment between two statements travels with the
// one after it.
func splitStatements(ddl string) []string {
	var out []string
	start, hasCode := 0, false
	emit := func(end int) {
		if hasCode {
			out = append(out, strings.TrimSpace(ddl[start:end]))
		}
		start, hasCode = end+1, false
	}
	// skipTo returns the index just past the next occurrence of close at or after i, or the
	// end of the input when there is none.
	skipTo := func(i int, close string) int {
		if j := strings.Index(ddl[i:], close); j >= 0 {
			return i + j + len(close)
		}
		return len(ddl)
	}
	for i := 0; i < len(ddl); {
		c := ddl[i]
		switch {
		case c == '-' && strings.HasPrefix(ddl[i:], "--"):
			i = skipTo(i, "\n")
		case c == '/' && strings.HasPrefix(ddl[i:], "/*"):
			i = skipTo(i+2, "*/")
		case c == '\'' || c == '"':
			// A doubled quote inside closes and reopens, which lands in the same place.
			i = skipTo(i+1, string(c))
			hasCode = true
		case c == '$' && (i == 0 || !isIdent(ddl[i-1])):
			if tag, ok := dollarTag(ddl[i:]); ok {
				i = skipTo(i+len(tag), tag)
			} else {
				i++
			}
			hasCode = true
		case c == ';':
			emit(i)
			i++
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				hasCode = true
			}
			i++
		}
	}
	if start < len(ddl) {
		emit(len(ddl))
	}
	return out
}

// dollarTag reports the opening dollar quote at the start of s ("$$" or "$name$"), if any.
// "$1" is a parameter, not a quote: a tag may not start with a digit.
func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		switch {
		case s[j] == '$':
			return s[:j+1], true
		case isIdent(s[j]) && !(j == 1 && s[j] >= '0' && s[j] <= '9'):
		default:
			return "", false
		}
	}
	return "", false
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}
