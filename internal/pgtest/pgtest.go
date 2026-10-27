// Package pgtest gives each package's tests a database of their own on the shared test
// Postgres server.
//
// `go test ./...` runs package test binaries in parallel, and the coverage gate (and CI)
// hands every one of them the SAME server through ROGERAI_TEST_DATABASE_URL. Sharing the
// database meant sharing tables, and that went wrong in two ways that both surfaced in a
// package that had done nothing wrong:
//
//   - one package's per-test TRUNCATE erased another package's fixtures mid-scenario;
//   - one package's schema migration (ALTER TABLE on one table, then CREATE INDEX on a
//     second, all in one implicit transaction) took the same two tables' locks in the
//     opposite order to another package's admission transaction, and Postgres killed the
//     admission with "deadlock detected" on a test that was not concurrent at all.
//
// A database per package makes both physically impossible while still running the real
// SQL against the real server. Production code never imports this package.
package pgtest

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver
)

// Env names the shared test server. Unset means "no Postgres": callers skip their durable
// half or fall back to the in-memory store, exactly as before.
const Env = "ROGERAI_TEST_DATABASE_URL"

// AllowRemoteEnv opts a run into a test server that is not on this machine. The helper creates
// (and on failure drops) databases, so by default it refuses anything but a local server.
const AllowRemoteEnv = "ROGERAI_TEST_DATABASE_ALLOW_REMOTE"

// refuseRemote returns an error for a server that is not local (loopback, localhost or a unix
// socket), unless AllowRemoteEnv is set. It reads the hosts pgx would dial, the URL's and any
// ?host= or fallback ones, and never connects.
func refuseRemote(shared string) error {
	if os.Getenv(AllowRemoteEnv) != "" {
		return nil
	}
	cfg, err := pgconn.ParseConfig(shared)
	if err != nil {
		return fmt.Errorf("%s is not a postgres URL: %w", Env, err)
	}
	hosts := []string{cfg.Host}
	for _, f := range cfg.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, h := range hosts {
		if !localHost(h) {
			return fmt.Errorf("%s names %s, not this machine: the test helper creates databases there; set %s=1 to allow it", Env, h, AllowRemoteEnv)
		}
	}
	return nil
}

// localHost reports a unix socket directory, localhost, or a loopback address.
func localHost(h string) bool {
	if strings.HasPrefix(h, "/") || h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// maxIdent is Postgres's identifier limit (NAMEDATALEN-1). A longer name is not an error to
// Postgres - it is silently TRUNCATED, and what it would cut is the random suffix, so two
// runs could land on one database again. Refuse instead.
const maxIdent = 63

var (
	mu sync.Mutex
	// created caches the private DSN per (shared server, package), so a binary provisions
	// its database once and every later test in it sees the same one.
	created = map[string]string{}
)

// DSN returns the calling package's private database, or "" when Env is unset. Failing to
// provision it fails the test: silently falling back to the shared database would bring
// back exactly the cross-package interference this exists to remove.
func DSN(t testing.TB) string {
	t.Helper()
	dsn, err := Private()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	return dsn
}

// Private is DSN for callers that hold no testing.TB, such as a godog scenario hook.
//
// The package is identified by the working directory: `go test` runs each test binary in
// its package's source directory, so the directory is unique per package and stable
// across runs.
func Private() (string, error) {
	shared := os.Getenv(Env)
	if shared == "" {
		return "", nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("identify the package: %w", err)
	}
	return privateFor(shared, dir)
}

// privateFor provisions (once per process) the database for package directory key on the
// server behind shared, and returns its DSN.
//
// The name is RANDOM per process, never derived from the package alone. A deterministic
// name would have to be dropped and recreated to start clean, and that drop kills any other
// run of the same package against the same server mid-test (it did, while reproducing the
// deadlock this package fixes). The cost is that a server which outlives the run keeps one
// small database per package per run. The coverage gate, CI and make test-db all discard
// the server afterwards; against one you keep, clear them out between runs with
//
//	psql "$ROGERAI_TEST_DATABASE_URL" -Atc "SELECT 'DROP DATABASE \"' || datname || '\";'
//	  FROM pg_database WHERE datname LIKE current_database() || '\_%'" | psql "$ROGERAI_TEST_DATABASE_URL"
func privateFor(shared, key string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	cacheKey := shared + "\x00" + key
	if dsn, ok := created[cacheKey]; ok {
		return dsn, nil
	}
	u, err := url.Parse(shared)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("%s must be a postgres:// URL that names a database", Env)
	}
	if err := refuseRemote(shared); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s_%s", strings.Trim(u.Path, "/"), label(filepath.Base(key)), strings.ToLower(rand.Text()[:8]))
	if len(name) > maxIdent {
		return "", fmt.Errorf("private database name %q exceeds %d bytes; use a shorter database name in %s", name, maxIdent, Env)
	}
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if err := run(shared, `CREATE DATABASE `+quoted); err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}
	u.Path = "/" + name
	private := u.String()
	// Production provisions the schema out of band; the test server's shared database gets
	// it from the gate script, so the private one has to be given it here.
	if err := run(private, `CREATE SCHEMA rogerai`); err != nil {
		// Never leave a half-made database behind; if even the drop fails, say so.
		if derr := run(shared, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); derr != nil {
			return "", fmt.Errorf("provision the rogerai schema in %s: %w (and dropping it failed: %v)", name, err, derr)
		}
		return "", fmt.Errorf("provision the rogerai schema in %s: %w", name, err)
	}
	created[cacheKey] = private
	return private, nil
}

// label turns a directory name into a short, lower-case identifier fragment. It is only
// for a human reading pg_database; the random suffix is what makes the name unique.
func label(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '_'
		}
	}
	if len(b) > 16 {
		b = b[:16]
	}
	return string(b)
}

// run executes one statement on a connection of its own to dsn.
func run(dsn, stmt string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(stmt)
	return err
}
