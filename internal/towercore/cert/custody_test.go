package cert

// Where the issuing root lives between restarts.
//
// NewAuthority mints a fresh root every call, which is right for a test and wrong for a
// deployment: a restart would issue a NEW root, and every certificate already in an
// operator's hands would stop authenticating at once. There is no recovery from that
// except re-enrolling every Tower on the network.
//
// The ladder below is deliberate. A deployment that injects a root as a secret gets what
// it configured. A deployment that does not gets a root generated once and kept, so a
// self-hoster is never asked to run a PKI ceremony before their first Tower - but it is
// told plainly that the root is sitting in its database.

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestARootSurvivesARestart(t *testing.T) {
	// The property everything else rests on: certificates issued before a restart still
	// authenticate after it.
	store := NewMemCustody()

	first, err := LoadOrCreate(Config{TTL: time.Hour}, store)
	require.NoError(t, err)
	leaf, err := first.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	second, err := LoadOrCreate(Config{TTL: time.Hour}, store)
	require.NoError(t, err)

	id, err := second.Authenticate(leaf)
	require.NoError(t, err, "a restart must not invalidate every Tower on the network")
	require.Equal(t, "tw-abc123", id)
	require.Equal(t, first.Root().SerialNumber, second.Root().SerialNumber,
		"it is the SAME root, not a new one that happens to work")
}

func TestTheRootIsGeneratedExactlyOnce(t *testing.T) {
	store := NewMemCustody()

	a, err := LoadOrCreate(Config{}, store)
	require.NoError(t, err)
	b, err := LoadOrCreate(Config{}, store)
	require.NoError(t, err)

	require.Equal(t, a.Root().SerialNumber, b.Root().SerialNumber)
	require.Equal(t, 1, store.(*memCustody).writes, "a second start must not re-mint the root")
}

func TestAnInjectedRootIsUsedAsGiven(t *testing.T) {
	// The production shape: the root arrives as a secret and this process is not the place
	// it is generated or stored.
	origin, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)
	keyPEM, certPEM, err := ExportRoot(origin)
	require.NoError(t, err)

	store := NewMemCustody()
	loaded, err := LoadOrCreate(Config{TTL: time.Hour, RootKeyPEM: keyPEM, RootCertPEM: certPEM}, store)
	require.NoError(t, err)

	require.Equal(t, origin.Root().SerialNumber, loaded.Root().SerialNumber)
	require.Zero(t, store.(*memCustody).writes,
		"an injected root must never be written to the database behind the operator's back")

	// And it really issues under that root.
	leaf, err := loaded.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)
	_, err = origin.Authenticate(leaf)
	require.NoError(t, err)
}

func TestAnInjectedRootThatDoesNotMatchItsKeyIsRefused(t *testing.T) {
	// Two halves of different roots is a misconfiguration that would otherwise produce
	// certificates nothing can verify.
	a, err := NewAuthority(Config{})
	require.NoError(t, err)
	b, err := NewAuthority(Config{})
	require.NoError(t, err)

	keyPEM, _, err := ExportRoot(a)
	require.NoError(t, err)
	_, certPEM, err := ExportRoot(b)
	require.NoError(t, err)

	_, err = LoadOrCreate(Config{RootKeyPEM: keyPEM, RootCertPEM: certPEM}, NewMemCustody())
	require.Error(t, err, "the key must belong to the certificate it is paired with")
}

func TestAHalfConfiguredRootIsRefusedRatherThanGenerated(t *testing.T) {
	// Silently generating a root because one env var was missing is how a deployment ends
	// up issuing under a root nobody meant to use.
	origin, err := NewAuthority(Config{})
	require.NoError(t, err)
	keyPEM, certPEM, err := ExportRoot(origin)
	require.NoError(t, err)

	_, err = LoadOrCreate(Config{RootKeyPEM: keyPEM}, NewMemCustody())
	require.Error(t, err, "a key with no certificate is a misconfiguration, not a request to generate")

	_, err = LoadOrCreate(Config{RootCertPEM: certPEM}, NewMemCustody())
	require.Error(t, err)
}

func TestGarbagePEMIsRefused(t *testing.T) {
	_, err := LoadOrCreate(Config{
		RootKeyPEM: []byte("not a key"), RootCertPEM: []byte("not a certificate"),
	}, NewMemCustody())
	require.Error(t, err)
}

func TestRevocationsSurviveARestart(t *testing.T) {
	// A revocation that lives only in the process that made it is undone by the next
	// deploy - and the whole point of revoking is that it stays revoked.
	store := NewMemCustody()

	first, err := LoadOrCreate(Config{TTL: time.Hour}, store)
	require.NoError(t, err)
	leaf, err := first.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)
	require.NoError(t, first.Revoke(leaf.SerialNumber))

	second, err := LoadOrCreate(Config{TTL: time.Hour}, store)
	require.NoError(t, err)
	_, err = second.Authenticate(leaf)
	require.Error(t, err, "a revoked certificate must stay revoked across a deploy")
}

func TestARevocationIsPersistedWhenItIsMadeNotAtShutdown(t *testing.T) {
	// Waiting until shutdown to write means a crash loses the revocation, and a crash is
	// exactly when somebody has just revoked something urgently.
	store := NewMemCustody()
	a, err := LoadOrCreate(Config{}, store)
	require.NoError(t, err)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.NoError(t, a.Revoke(leaf.SerialNumber))
	require.Contains(t, store.(*memCustody).revoked, leaf.SerialNumber.String(),
		"the revocation is durable the moment it is made")
}

func TestAFailedRevocationWriteIsReportedRatherThanSwallowed(t *testing.T) {
	// If we cannot record it, the operator must know their revocation did not take.
	store := &memCustody{failWrite: true}
	a, err := LoadOrCreateFrom(mustAuthority(t), store)
	require.NoError(t, err)
	leaf, err := a.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	require.Error(t, a.Revoke(leaf.SerialNumber),
		"a revocation we could not persist must not be reported as done")
}

func TestExportRootRoundTrips(t *testing.T) {
	origin, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)
	keyPEM, certPEM, err := ExportRoot(origin)
	require.NoError(t, err)

	require.Contains(t, string(keyPEM), "PRIVATE KEY")
	require.Contains(t, string(certPEM), "CERTIFICATE")

	block, _ := decodePEMBlock(certPEM, "CERTIFICATE")
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, origin.Root().SerialNumber, cert.SerialNumber)
}

func mustAuthority(t *testing.T) *Authority {
	t.Helper()
	a, err := NewAuthority(Config{TTL: time.Hour})
	require.NoError(t, err)
	return a
}

func TestCustodyFailuresAreReportedNotSwallowed(t *testing.T) {
	// Startup must fail loudly. A broker that came up believing it had a root, and did not,
	// would issue certificates nothing could verify.
	failing := &memCustody{failWrite: true}
	_, err := LoadOrCreate(Config{}, failing)
	require.Error(t, err, "a root we could not store must not be used as though it were kept")

	_, err = LoadOrCreate(Config{}, nil)
	require.Error(t, err, "there is nowhere to keep the root")
}

func TestLoadOrCreateFromRequiresBothHalves(t *testing.T) {
	_, err := LoadOrCreateFrom(nil, NewMemCustody())
	require.Error(t, err)
	_, err = LoadOrCreateFrom(mustAuthority(t), nil)
	require.Error(t, err)
}

func TestLoadOrCreateFromAdoptsStoredRevocations(t *testing.T) {
	// The path for a root that came from somewhere else entirely: it still has to pick up
	// the revocations already on record, or a revoked Tower silently works again.
	origin := mustAuthority(t)
	leaf, err := origin.Issue("tw-abc123", newKey(t).Public())
	require.NoError(t, err)

	store := NewMemCustody()
	require.NoError(t, store.SaveRevoked(leaf.SerialNumber.String()))

	adopted, err := LoadOrCreateFrom(mustAuthority(t), store)
	require.NoError(t, err)
	require.Contains(t, adopted.RevokedSerials(), leaf.SerialNumber.String())
}

func TestExportRootRefusesNothing(t *testing.T) {
	_, _, err := ExportRoot(nil)
	require.Error(t, err)
}

func TestAPEMBundleFindsTheBlockItNeeds(t *testing.T) {
	// A bundle carrying more than one block must not depend on ordering: operators paste
	// these together all the time.
	origin := mustAuthority(t)
	keyPEM, certPEM, err := ExportRoot(origin)
	require.NoError(t, err)

	bundle := append(append([]byte{}, certPEM...), keyPEM...)
	loaded, err := LoadOrCreate(Config{RootKeyPEM: bundle, RootCertPEM: bundle}, NewMemCustody())
	require.NoError(t, err)
	require.Equal(t, origin.Root().SerialNumber, loaded.Root().SerialNumber)

	block, _ := decodePEMBlock([]byte("no pem here"), "CERTIFICATE")
	require.Nil(t, block)
}

func TestAKeyThatCannotSignIsRefused(t *testing.T) {
	// A PEM block that parses as a key but is not usable for signing must be caught at
	// startup, not at the first issuance.
	origin := mustAuthority(t)
	_, certPEM, err := ExportRoot(origin)
	require.NoError(t, err)

	notAKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	_, err = LoadOrCreate(Config{RootKeyPEM: notAKey, RootCertPEM: certPEM}, NewMemCustody())
	require.Error(t, err)
}

func TestARootThatIsNotACAIsRefused(t *testing.T) {
	// A leaf certificate as a root would mean anything it "issued" chained to nothing.
	origin := mustAuthority(t)
	keyPEM, _, err := ExportRoot(origin)
	require.NoError(t, err)
	leaf, err := origin.Issue("tw-abc123", origin.RootKey().Public())
	require.NoError(t, err)
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})

	_, err = LoadOrCreate(Config{RootKeyPEM: keyPEM, RootCertPEM: leafPEM}, NewMemCustody())
	require.Error(t, err)
}
