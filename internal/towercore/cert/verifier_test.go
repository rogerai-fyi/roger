package cert

// The VERIFY-ONLY authority and the constructor refusals: a node holding only the public
// root can authenticate everything the root issued and can issue nothing - structurally,
// because it was never given the private half. Plus the paths the custody ladder refuses.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAVerifierAuthenticatesAndCannotIssue(t *testing.T) {
	issuer := newAuthority(t)
	key := newKey(t)
	leaf, err := issuer.Issue("tower-1", key.Public())
	require.NoError(t, err)

	v, err := NewVerifier(issuer.Root(), nil)
	require.NoError(t, err)
	require.Nil(t, v.RootKey(), "a verifier holds no private half")
	_, err = v.Authenticate(leaf)
	require.NoError(t, err, "a verifier authenticates what the root issued")
	_, err = v.Issue("tower-2", newKey(t).Public())
	require.Error(t, err, "a verifier can never issue")
	require.Contains(t, err.Error(), "never issue")

	// A revocation set handed in at construction is honoured.
	rv, err := NewVerifier(issuer.Root(), []string{leaf.SerialNumber.String()})
	require.NoError(t, err)
	_, err = rv.Authenticate(leaf)
	require.Error(t, err, "a serial revoked at construction is refused")
}

func TestVerifierRefusesNoRootAndANonCA(t *testing.T) {
	_, err := NewVerifier(nil, nil)
	require.Error(t, err)
	issuer := newAuthority(t)
	leaf, err := issuer.Issue("tower-1", newKey(t).Public())
	require.NoError(t, err)
	_, err = NewVerifier(leaf, nil)
	require.Error(t, err, "a leaf is not a certificate authority")
}

func TestNewAuthorityFromRefusals(t *testing.T) {
	a := newAuthority(t)
	_, err := NewAuthorityFrom(nil, a.Root(), Config{}, nil)
	require.Error(t, err)
	_, err = NewAuthorityFrom(a.RootKey(), nil, Config{}, nil)
	require.Error(t, err)
	leaf, err := a.Issue("tower-1", newKey(t).Public())
	require.NoError(t, err)
	_, err = NewAuthorityFrom(a.RootKey(), leaf, Config{}, nil)
	require.Error(t, err, "a leaf cannot root an authority")
	// A zero TTL takes the default, and a revocation set is adopted.
	b, err := NewAuthorityFrom(a.RootKey(), a.Root(), Config{}, []string{"12345"})
	require.NoError(t, err)
	require.Equal(t, defaultTTL, b.cfg.TTL)
	require.Contains(t, b.RevokedSerials(), "12345")
}

func TestValidTowerIDRoundTrips(t *testing.T) {
	for _, bad := range []string{"", " t", "t ", "a/b", "a\\b", "a b", "a\tb", "a?b", "a#b", "a%b", "a:b", "a@b"} {
		require.False(t, validTowerID(bad), "%q must not be an identity", bad)
	}
	for _, good := range []string{"tower-1", "t.1", "T_1", "shed"} {
		require.True(t, validTowerID(good), good)
	}
}

func TestProveMatchesRefusals(t *testing.T) {
	a := newAuthority(t)
	key := newKey(t)
	leaf, err := a.Issue("tower-1", key.Public())
	require.NoError(t, err)
	require.NoError(t, a.ProveMatches(leaf, key.Public()))
	require.Error(t, a.ProveMatches(nil, key.Public()))
	require.Error(t, a.ProveMatches(leaf, nil))
	other := newKey(t)
	require.Error(t, a.ProveMatches(leaf, other.Public()), "another key is not this certificate's")
}

func TestIssueRefusesANilPublicKey(t *testing.T) {
	a := newAuthority(t)
	_, err := a.Issue("tower-1", nil)
	require.Error(t, err)
}

// failingCustody is the storage boundary refusing: the ladder must surface it, never
// generate a root over it.
type failingCustody struct {
	memCustody
	loadRootErr    error
	loadRevokedErr error
}

func (f *failingCustody) LoadRoot() ([]byte, []byte, bool, error) {
	if f.loadRootErr != nil {
		return nil, nil, false, f.loadRootErr
	}
	return f.memCustody.LoadRoot()
}

func (f *failingCustody) LoadRevoked() ([]string, error) {
	if f.loadRevokedErr != nil {
		return nil, f.loadRevokedErr
	}
	return f.memCustody.LoadRevoked()
}

func TestTheCustodyLadderSurfacesEveryRefusal(t *testing.T) {
	_, err := LoadOrCreate(Config{}, nil)
	require.Error(t, err, "no custody at all")

	_, err = LoadOrCreate(Config{}, &failingCustody{loadRootErr: errors.New("disk on fire")})
	require.ErrorContains(t, err, "disk on fire")

	_, err = LoadOrCreate(Config{}, &memCustody{failWrite: true})
	require.ErrorContains(t, err, "custody unavailable", "a root that cannot be stored is not generated silently")

	a := newAuthority(t)
	keyPEM, certPEM, err := ExportRoot(a)
	require.NoError(t, err)
	_, err = LoadOrCreate(Config{RootKeyPEM: keyPEM, RootCertPEM: certPEM}, &failingCustody{loadRevokedErr: errors.New("no list")})
	require.ErrorContains(t, err, "no list", "an injected root still needs its revocations read")

	_, err = LoadOrCreateFrom(a, &failingCustody{loadRevokedErr: errors.New("no list")})
	require.ErrorContains(t, err, "no list")

	_, _, err = ExportRoot(nil)
	require.Error(t, err)
}

func TestAnInjectedKeyOfAnotherRootIsRefused(t *testing.T) {
	a, b := newAuthority(t), newAuthority(t)
	keyA, _, err := ExportRoot(a)
	require.NoError(t, err)
	_, certB, err := ExportRoot(b)
	require.NoError(t, err)
	_, err = LoadOrCreate(Config{RootKeyPEM: keyA, RootCertPEM: certB}, NewMemCustody())
	require.ErrorContains(t, err, "does not match")
}

func TestAnAuthorityIssuesWithinItsTTL(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	a, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)
	leaf, err := a.Issue("tower-1", key.Public())
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), leaf.NotAfter, 2*time.Minute)
}
