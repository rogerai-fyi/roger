package store

// mint_key_rules_test.go: CreateAccountKey enforces the per-account mint rules INSIDE its own
// write, on BOTH backends (parityStores), so brokers on several instances racing a mint for one
// account can neither pass the live-key cap nor mint twice for one Idempotency-Key (state audit
// 2026-10-05). Mint order stays strict per account (newest first never ties).

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mintRace(t *testing.T, db Store, acct, idem string, n int, rules MintKeyRules) (ok []AccountKey, errs []error) {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	race := time.Now().UnixNano() // ids are unique per race, so no mint overwrites another
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			k, err := db.CreateAccountKey(AccountKey{
				ID: fmt.Sprintf("key_%s_%s_%d_%d", acct, idem, race, i), SecretHash: fmt.Sprintf("h_%s_%s_%d_%d", acct, idem, race, i),
				Account: acct, IdemKey: idem, CreatedAt: time.Now().UnixNano(), Reset: "none",
			}, rules)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ok = append(ok, k)
		}(i)
	}
	close(start)
	wg.Wait()
	return ok, errs
}

func TestCreateAccountKeyMintRulesParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			acct := "u_mint_" + name + "_" + fmt.Sprint(time.Now().UnixNano())

			// The cap: 8 racing mints against a cap of 3 leave exactly 3 live keys.
			ok, errs := mintRace(t, db, acct, "", 8, MintKeyRules{MaxLive: 3})
			require.Len(t, ok, 3)
			require.Len(t, errs, 5)
			for _, err := range errs {
				require.ErrorIs(t, err, ErrKeyCount)
			}
			keys, err := db.AccountKeysOf(acct)
			require.NoError(t, err)
			require.Len(t, keys, 3)

			// A revoked key frees its slot.
			keys[0].Revoked = true
			require.NoError(t, db.SaveAccountKey(keys[0]))
			ok, _ = mintRace(t, db, acct, "", 2, MintKeyRules{MaxLive: 3})
			require.Len(t, ok, 1)

			// Mint order is strict per account: no two keys share a CreatedAt.
			keys, err = db.AccountKeysOf(acct)
			require.NoError(t, err)
			seen := map[int64]bool{}
			for _, k := range keys {
				require.False(t, seen[k.CreatedAt], "two keys share CreatedAt %d", k.CreatedAt)
				seen[k.CreatedAt] = true
			}

			// The replay: 8 racing mints with one Idempotency-Key mint once; the rest name it.
			acct2 := acct + "_idem"
			since := time.Now().Add(-time.Minute).UnixNano()
			ok, errs = mintRace(t, db, acct2, "abc", 8, MintKeyRules{MaxLive: 32, IdemSince: since})
			require.Len(t, ok, 1)
			require.Len(t, errs, 7)
			for _, err := range errs {
				var rep *KeyReplayError
				require.True(t, errors.As(err, &rep), "want a KeyReplayError, got %v", err)
				require.Equal(t, ok[0].ID, rep.ID)
			}
			// The replay check binds before the cap: an account at its cap still gets the 409.
			_, err = db.CreateAccountKey(AccountKey{ID: "key_" + acct2 + "_late", SecretHash: "h_" + acct2 + "_late", Account: acct2, IdemKey: "abc", Reset: "none"}, MintKeyRules{MaxLive: 1, IdemSince: since})
			var rep *KeyReplayError
			require.True(t, errors.As(err, &rep), "want a KeyReplayError, got %v", err)

			// A key minted with the same Idempotency-Key before the window is not a replay.
			ok, errs = mintRace(t, db, acct2, "abc", 1, MintKeyRules{MaxLive: 32, IdemSince: time.Now().Add(time.Minute).UnixNano()})
			require.Len(t, ok, 1, "errs: %v", errs)

			// No rules: no cap, no replay check.
			_, err = db.CreateAccountKey(AccountKey{ID: "key_" + acct2 + "_free", SecretHash: "h_" + acct2 + "_free", Account: acct2, IdemKey: "abc", Reset: "none"}, MintKeyRules{})
			require.NoError(t, err)
		})
	}
}
