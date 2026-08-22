package main

// accountkey_test.go pins the multi-device invariant: ONE account, ONE key its money lives
// under, however many devices its owner signs in from - at mint and at cash-out alike.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// twoDevices binds two owner rows to ONE GitHub account, oldest first, and returns both
// pubkeys in creation order.
func twoDevices(t *testing.T, b *broker, login string, githubID int64) (first, second string) {
	t.Helper()
	mk := func(created int64) string {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		hexPub := hexOf(pub)
		require.NoError(t, b.db.BindOwner(store.Owner{
			Pubkey: hexPub, Login: login, GitHubID: githubID,
			Email: login + "@x.test", EmailVerifiedAt: created, CreatedAt: created,
		}))
		return hexPub
	}
	first = mk(1000)
	second = mk(2000)
	return first, second
}

// The lookups must answer the SAME row every time. In the mem store they used to range a Go
// map, whose iteration order is deliberately randomized - so two calls a microsecond apart
// could name different device rows as "the" account.
func TestAnAccountResolvesToOneRowEveryTime(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	first, second := twoDevices(t, b, "octocat", 7)

	for i := 0; i < 50; i++ {
		got, found, err := b.db.OwnerByLogin("octocat")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, first, got.Pubkey, "the earliest row is the canonical one, always")
	}
	// And every device of that account resolves to it.
	for _, dev := range []string{first, second} {
		require.Equal(t, first, b.accountKeyOfPubkey(dev))
	}
}

// THE FAILURE THIS PREVENTS: earn on one device, cash out from the other. Before the money
// keyed on whichever device was present, so the second device saw an empty balance.
func TestEarningsFollowTheAccountNotTheDevice(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b := testBrokerWithDB(store.NewMem())
	first, second := twoDevices(t, b, "octocat", 7)

	// A lot minted under the account's canonical key (what the settle path now resolves).
	require.NoError(t, b.db.BindNode("n-1", b.accountKeyOfPubkey(second)))
	consumer := "u_consumer"
	_, err := b.db.AddCredits(consumer, 100)
	require.NoError(t, err)
	_, err = b.db.Hold(consumer, 10)
	require.NoError(t, err)
	_, err = b.db.Finalize(consumer, "n-1", 10, 10, 7, rec("req-1"))
	require.NoError(t, err)

	// Read from EITHER device: one balance.
	for _, dev := range []string{first, second} {
		o, found, oerr := b.db.OwnerByPubkey(dev)
		require.NoError(t, oerr)
		require.True(t, found)
		split, serr := b.db.EarningSplitOf(b.accountKeyOf(o), time.Now())
		require.NoError(t, serr)
		require.InDelta(t, 7.0, split.Payable, 1e-9,
			"the same account sees the same money from every device it signs in from")
	}
}
