package attempt

// parity_test.go runs the chain's guarantees through BOTH stores and requires the same
// answers.
//
// This is the record money is decided from. "Exactly one writer advances the chain" and
// "exact replay is idempotent" have to mean the same thing in memory and in Postgres, or
// everything written on top of them is true only on a single broker.

import (
	"crypto/ed25519"
	"crypto/rand"
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

func privateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_attempt"
	privateOnce.Do(func() {
		admin, aerr := sql.Open("pgx", dsn)
		if aerr != nil {
			t.Fatalf("private db: open admin: %v", aerr)
		}
		defer admin.Close()
		if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
			!strings.Contains(cerr.Error(), "already exists") {
			t.Fatalf("private db: create %s: %v", name, cerr)
		}
	})
	u.Path = "/" + name
	return u.String()
}

func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.attempt_events`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func each(t *testing.T, fn func(t *testing.T, l *Ledger)) {
	t.Helper()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			var seq int64Counter
			fn(t, New(Config{
				Network: network, Signer: priv,
				Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
				Sequence: seq.next,
			}, s))
		})
	}
}

// THE CHAIN ADVANCES ONE REVISION AT A TIME, wherever it is kept.
func TestParityTheChainAdvances(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, issued, err := l.Issue(joinedSpec("att-chain"))
		require.NoError(t, err)
		require.Equal(t, int64(1), issued.Revision)

		leased, err := l.Commit("att-chain", Observation{
			Kind: KindDispatchAccepted, EvidenceHash: "lease",
		})
		require.NoError(t, err)
		require.Equal(t, int64(2), leased.Revision)
		require.Equal(t, StateLeased, leased.State)

		state, rev, ok, err := l.State("att-chain")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, StateLeased, state)
		require.Equal(t, int64(2), rev)

		// And the recorded authority survives the round trip, or a successor would restate
		// this attempt's money differently from its own first event.
		head, ok, err := l.store.Head("att-chain")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "hold-1", head.Spec.Hold.ID)
		require.Equal(t, int64(1500), head.Spec.Hold.Amount)
		require.Equal(t, "grant-hash", head.Spec.GrantHash)
	})
}

// EXACTLY ONE WRITER ADVANCES IT. This is the guarantee the whole ledger exists for, and it
// has to hold in the database as well as in a map.
func TestParityConcurrentWritersAdvanceItOnce(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, _, err := l.Issue(joinedSpec("att-race"))
		require.NoError(t, err)

		var wg sync.WaitGroup
		won := make(chan struct{}, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, cerr := l.Commit("att-race", Observation{
					Kind: KindDispatchAccepted, EvidenceHash: "lease",
				}); cerr == nil {
					won <- struct{}{}
				}
			}()
		}
		wg.Wait()
		close(won)
		require.Len(t, won, 1, "exactly one writer may advance the chain")

		_, rev, _, err := l.State("att-race")
		require.NoError(t, err)
		require.Equal(t, int64(2), rev, "and the chain has exactly one new link")
	})
}

// An attempt is issued ONCE, however many callers try.
func TestParityAnAttemptIsIssuedOnce(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, _, err := l.Issue(joinedSpec("att-once"))
		require.NoError(t, err)
		_, _, err = l.Issue(joinedSpec("att-once"))
		require.ErrorIs(t, err, ErrAlreadyIssued)

		_, rev, _, err := l.State("att-once")
		require.NoError(t, err)
		require.Equal(t, int64(1), rev)
	})
}

// Exact replay is idempotent; different bytes at a taken revision are a conflict; a skipped
// revision fails before anything moves.
func TestParityReplayConflictAndSkip(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, _, err := l.Issue(joinedSpec("att-replay"))
		require.NoError(t, err)
		leased, err := l.Commit("att-replay", Observation{
			Kind: KindDispatchAccepted, EvidenceHash: "lease",
		})
		require.NoError(t, err)

		at2, ok, err := l.store.At("att-replay", 2)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, leased.Hash, at2.Hash)

		require.NoError(t, l.store.Append(at2, 1), "the same event again is the same fact")

		conflicting := at2
		conflicting.EventID = "different-id"
		conflicting.Hash = "different-hash"
		require.ErrorIs(t, l.store.Append(conflicting, 1), ErrConflict)

		skipped := at2
		skipped.Revision = 7
		skipped.EventID = "skipped-id"
		skipped.Hash = "skipped-hash"
		require.ErrorIs(t, l.store.Append(skipped, 6), ErrRevision)

		// Nothing moved.
		_, rev, _, err := l.State("att-replay")
		require.NoError(t, err)
		require.Equal(t, int64(2), rev)
	})
}

// A terminal attempt is not revivable, in either store.
func TestParityATerminalAttemptStaysTerminal(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, _, err := l.Issue(joinedSpec("att-term"))
		require.NoError(t, err)
		_, err = l.Commit("att-term", Observation{
			Kind: KindDispatchFailed, EvidenceHash: "e", Reason: "dispatch failed",
			ReleaseID: "rel-1", ReleaseIndex: 1,
		})
		require.NoError(t, err)

		_, err = l.Commit("att-term", Observation{Kind: KindDispatchAccepted, EvidenceHash: "e"})
		require.ErrorIs(t, err, ErrTerminal)

		state, rev, _, err := l.State("att-term")
		require.NoError(t, err)
		require.Equal(t, StateFailed, state)
		require.Equal(t, int64(2), rev)
	})
}

// A successor for an attempt that was never issued creates nothing: a chain with no
// beginning is not a chain.
func TestParityASuccessorNeedsAnAttempt(t *testing.T) {
	each(t, func(t *testing.T, l *Ledger) {
		_, err := l.Commit("att-nobody", Observation{
			Kind: KindDispatchAccepted, EvidenceHash: "e",
		})
		require.ErrorIs(t, err, ErrNotFound)

		require.ErrorIs(t, l.store.Append(Record{
			AttemptID: "att-nobody", Revision: 2, EventID: "e", Hash: "h",
		}, 1), ErrNotFound)
	})
}

func TestADurableLedgerNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database handle")
}

// A database that has gone away is reported, never read as "no such attempt" - which would
// let a second broker issue an attempt that already exists.
func TestADeadDatabaseIsReportedRatherThanReadAsAbsent(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ROGERAI_TEST_DATABASE_URL to exercise the durable ledger")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	require.Error(t, s.Append(Record{AttemptID: "att-1", Revision: 1, Hash: "h"}, 0))
	_, _, err = s.Head("att-1")
	require.Error(t, err)
	_, _, err = s.At("att-1", 1)
	require.Error(t, err)
}

// int64Counter is a concurrency-safe sequencer, which Config.Sequence requires.
type int64Counter struct {
	mu sync.Mutex
	n  int64
}

func (c *int64Counter) next() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}
