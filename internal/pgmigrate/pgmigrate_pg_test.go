package pgmigrate

// Against REAL PostgreSQL: several instances migrating one fresh database at the same moment,
// which is a rolling deploy that starts pods together. Skips without ROGERAI_TEST_DATABASE_URL.

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestInstancesMigratingTogetherAllStart(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the concurrent-migration test")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.Ping())

	const instances, tables, rounds = 6, 25, 5
	for round := 0; round < rounds; round++ {
		ns := fmt.Sprintf("pgmigrate_race_%d_%d", time.Now().UnixNano(), round)
		_, err := db.Exec(`CREATE SCHEMA ` + ns)
		require.NoError(t, err)
		t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + ns + ` CASCADE`) }) //nolint:errcheck

		var ddl strings.Builder
		for i := 0; i < tables; i++ {
			fmt.Fprintf(&ddl, "CREATE TABLE IF NOT EXISTS %s.t%d (id TEXT PRIMARY KEY, v INT);\n", ns, i)
			fmt.Fprintf(&ddl, "CREATE INDEX IF NOT EXISTS t%d_v ON %s.t%d (v);\n", i, ns, i)
		}

		start := make(chan struct{})
		errs := make([]error, instances)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = Apply(db, ddl.String())
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "round %d: instance %d failed to start", round, i)
		}
	}
}
