package cert

// The joined-Tower certificate, per features/tower/public_enrollment.feature
// ("certificate content and channel authentication").
//
// This certificate is the ONLY thing that lets a community-run machine speak on the public
// network as a named Tower. Everything downstream - inventory, routing, dispatch, receipts
// - trusts the Tower ID this certificate asserts, so every scenario below is really one
// question: can a machine end up speaking as a Tower it is not?

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return k
}

func newAuthority(t *testing.T) *Authority {
	t.Helper()
	a, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)
	return a
}

// --- what an issued certificate says --------------------------------------

func TestAnIssuedCertificateNamesExactlyItsTower(t *testing.T) {
	a := newAuthority(t)
	key := newKey(t)

	leaf, err := a.Issue("tw-abc123", key.Public())
	require.NoError(t, err)

	require.Len(t, leaf.URIs, 1, "exactly one identity, or 'which Tower is this' has no answer")
	require.Equal(t, TowerURI("tw-abc123").String(), leaf.URIs[0].String())
	require.Equal(t, "tw-abc123", leaf.Subject.CommonName)
}

func TestAnIssuedCertificateCarriesNoAuthorityBeyondTheChannel(t *testing.T) {
	// "it contains no wallet, settlement, admin, or platform-signing authority". The
	// enforceable form of that is: it may not sign for anything, and it may not issue.
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.False(t, leaf.IsCA, "a Tower certificate must never mint another identity")
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, leaf.ExtKeyUsage,
		"the joined channel only - not server auth, not code signing")
	require.Equal(t, x509.KeyUsageDigitalSignature, leaf.KeyUsage&x509.KeyUsageDigitalSignature)
	require.Zero(t, leaf.KeyUsage&x509.KeyUsageCertSign)
	require.Zero(t, leaf.KeyUsage&x509.KeyUsageCRLSign)
}

func TestAnIssuedCertificateIsUnambiguousAboutItsBounds(t *testing.T) {
	a := newAuthority(t)
	issuedAt := time.Now()
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.NotNil(t, leaf.SerialNumber)
	require.Positive(t, leaf.SerialNumber.Sign(), "a serial is what revocation names")
	// NotBefore is deliberately backdated a minute to absorb clock skew between us and the
	// Tower; without it a fresh certificate is briefly "not yet valid" on a host running
	// slightly behind. Bounded, so the backdating cannot quietly grow into a real window.
	require.WithinDuration(t, issuedAt.Add(-time.Minute), leaf.NotBefore, 5*time.Second)
	require.True(t, leaf.NotAfter.After(leaf.NotBefore))
	require.WithinDuration(t, issuedAt.Add(time.Hour), leaf.NotAfter, time.Minute,
		"short-lived by construction: the lease, not the certificate, is the long-lived thing")
	require.Equal(t, a.Root().Subject.String(), leaf.Issuer.String())
}

func TestSerialsAreUnpredictableAndDistinct(t *testing.T) {
	// A guessable serial lets somebody speak about a certificate that does not exist yet.
	a := newAuthority(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		leaf, err := a.Issue("tw-abc123", newKey(t).Public())
		require.NoError(t, err)
		s := leaf.SerialNumber.String()
		require.False(t, seen[s], "serials must not repeat")
		seen[s] = true
		require.Greater(t, leaf.SerialNumber.BitLen(), 32, "and must not be a counter")
	}
}

func TestIssuingRefusesAnEmptyOrMalformedTowerID(t *testing.T) {
	a := newAuthority(t)
	for _, bad := range []string{"", " ", "tw/../other", "tw abc", "tw\n123"} {
		_, err := a.Issue(bad, newKey(t).Public())
		require.Error(t, err, "a Tower ID that does not round-trip through a URI is not an identity: %q", bad)
	}
}

// --- authenticating a joined channel --------------------------------------

func TestAValidCertificateAuthenticatesItsTower(t *testing.T) {
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	id, err := a.Authenticate(leaf)
	require.NoError(t, err)
	require.Equal(t, "tw-abc123", id)
}

func TestRogerCoreRefusesAJoinedChannelWithAnInvalidCertificate(t *testing.T) {
	// The spec's table. Each row is a way a machine could end up speaking as a Tower it is
	// not, so each one must fail BEFORE inventory or jobs are accepted.
	a := newAuthority(t)

	t.Run("none", func(t *testing.T) {
		_, err := a.Authenticate(nil)
		require.Error(t, err)
	})

	t.Run("self-signed", func(t *testing.T) {
		_, err := a.Authenticate(selfSigned(t, "tw-abc123"))
		require.Error(t, err)
	})

	t.Run("issued by an unknown root", func(t *testing.T) {
		other := newAuthority(t)
		leaf, err := other.Issue("tw-abc123", newKey(t).Public())
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "another authority's Tower is not ours")
	})

	t.Run("valid for a Station rather than a Tower", func(t *testing.T) {
		leaf, err := a.issueForTest("st-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			u, _ := url.Parse("spiffe://rogerai.fm/station/st-abc123")
			tmpl.URIs = []*url.URL{u}
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "a Station credential must not open a Tower channel")
	})

	t.Run("not yet valid", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.NotBefore = time.Now().Add(time.Hour)
			tmpl.NotAfter = time.Now().Add(2 * time.Hour)
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err)
	})

	t.Run("expired", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.NotBefore = time.Now().Add(-2 * time.Hour)
			tmpl.NotAfter = time.Now().Add(-time.Hour)
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err)
	})

	t.Run("malformed", func(t *testing.T) {
		_, err := a.Authenticate(&x509.Certificate{})
		require.Error(t, err)
	})

	t.Run("missing the required URI identity", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.URIs = nil
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "a certificate that names no Tower authenticates no Tower")
	})

	t.Run("more than one URI identity", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			second, _ := url.Parse(TowerURI("tw-other").String())
			tmpl.URIs = append(tmpl.URIs, second)
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "two identities is an ambiguity an attacker chooses the answer to")
	})

	t.Run("carrying an unsupported critical constraint", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.ExtraExtensions = []pkix.Extension{{
				Id: []int{1, 3, 6, 1, 4, 1, 99999, 1}, Critical: true, Value: []byte{0x05, 0x00},
			}}
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "a constraint we cannot evaluate must not be ignored")
	})

	t.Run("a certificate that may issue others", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.IsCA = true
			tmpl.BasicConstraintsValid = true
			tmpl.KeyUsage |= x509.KeyUsageCertSign
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err, "a Tower must never be able to mint another Tower")
	})

	t.Run("wrong extended key usage", func(t *testing.T) {
		leaf, err := a.issueForTest("tw-abc123", newKey(t).Public(), func(tmpl *x509.Certificate) {
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}
		})
		require.NoError(t, err)
		_, err = a.Authenticate(leaf)
		require.Error(t, err)
	})
}

func TestAuthenticatingADifferentTowerIDIsRefusedWhenOneIsExpected(t *testing.T) {
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.NoError(t, a.AuthenticateAs(leaf, "tw-abc123"))
	require.Error(t, a.AuthenticateAs(leaf, "tw-other"),
		"a valid credential for one Tower must not answer for another")
}

// --- revocation ------------------------------------------------------------

func TestARevokedSerialIsRefused(t *testing.T) {
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	_, err = a.Authenticate(leaf)
	require.NoError(t, err)

	require.NoError(t, a.Revoke(leaf.SerialNumber))
	_, err = a.Authenticate(leaf)
	require.Error(t, err, "revocation must end new sessions")
}

func TestRevokingOneSerialLeavesTheTowersOthersAlone(t *testing.T) {
	// Rotation issues a new certificate for the same Tower. Revoking the old one must not
	// take the Tower off the network.
	a := newAuthority(t)
	old, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)
	fresh, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.NoError(t, a.Revoke(old.SerialNumber))

	_, err = a.Authenticate(old)
	require.Error(t, err)
	id, err := a.Authenticate(fresh)
	require.NoError(t, err, "rotating a certificate must not interrupt the Tower")
	require.Equal(t, "tw-abc123", id)
}

func TestRevokingIsIdempotentAndSurvivesAReload(t *testing.T) {
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.NoError(t, a.Revoke(leaf.SerialNumber))
	require.NoError(t, a.Revoke(leaf.SerialNumber), "revoking twice is not an error")

	// A revocation that lives only in this process is undone by the next deploy - the same
	// defect the admission registry had.
	revoked := a.RevokedSerials()
	require.Len(t, revoked, 1)

	reloaded, err := NewAuthorityFrom(a.RootKey(), a.Root(), Config{TTL: time.Hour}, revoked)
	require.NoError(t, err)
	_, err = reloaded.Authenticate(leaf)
	require.Error(t, err, "a revocation must not be forgotten by a restart")
}

// --- proof of possession ---------------------------------------------------

func TestACopiedCertificateWithoutItsKeyProvesNothing(t *testing.T) {
	// The certificate is public by nature - it crosses the wire on every handshake. What
	// makes it a credential is the key nobody else holds.
	a := newAuthority(t)
	key := newKey(t)
	leaf, err := a.Issue("tw-abc123", key.Public())
	require.NoError(t, err)

	attacker := newKey(t)
	require.Error(t, a.ProveMatches(leaf, attacker.Public()),
		"a stolen certificate paired with the wrong key must not authenticate")
	require.NoError(t, a.ProveMatches(leaf, key.Public()))
}

func selfSigned(t *testing.T, towerID string) *x509.Certificate {
	t.Helper()
	key := newKey(t)
	u, _ := url.Parse(TowerURI(towerID).String())
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: towerID},
		URIs:         []*url.URL{u},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

// --- construction, guards, and the remaining edges -------------------------

func TestNewAuthorityFromRefusesAnUnusableRoot(t *testing.T) {
	a := newAuthority(t)

	_, err := NewAuthorityFrom(nil, a.Root(), Config{}, nil)
	require.Error(t, err, "an authority without its key can issue nothing")

	_, err = NewAuthorityFrom(a.RootKey(), nil, Config{}, nil)
	require.Error(t, err)

	// A leaf is not an authority. Accepting one would make every Tower a potential issuer.
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)
	_, err = NewAuthorityFrom(a.RootKey(), leaf, Config{}, nil)
	require.Error(t, err, "a Tower certificate is not a certificate authority")
}

func TestAZeroTTLFallsBackToTheShortLivedDefault(t *testing.T) {
	a, err := NewAuthorityFrom(newKey(t), newAuthority(t).Root(), Config{}, nil)
	require.NoError(t, err)
	require.Equal(t, defaultTTL, a.cfg.TTL,
		"a zero config must not mean an unbounded certificate")
}

func TestIssuingRequiresAPublicKey(t *testing.T) {
	a := newAuthority(t)
	_, err := a.Issue("tw-abc123", nil)
	require.Error(t, err, "a certificate binding no key binds nobody")
}

func TestACertificateWithNoSerialIsNotTrusted(t *testing.T) {
	// isRevoked cannot answer for a certificate it cannot name, and "cannot check" must
	// resolve to "do not trust" rather than to "fine".
	a := newAuthority(t)
	require.True(t, a.isRevoked(nil))
}

func TestAnIdentityOutsideOurTrustDomainIsNotOurs(t *testing.T) {
	for name, raw := range map[string]string{
		"another trust domain": "spiffe://evil.example/tower/tw-abc123",
		"no path":              "spiffe://rogerai.fm",
		"a station":            "spiffe://rogerai.fm/station/st-1",
		"an empty tower id":    "spiffe://rogerai.fm/tower/",
		"a traversal":          "spiffe://rogerai.fm/tower/../station/st-1",
	} {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(raw)
			require.NoError(t, err)
			_, ok := towerIDFromURI(u)
			require.False(t, ok, "%s must not resolve to a Tower", raw)
		})
	}

	_, ok := towerIDFromURI(nil)
	require.False(t, ok)
}

func TestProveMatchesNeedsBothHalves(t *testing.T) {
	a := newAuthority(t)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.Error(t, a.ProveMatches(nil, newKey(t).Public()))
	require.Error(t, a.ProveMatches(leaf, nil))
}

func TestAuthenticateAsRejectsACertificateThatDoesNotAuthenticateAtAll(t *testing.T) {
	// The identity check must not become a way to skip the validity checks in front of it.
	a := newAuthority(t)
	require.Error(t, a.AuthenticateAs(selfSigned(t, "tw-abc123"), "tw-abc123"))
}

func TestRevokeNamesASerial(t *testing.T) {
	a := newAuthority(t)
	require.Error(t, a.Revoke(nil))
}

func TestAReloadedAuthorityKeepsIssuing(t *testing.T) {
	// The production path: a persisted root and revocation set are loaded, and the
	// authority carries on as the same issuer rather than as a new one.
	a := newAuthority(t)
	first, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	reloaded, err := NewAuthorityFrom(a.RootKey(), a.Root(), Config{TTL: time.Hour}, a.RevokedSerials())
	require.NoError(t, err)

	id, err := reloaded.Authenticate(first)
	require.NoError(t, err, "a certificate issued before the reload still authenticates")
	require.Equal(t, "tw-abc123", id)

	second, err := reloaded.Issue("tw-def456", newKey(t).Public())
	require.NoError(t, err)
	id, err = a.Authenticate(second)
	require.NoError(t, err, "and one issued after it is recognised by the original too")
	require.Equal(t, "tw-def456", id)
}

// SerialRevoked is the per-Tower kill switch: this deployment authenticates by signed
// request rather than TLS handshake, so the request-auth layer asks THIS question to make a
// revoked certificate actually stop its Tower. It had never been asked in a test.
func TestSerialRevokedIsTheKillSwitchTheAuthLayerAsks(t *testing.T) {
	a, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)

	serial := big.NewInt(424242)
	require.False(t, a.SerialRevoked(serial.String()), "an unrevoked serial must not read as killed")
	require.NoError(t, a.Revoke(serial))
	require.True(t, a.SerialRevoked(serial.String()), "the revocation must be visible to the auth layer")

	// Pre-enrollment: no serial recorded yet is NOT a revocation - killing every
	// not-yet-enrolled Tower would make enrollment impossible.
	require.False(t, a.SerialRevoked(""))
	require.False(t, a.SerialRevoked("999999"), "somebody else's serial stays alive")
}
