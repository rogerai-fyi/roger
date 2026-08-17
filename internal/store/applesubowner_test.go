package store

// Apple-identity account resolution (OwnerByAppleSub), the third provider behind the
// consolidated website login alongside GitHub id and verified email. Apple's "sub" is a
// stable, unique per-account key, so it resolves the correct owner without any reliance on
// a collidable login string - the property that keeps Apple sessions isolated from GitHub
// accounts (features/security/apple_session_isolation) while still letting an Apple-signup
// operator reach their earnings/payouts.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOwnerByAppleSubResolvesAndIsolates(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			uid := name + "-" + time.Now().UTC().Format("150405.000000000")
			sub := "apple-sub-" + uid
			pk := "pk-apple-" + uid
			require.NoError(t, db.BindOwner(Owner{Pubkey: pk, AppleSub: sub, Login: "op@privaterelay.appleid.com"}))

			got, ok, err := db.OwnerByAppleSub(sub)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, pk, got.Pubkey)
			require.Equal(t, sub, got.AppleSub)

			// An empty sub never resolves (an owner with no Apple link must not match "").
			_, ok, err = db.OwnerByAppleSub("")
			require.NoError(t, err)
			require.False(t, ok, "an empty Apple sub must not resolve any account")

			// An unknown sub never resolves.
			_, ok, err = db.OwnerByAppleSub("apple-sub-nobody-" + uid)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

func TestOwnerByAppleSubDoesNotReachAnonymizedAccounts(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			uid := name + "-" + time.Now().UTC().Format("150405.000000000")
			sub := "apple-sub-del-" + uid
			login := "gone-" + uid
			require.NoError(t, db.BindOwner(Owner{Pubkey: "pk-del-" + uid, AppleSub: sub, Login: login}))
			ok, err := db.DeleteAccount(login)
			require.NoError(t, err)
			require.True(t, ok)

			_, found, err := db.OwnerByAppleSub(sub)
			require.NoError(t, err)
			require.False(t, found, "a scrubbed, anonymized row is not reachable by its Apple sub")
		})
	}
}
