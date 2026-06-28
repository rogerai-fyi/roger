package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// poisonedTx returns an OPEN transaction that Postgres has already ABORTED (a prior
// statement referenced a missing relation), so every subsequent statement on it fails with
// "current transaction is aborted, commands ignored until end of transaction block" — a
// REAL in-transaction DB error, no driver mock. It drives the first-statement error branch
// of the in-tx money helpers that the happy-path settles never exercise.
func poisonedTx(t *testing.T, pg *Postgres) *sql.Tx {
	t.Helper()
	tx, err := pg.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`SELECT 1 FROM rogerai.a_relation_that_does_not_exist`); err == nil {
		t.Fatal("expected the poisoning statement to fail so the tx is aborted")
	}
	return tx
}

// TestPostgresInTxHelperErrorPaths: each in-transaction money helper must propagate a DB
// error from its first statement (here: a poisoned/aborted tx) rather than swallow it —
// otherwise a settle could mint/skip money on a half-failed transaction. ownerShare/seed/
// cost are non-zero so each helper reaches its DB statement (past the cheap zero guards).
func TestPostgresInTxHelperErrorPaths(t *testing.T) {
	pg := pgOnly(t)
	now := time.Now()

	t.Run("addLot", func(t *testing.T) {
		if err := pg.addLot(poisonedTx(t, pg), "n", "req", 5, now); err == nil {
			t.Fatal("addLot on an aborted tx must surface the DB error")
		}
	})
	t.Run("realEarnShareTx", func(t *testing.T) {
		if _, err := pg.realEarnShareTx(poisonedTx(t, pg), "u", 10, 5); err == nil {
			t.Fatal("realEarnShareTx on an aborted tx must surface the DB error")
		}
	})
	t.Run("grantSeedTx", func(t *testing.T) {
		if _, err := pg.grantSeedTx(poisonedTx(t, pg), "u", 25); err == nil {
			t.Fatal("grantSeedTx on an aborted tx must surface the DB error")
		}
	})
	t.Run("claimReceipt", func(t *testing.T) {
		rec := protocol.UsageReceipt{RequestID: "req-x", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}
		if _, _, err := pg.claimReceipt(poisonedTx(t, pg), "u", "n", 10, rec); err == nil {
			t.Fatal("claimReceipt on an aborted tx must surface the DB error")
		}
	})
}
