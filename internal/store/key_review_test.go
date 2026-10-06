package store

// key_review_test.go: the slice-5 review fixes at the store, on BOTH backends (parityStores).
// An edit re-reads the key under its own lock, so it never writes back a revoked or disabled
// flag read earlier; a key-side reversal is dated in the window its spend was, and a request
// is reversed on its key at most once in total.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpdateAccountKeyParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			acct := fmt.Sprintf("u_upd_%s_%d", name, time.Now().UnixNano())
			mk := func(id string) AccountKey {
				k, err := db.CreateAccountKey(AccountKey{ID: id, SecretHash: "h_" + id, Account: acct, Reset: "none", CreatedAt: time.Now().UnixNano()}, MintKeyRules{})
				require.NoError(t, err)
				return k
			}
			k1 := mk(acct + "_k1")
			got, ok, err := db.UpdateAccountKey(k1.ID, func(k *AccountKey) { k.Name = "x" })
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "x", got.Name)

			// An edit after the account's keys were retired finds nothing and writes nothing.
			require.NoError(t, db.RetireAccountKeys(acct, "anon_"+acct))
			_, ok, err = db.UpdateAccountKey(k1.ID, func(k *AccountKey) { k.Name = "y" })
			require.NoError(t, err)
			require.False(t, ok, "an edit must never touch a revoked key")
			rec, _, err := db.AccountKeyByID(k1.ID)
			require.NoError(t, err)
			require.True(t, rec.Revoked)
			require.Equal(t, "x", rec.Name)

			// An edit that does not name disabled keeps the live value, not one read earlier.
			acct = acct + "_b"
			k2 := mk(acct + "_k2")
			stale := k2 // what a slow edit read before another edit disabled the key
			_, ok, err = db.UpdateAccountKey(k2.ID, func(k *AccountKey) { k.Disabled = true })
			require.NoError(t, err)
			require.True(t, ok)
			_, ok, err = db.UpdateAccountKey(stale.ID, func(k *AccountKey) { k.Name = "renamed" })
			require.NoError(t, err)
			require.True(t, ok)
			rec, _, err = db.AccountKeyByID(k2.ID)
			require.NoError(t, err)
			require.True(t, rec.Disabled)
			require.Equal(t, "renamed", rec.Name)

			_, ok, err = db.UpdateAccountKey("key_missing_"+acct, func(k *AccountKey) { k.Name = "z" })
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

// reviewKeySpendRow writes a key spend row as a capture does; reviewKeyReverse runs the
// key-side reversal a chargeback or refund runs.
func reviewKeySpendRow(t *testing.T, db Store, keyID, ref string, cost float64, ts int64) {
	t.Helper()
	switch d := db.(type) {
	case *Mem:
		d.mu.Lock()
		d.appendLedgerLocked(keyID, "key", KindKeySpend, -cost, "", StatePosted, ref, ts)
		d.mu.Unlock()
	case *Postgres:
		tx, err := d.db.Begin()
		require.NoError(t, err)
		require.NoError(t, keySpendCaptureTx(tx, keyID, ts, ref, cost))
		require.NoError(t, tx.Commit())
	default:
		t.Fatalf("unknown store %T", db)
	}
}

func reviewKeyReverse(t *testing.T, db Store, requestID string, amount float64) {
	t.Helper()
	switch d := db.(type) {
	case *Mem:
		d.mu.Lock()
		d.keyReverseLocked(requestID, amount)
		d.mu.Unlock()
	case *Postgres:
		tx, err := d.db.Begin()
		require.NoError(t, err)
		require.NoError(t, keyReverseTx(tx, requestID, amount))
		require.NoError(t, tx.Commit())
	}
}

func TestKeyReversalDatedAtSpendAndCappedParity(t *testing.T) {
	day := int64(24 * time.Hour / time.Millisecond)
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			n := time.Now().UnixNano()
			key, req := fmt.Sprintf("key_rev_%s_%d", name, n), fmt.Sprintf("req_rev_%s_%d", name, n)
			monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
			reviewKeySpendRow(t, db, key, req, 5, monday+3600_000)

			// A chargeback that lands on Tuesday nets out of Monday, the window the money was spent in.
			reviewKeyReverse(t, db, req, 5)
			mon, err := db.KeySpend(key, monday, monday+day)
			require.NoError(t, err)
			require.InDelta(t, 0, mon, 1e-9)
			tue, err := db.KeySpend(key, monday+day, 0)
			require.NoError(t, err)
			require.InDelta(t, 0, tue, 1e-9, "a later window must not gain budget from an earlier spend's reversal")

			// A second reversal of the same request (a refund, then a chargeback) reverses nothing more.
			reviewKeyReverse(t, db, req, 5)
			life, err := db.KeySpend(key, 0, 0)
			require.NoError(t, err)
			require.InDelta(t, 0, life, 1e-9, "a request is reversed on its key at most once in total")

			// Partial reversals add up to at most the spend.
			req2 := req + "_b"
			reviewKeySpendRow(t, db, key, req2, 3, monday+7200_000)
			reviewKeyReverse(t, db, req2, 1)
			reviewKeyReverse(t, db, req2, 5)
			life, err = db.KeySpend(key, 0, 0)
			require.NoError(t, err)
			require.InDelta(t, 0, life, 1e-9)
		})
	}
}

func TestKeySpendSinceAndRefsParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			n := time.Now().UnixNano()
			acct := fmt.Sprintf("u_sums_%s_%d", name, n)
			for _, id := range []string{acct + "_a", acct + "_b"} {
				_, err := db.CreateAccountKey(AccountKey{ID: id, SecretHash: "h_" + id, Account: acct, Reset: "none", CreatedAt: time.Now().UnixNano()}, MintKeyRules{})
				require.NoError(t, err)
			}
			ka, kb := acct+"_a", acct+"_b"
			reviewKeySpendRow(t, db, ka, "r1_"+acct, 1, 1000)
			reviewKeySpendRow(t, db, ka, "r2_"+acct, 2, 2000)
			reviewKeySpendRow(t, db, kb, "r3_"+acct, 4, 3000)
			reviewKeyReverse(t, db, "r2_"+acct, 0.5)

			got, err := db.KeySpendSince(ka, []int64{0, 1500, 2500})
			require.NoError(t, err)
			require.InDeltaSlice(t, []float64{2.5, 1.5, 0}, got, 1e-9)
			for i, f := range []int64{0, 1500, 2500} {
				one, err := db.KeySpend(ka, f, 0)
				require.NoError(t, err)
				require.InDelta(t, one, got[i], 1e-9, "KeySpendSince must agree with KeySpend")
			}
			none, err := db.KeySpendSince(ka, nil)
			require.NoError(t, err)
			require.Empty(t, none)

			refs, err := db.AccountKeySpendRefs(acct)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"r1_" + acct: ka, "r2_" + acct: ka, "r3_" + acct: kb}, refs)
			other, err := db.AccountKeySpendRefs(acct + "_nobody")
			require.NoError(t, err)
			require.Empty(t, other)
		})
	}
}
