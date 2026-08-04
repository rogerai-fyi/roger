package store

// Verified-email account resolution, per features/auth/email_code_login.feature.
//
// An address becomes an identity only once somebody has PROVEN they hold it. The
// distinction between a recorded email and a verified one is the whole security story
// here: owners.email has always been a user-editable profile field, and treating it as a
// login key would mean anyone who can type an address into their profile can claim the
// account that address belongs to.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnUnverifiedEmailIsNotAnIdentity(t *testing.T) {
	m := NewMem()
	require.NoError(t, m.BindOwner(Owner{Pubkey: "pk-1", GitHubID: 7, Login: "octocat"}))

	// The profile field alone: recorded, never proven.
	_, ok, err := m.UpdateAccount("octocat", "someone@rogerai.fm")
	require.NoError(t, err)
	require.True(t, ok)

	_, found, err := m.OwnerByVerifiedEmail("someone@rogerai.fm")
	require.NoError(t, err)
	require.False(t, found, "a self-asserted address must never resolve to an account")
}

func TestAVerifiedEmailResolvesToItsAccount(t *testing.T) {
	m := NewMem()
	now := time.Now().Unix()
	require.NoError(t, m.BindOwner(Owner{
		Pubkey: "pk-1", Login: "someone@rogerai.fm",
		Email: "someone@rogerai.fm", EmailVerifiedAt: now,
	}))

	got, ok, err := m.OwnerByVerifiedEmail("someone@rogerai.fm")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "pk-1", got.Pubkey)
	require.Equal(t, now, got.EmailVerifiedAt)
}

func TestVerifiedEmailLookupIsCaseInsensitiveOnTheStoredForm(t *testing.T) {
	m := NewMem()
	require.NoError(t, m.BindOwner(Owner{
		Pubkey: "pk-1", Login: "someone@rogerai.fm",
		Email: "someone@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))

	got, ok, err := m.OwnerByVerifiedEmail("SoMeOne@RogerAI.FM")
	require.NoError(t, err)
	require.True(t, ok, "the caller normalizes, but a stray spelling must not mint a second account")
	require.Equal(t, "pk-1", got.Pubkey)
}

func TestADeletedAccountsAddressDoesNotResurrectIt(t *testing.T) {
	// Scenario: signing in with the original address creates a NEW account, and nothing
	// from the deleted one's history, wallet, or balance is attached to it.
	m := NewMem()
	require.NoError(t, m.BindOwner(Owner{
		Pubkey: "pk-1", Login: "someone@rogerai.fm",
		Email: "someone@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))
	ok, err := m.DeleteAccount("someone@rogerai.fm")
	require.NoError(t, err)
	require.True(t, ok)

	_, found, err := m.OwnerByVerifiedEmail("someone@rogerai.fm")
	require.NoError(t, err)
	require.False(t, found, "a scrubbed, anonymized row is not reachable by its old address")
}

func TestBindingAVerifiedEmailPreservesAnExistingProviderLink(t *testing.T) {
	// The same cross-provider preserve BindOwner already applies to GitHub and Apple: a
	// person who adds an email to a GitHub account must not lose the GitHub link.
	m := NewMem()
	require.NoError(t, m.BindOwner(Owner{Pubkey: "pk-1", GitHubID: 7, Login: "octocat"}))
	require.NoError(t, m.BindOwner(Owner{
		Pubkey: "pk-1", Email: "someone@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))

	got, ok, err := m.OwnerByPubkey("pk-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 7, got.GitHubID, "the GitHub link survives an email bind")
	require.Equal(t, "octocat", got.Login)
	require.NotZero(t, got.EmailVerifiedAt)

	byEmail, ok, err := m.OwnerByVerifiedEmail("someone@rogerai.fm")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "pk-1", byEmail.Pubkey, "and both routes reach the ONE account")
}

func TestAnEmailBindDoesNotClearAVerificationAlreadyRecorded(t *testing.T) {
	m := NewMem()
	verified := time.Now().Add(-time.Hour).Unix()
	require.NoError(t, m.BindOwner(Owner{
		Pubkey: "pk-1", Email: "someone@rogerai.fm", EmailVerifiedAt: verified,
	}))
	// A later bind that carries no email (e.g. a GitHub re-bind on the same device).
	require.NoError(t, m.BindOwner(Owner{Pubkey: "pk-1", GitHubID: 7, Login: "octocat"}))

	got, _, err := m.OwnerByPubkey("pk-1")
	require.NoError(t, err)
	require.Equal(t, verified, got.EmailVerifiedAt, "a proof already given is not withdrawn")
	require.Equal(t, "someone@rogerai.fm", got.Email)
}
