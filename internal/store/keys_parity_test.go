package store

// keys_parity_test.go: the account-key store surface (features/auth/key_guardrails.feature) on
// BOTH backends - the in-memory store and a real Postgres (skipped without
// ROGERAI_TEST_DATABASE_URL; cover-gate provisions one). The broker suites drive these through
// the relay; this pins the store contract itself: lookups, saves of unknown ids, the limit
// enforced inside the hold, capture into key spend, the window bounds, reversal on a
// chargeback, audit rows and retirement. Every assertion re-reads from the store.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestAccountKeyExpired(t *testing.T) {
	now := time.Unix(1_000, 0)
	for _, tc := range []struct {
		exp  int64
		want bool
	}{
		{0, false},     // never expires
		{999, true},    // past
		{1_000, true},  // exactly at the expiry is expired
		{1_001, false}, // future
	} {
		require.Equal(t, tc.want, AccountKey{ExpiresAt: tc.exp}.Expired(now), "expires_at=%d", tc.exp)
	}
}

func TestAccountKeysParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			const acct, other = "u_keys_owner", "u_keys_other"
			k := AccountKey{ID: "key_a", SecretHash: "hash-a", Account: acct, Name: "ci", Hint: "...abcd",
				LimitUSD: 1, Reset: "monthly", AllowedModels: []string{"m1"}, AllowedNodes: []string{"n1"},
				OwnerPub: "pub-a", CreatedAt: 1}
			_, err := s.CreateAccountKey(k, MintKeyRules{})
			require.NoError(t, err)
			_, err = s.CreateAccountKey(AccountKey{ID: "key_b", SecretHash: "hash-b", Account: acct, Name: "b", Reset: "none", CreatedAt: 2}, MintKeyRules{})
			require.NoError(t, err)
			_, err = s.CreateAccountKey(AccountKey{ID: "key_o", SecretHash: "hash-o", Account: other, Name: "o", Reset: "none", CreatedAt: 3}, MintKeyRules{})
			require.NoError(t, err)

			got, ok, err := s.AccountKeyByHash("hash-a")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "key_a", got.ID)
			require.Equal(t, []string{"m1"}, got.AllowedModels)
			require.Equal(t, []string{"n1"}, got.AllowedNodes)
			_, ok, err = s.AccountKeyByHash("nope")
			require.NoError(t, err)
			require.False(t, ok, "an unknown hash is not found")
			got, ok, err = s.AccountKeyByID("key_b")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "b", got.Name)
			_, ok, err = s.AccountKeyByID("key_missing")
			require.NoError(t, err)
			require.False(t, ok)

			mine, err := s.AccountKeysOf(acct)
			require.NoError(t, err)
			require.Len(t, mine, 2, "only the account's own keys")
			none, err := s.AccountKeysOf("u_nobody")
			require.NoError(t, err)
			require.Empty(t, none)

			// Update edits an existing key; updating an unknown id creates nothing.
			_, found, err := s.UpdateAccountKey("key_a", func(x *AccountKey) { x.Name, x.Disabled, x.LimitUSD = "renamed", true, 2 })
			require.NoError(t, err)
			require.True(t, found)
			_, found, err = s.UpdateAccountKey("key_ghost", func(x *AccountKey) { x.Name = "ghost" })
			require.NoError(t, err)
			require.False(t, found)
			got, _, _ = s.AccountKeyByID("key_a")
			require.Equal(t, "renamed", got.Name)
			require.True(t, got.Disabled)
			require.InDelta(t, 2, got.LimitUSD, 1e-9)
			_, ok, _ = s.AccountKeyByID("key_ghost")
			require.False(t, ok, "an update never creates a key")

			// Touch: last-used always, the request count only for a request.
			require.NoError(t, s.TouchAccountKey("key_a", 100, true))
			require.NoError(t, s.TouchAccountKey("key_a", 200, false))
			require.NoError(t, s.TouchAccountKey("key_missing", 300, true))
			got, _, _ = s.AccountKeyByID("key_a")
			require.EqualValues(t, 200, got.LastUsed)
			require.EqualValues(t, 1, got.Requests)

			// The limit is enforced inside the hold: spend + reserved + the new hold <= limit.
			_, err = s.AddCredits(acct, 10)
			require.NoError(t, err)
			nowMs := time.Now().UnixMilli()
			lim := KeyLimit{USD: 1, From: nowMs - 60_000}
			placed, err := s.HoldForKey(acct, "req-1", 0.6, "key_a", nowMs, lim)
			require.NoError(t, err)
			require.True(t, placed)
			res, err := s.KeyReserved("key_a")
			require.NoError(t, err)
			require.InDelta(t, 0.6, res, 1e-9)
			placed, err = s.HoldForKey(acct, "req-2", 0.5, "key_a", nowMs, lim)
			require.ErrorIs(t, err, ErrKeyLimit, "0.6 reserved + 0.5 > the $1 limit")
			require.False(t, placed)
			res, _ = s.KeyReserved("key_a")
			require.InDelta(t, 0.6, res, 1e-9, "a refused hold reserves nothing")
			// No limit: only the wallet bounds it; a wallet that cannot cover it is refused.
			placed, err = s.HoldForKey(acct, "req-big", 1_000, "key_b", nowMs, KeyLimit{})
			require.NoError(t, err)
			require.False(t, placed, "the wallet cannot cover the hold")

			// Capture: the settled cost becomes key spend dated at the hold's keyTS.
			_, err = s.Finalize(acct, "n1", 0.6, 0.4, 0.36, protocol.UsageReceipt{RequestID: "req-1", TS: time.Now().Unix()})
			require.NoError(t, err)
			res, _ = s.KeyReserved("key_a")
			require.InDelta(t, 0, res, 1e-9, "the captured hold no longer reserves")
			spend, err := s.KeySpend("key_a", nowMs-1, 0)
			require.NoError(t, err)
			require.InDelta(t, 0.4, spend, 1e-9)
			spend, _ = s.KeySpend("key_a", nowMs+1, 0)
			require.InDelta(t, 0, spend, 1e-9, "a window starting after the spend excludes it")
			spend, _ = s.KeySpend("key_a", 0, nowMs)
			require.InDelta(t, 0, spend, 1e-9, "the window's end is exclusive")
			spend, _ = s.KeySpend("key_b", 0, 0)
			require.InDelta(t, 0, spend, 1e-9, "another key's spend is its own")
			refs, err := s.AccountKeySpendRefs(acct)
			require.NoError(t, err)
			require.Equal(t, "key_a", refs["req-1"], "the settled request is attributed to the key that made it")

			// With 0.4 spent, a 0.7 hold now exceeds the $1 limit; 0.5 fits.
			_, err = s.HoldForKey(acct, "req-3", 0.7, "key_a", nowMs, lim)
			require.ErrorIs(t, err, ErrKeyLimit)
			placed, err = s.HoldForKey(acct, "req-3", 0.5, "key_a", nowMs, lim)
			require.NoError(t, err)
			require.True(t, placed)

			// A chargeback of the request reverses its key spend (never more than was spent).
			_, err = s.ChargebackLineage("dp_keys_1", acct, "req-1", 5, time.Now())
			require.NoError(t, err)
			spend, _ = s.KeySpend("key_a", 0, 0)
			require.InDelta(t, 0, spend, 1e-9, "the reversal nets the key's spend to zero")

			// Audit rows and retirement.
			require.NoError(t, s.AppendKeyEvent(acct, "key_a:created", time.Now().Unix()))
			require.NoError(t, s.RetireAccountKeys(acct, "anon_x"))
			mine, _ = s.AccountKeysOf(acct)
			require.Len(t, mine, 2, "retired keys are kept, revoked")
			for _, k := range mine {
				require.True(t, k.Revoked, "%s revoked", k.ID)
				require.Empty(t, k.OwnerPub, "%s de-identified", k.ID)
			}
			o, _, _ := s.AccountKeyByID("key_o")
			require.False(t, o.Revoked, "another account's key is untouched")
		})
	}
}

func TestOwnerStrikePayersParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			const owner, kind = "u_strike_owner", "empty_output"
			for i, ev := range []string{
				`{"payer":"w1"}`, `{"payer":"w1"}`, `{"payer":"w2"}`, // two distinct payers on three rows
				`{}`, // a pre-floor row: its own payer
			} {
				_, err := s.OwnerStrike(owner, kind, ev, name+"-strike-"+string(rune('a'+i)))
				require.NoError(t, err)
			}
			_, err := s.OwnerStrike(owner, "other_kind", `{"payer":"w9"}`, name+"-strike-other")
			require.NoError(t, err)
			total, payerRows, payers, err := s.OwnerStrikePayers(owner, kind, 0)
			require.NoError(t, err)
			require.Equal(t, 4, total)
			require.Equal(t, 3, payerRows)
			require.Equal(t, 3, payers, "w1, w2 and the pre-floor row")
			total, _, _, err = s.OwnerStrikePayers(owner, kind, time.Now().Add(time.Hour).Unix())
			require.NoError(t, err)
			require.Zero(t, total, "nothing at or after a future since")
			total, _, _, _ = s.OwnerStrikePayers("u_nobody", kind, 0)
			require.Zero(t, total)
		})
	}
}
