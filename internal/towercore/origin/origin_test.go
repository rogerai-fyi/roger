package origin

// origin_test.go runs the tally through BOTH stores (mem + Postgres when a DSN is set) so a
// count recorded on one deployment reads the same on the other. It asserts the privacy and
// correctness invariants the detail view depends on: country-only, attempt-idempotent,
// window-filtered, no-header-as-unknown, and CF's code taken verbatim.

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

var privateOnce sync.Once

func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	name := "roger_test_origin"
	if u, err := url.Parse(dsn); err == nil && u.Path != "" && u.Path != "/" {
		name = strings.TrimPrefix(u.Path, "/") + "_origin"
		privateOnce.Do(func() {
			admin, aerr := sql.Open("pgx", dsn)
			require.NoError(t, aerr)
			defer admin.Close()
			if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
				!strings.Contains(cerr.Error(), "already exists") {
				t.Fatalf("create %s: %v", name, cerr)
			}
		})
		u.Path = "/" + name
		dsn = u.String()
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_origin_seen, rogerai.tower_origin_events`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func TestOriginByTower(t *testing.T) {
	now := time.Now().UTC()
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			data := []struct{ tower, attempt, country string }{
				{"tw-A", "a1", "US"}, {"tw-A", "a2", "US"}, {"tw-A", "a3", "DE"},
				{"tw-A", "a4", "US"}, {"tw-A", "a5", "BR"},
				{"tw-B", "b1", "US"}, // isolation
			}
			for _, d := range data {
				require.NoError(t, st.Record(d.tower, d.attempt, d.country, now))
			}

			got, err := st.ByTower("tw-A", time.Time{})
			require.NoError(t, err)
			require.Equal(t, []Tally{
				{Country: "BR", Attempts: 1},
				{Country: "DE", Attempts: 1},
				{Country: "US", Attempts: 3},
			}, got, "sorted by country, isolated to tw-A")

			// Isolation: tw-B unaffected.
			b, err := st.ByTower("tw-B", time.Time{})
			require.NoError(t, err)
			require.Equal(t, []Tally{{Country: "US", Attempts: 1}}, b)

			// Unknown Tower is empty, not an error.
			none, err := st.ByTower("tw-nope", time.Time{})
			require.NoError(t, err)
			require.Empty(t, none)
		})
	}
}

func TestOriginIdempotentPerAttempt(t *testing.T) {
	now := time.Now().UTC()
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			require.NoError(t, st.Record("tw-I", "same", "US", now))
			require.NoError(t, st.Record("tw-I", "same", "US", now)) // retried open
			require.NoError(t, st.Record("tw-I", "same", "DE", now)) // even a different country: still one attempt
			got, err := st.ByTower("tw-I", time.Time{})
			require.NoError(t, err)
			require.Equal(t, []Tally{{Country: "US", Attempts: 1}}, got, "an attempt counts once, at its first country")
		})
	}
}

func TestOriginNoHeaderIsUnknown(t *testing.T) {
	now := time.Now().UTC()
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			require.NoError(t, st.Record("tw-U", "u1", "", now))
			require.NoError(t, st.Record("tw-U", "u2", "  ", now)) // whitespace-only is also no header
			require.NoError(t, st.Record("tw-U", "u3", "us", now)) // lower-case normalizes up
			got, err := st.ByTower("tw-U", time.Time{})
			require.NoError(t, err)
			require.Equal(t, []Tally{
				{Country: "US", Attempts: 1},
				{Country: Unknown, Attempts: 2},
			}, got)
		})
	}
}

func TestOriginWindowFilters(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			require.NoError(t, st.Record("tw-W", "old", "US", old))
			require.NoError(t, st.Record("tw-W", "new", "US", now))
			all, err := st.ByTower("tw-W", time.Time{})
			require.NoError(t, err)
			require.Equal(t, []Tally{{Country: "US", Attempts: 2}}, all)
			recent, err := st.ByTower("tw-W", now.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, []Tally{{Country: "US", Attempts: 1}}, recent, "window excludes the old attempt")
		})
	}
}

func TestOriginEmptyIdsAreNoOps(t *testing.T) {
	now := time.Now().UTC()
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			require.NoError(t, st.Record("", "a", "US", now))
			require.NoError(t, st.Record("tw-X", "", "US", now))
			got, err := st.ByTower("tw-X", time.Time{})
			require.NoError(t, err)
			require.Empty(t, got, "nothing to attribute without both ids")
		})
	}
}

func TestNewPGStoreRejectsNilDB(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err, "a durable tally needs a database handle")
}

// A store whose database has gone away must return the error, not swallow it: an origin
// write that silently fails would under-count demand without a trace.
func TestOriginPGSurfacesDBErrors(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL; the durable error paths need a real handle")
	}
	if u, err := url.Parse(dsn); err == nil && u.Path != "" && u.Path != "/" {
		u.Path = "/" + strings.TrimPrefix(u.Path, "/") + "_origin"
		dsn = u.String()
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	st, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close()) // pull the handle out from under it

	require.Error(t, st.Record("tw", "att", "US", time.Now()), "a dead handle fails the write")
	_, err = st.ByTower("tw", time.Time{})
	require.Error(t, err, "a dead handle fails the read")
}
