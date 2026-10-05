package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// LinkVerifiedEmail is how a signed-in provider account PROVES an address: it records the
// address AND the proof atomically, and at most one live account may hold a verified address.
func TestLinkVerifiedEmail(t *testing.T) {
	now := time.Now().Unix()
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, db.BindOwner(Owner{Pubkey: "pk-a", GitHubID: 1, Login: "alice"}))
			require.NoError(t, db.BindOwner(Owner{Pubkey: "pk-b", GitHubID: 2, Login: "bob"}))

			// records address + proof; the address now resolves to THIS account
			require.NoError(t, db.LinkVerifiedEmail("pk-a", "alice@x.com", now))
			o, ok, _ := db.OwnerByVerifiedEmail("alice@x.com")
			require.True(t, ok)
			require.Equal(t, "pk-a", o.Pubkey)
			require.Equal(t, "alice", o.Login, "the account's login is untouched")
			require.Equal(t, int64(1), o.GitHubID)

			// idempotent
			require.NoError(t, db.LinkVerifiedEmail("pk-a", "alice@x.com", now+1))

			// another live account cannot take it, any case
			require.ErrorIs(t, db.LinkVerifiedEmail("pk-b", "ALICE@x.com", now), ErrEmailTaken)
			o, _, _ = db.OwnerByVerifiedEmail("alice@x.com")
			require.Equal(t, "pk-a", o.Pubkey, "the holder is unchanged")
			b, _, _ := db.OwnerByPubkey("pk-b")
			require.Zero(t, b.EmailVerifiedAt, "the refused account gained nothing")

			// replacing drops the old proof
			require.NoError(t, db.LinkVerifiedEmail("pk-a", "alice2@x.com", now))
			_, found, _ := db.OwnerByVerifiedEmail("alice@x.com")
			require.False(t, found, "the old address no longer resolves")
			o, found, _ = db.OwnerByVerifiedEmail("alice2@x.com")
			require.True(t, found)
			require.Equal(t, "pk-a", o.Pubkey)

			// ...and the old address is free for someone else
			require.NoError(t, db.LinkVerifiedEmail("pk-b", "alice@x.com", now))

			// a deleted (anonymized) account's address is reusable
			ok2, err := db.DeleteAccount("bob")
			require.NoError(t, err)
			require.True(t, ok2)
			require.NoError(t, db.LinkVerifiedEmail("pk-a", "alice@x.com", now), "a deleted account does not hold an address hostage")

			// unknown / deleted owner
			require.ErrorIs(t, db.LinkVerifiedEmail("pk-nope", "z@x.com", now), ErrNoOwner)
			require.ErrorIs(t, db.LinkVerifiedEmail("pk-b", "z@x.com", now), ErrNoOwner, "an anonymized account cannot link")
		})
	}
}
