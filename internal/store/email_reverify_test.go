package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Changing the profile email must drop the "verified" stamp: the proof was for the OLD
// address. A verified address mints a session, so a stale stamp on a self-asserted new one
// would let an address nobody proved resolve to (and sign in as) this account.
func TestChangingTheProfileEmailDropsItsVerification(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, db.BindOwner(Owner{
				Pubkey: "pk-v", GitHubID: 5, Login: "verified-user",
				Email: "proved@x.com", EmailVerifiedAt: time.Now().Unix(),
			}))
			_, found, _ := db.OwnerByVerifiedEmail("proved@x.com")
			require.True(t, found, "precondition: the proven address resolves")

			// Re-saving the SAME address keeps the proof.
			_, ok, err := db.UpdateAccount("verified-user", "proved@x.com")
			require.NoError(t, err)
			require.True(t, ok)
			_, found, _ = db.OwnerByVerifiedEmail("proved@x.com")
			require.True(t, found, "an unchanged address stays verified")

			// Changing it does not.
			_, ok, err = db.UpdateAccount("verified-user", "other@x.com")
			require.NoError(t, err)
			require.True(t, ok)
			_, found, _ = db.OwnerByVerifiedEmail("other@x.com")
			require.False(t, found, "an address nobody proved must not resolve")
			_, found, _ = db.OwnerByVerifiedEmail("proved@x.com")
			require.False(t, found, "the old proof does not follow the row to a new address")
		})
	}
}
