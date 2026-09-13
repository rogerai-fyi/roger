package edgeauth_test

// Unit cover for the Edge authority, against REAL certificates from the REAL
// towercore/cert authority and a REAL directory on disk. Nothing here is mocked: the
// only thing these tests do that the executable spec does not is drive the failures
// that are awkward to stage over a wire - an unreadable key file, a half-written
// directory, a serial that is not a serial.
//
// The behaviour itself is specified in features/edge/enrollment.feature and driven end
// to end from cmd/rogerai; this file is the table-driven half of the same contract.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

func authority(t *testing.T) *cert.Authority {
	t.Helper()
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	return a
}

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// signedRequest is one machine asking, properly.
func signedRequest(t *testing.T, account string, user ed25519.PrivateKey) (edgeauth.Request, ed25519.PublicKey) {
	t.Helper()
	pub, _ := keypair(t)
	r := edgeauth.NewRequest(account, "workshop", "host", pub, time.Now())
	r.Sign(user)
	return r, pub
}

func TestDescriptorSaysWhoRootsTheEdgeAndWhetherEnrollingNeedsTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		name         string
		d            edgeauth.Descriptor
		names        string
		needsNetwork bool
	}{
		{"core", edgeauth.Descriptor{Kind: edgeauth.KindCore}, "Core", true},
		{"unset defaults to core", edgeauth.Descriptor{}, "Core", true},
		{"local", edgeauth.Descriptor{Kind: edgeauth.KindLocal, Where: "shed"},
			`the designated machine "shed"`, false},
		{"local unnamed", edgeauth.Descriptor{Kind: edgeauth.KindLocal},
			"the designated machine", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.names, tc.d.Names())
			require.Equal(t, tc.needsNetwork, tc.d.NeedsNetwork())
			if tc.needsNetwork {
				require.Contains(t, tc.d.NetworkLine(), "needs the network")
			} else {
				require.Contains(t, tc.d.NetworkLine(), "needs no network")
			}
		})
	}
}

func TestCertificatesRoundTripAndRubbishIsMalformed(t *testing.T) {
	a := authority(t)
	pub, _ := keypair(t)
	leaf, err := a.Issue(edgeauth.NodeID(pub), pub)
	require.NoError(t, err)

	pemText := edgeauth.EncodeCert(leaf)
	back, err := edgeauth.DecodeCert(pemText)
	require.NoError(t, err)
	require.True(t, leaf.Equal(back))
	require.Equal(t, edgeauth.RootFingerprint(a.Root()), edgeauth.RootFingerprint(a.Root()))
	require.NotEqual(t, edgeauth.RootFingerprint(a.Root()), edgeauth.RootFingerprint(leaf))

	require.Empty(t, edgeauth.EncodeCert(nil))
	require.Empty(t, edgeauth.RootFingerprint(nil))
	for _, bad := range []string{"", "not pem", "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"} {
		_, err := edgeauth.DecodeCert(bad)
		require.ErrorIs(t, err, edgeauth.ErrMalformed, "%q", bad)
	}
}

func TestNodeIDIsDerivedFromTheKeyAndFitsAnMDNSLabel(t *testing.T) {
	pub, _ := keypair(t)
	id := edgeauth.NodeID(pub)
	require.True(t, strings.HasPrefix(id, "n_"))
	require.Len(t, id, 50, "an id is also a DNS label, which is at most 63 bytes")
	require.Equal(t, id, edgeauth.NodeID(pub), "the derivation is stable")
	other, _ := keypair(t)
	require.NotEqual(t, id, edgeauth.NodeID(other))
}

func TestASignatureCoversEveryFieldThatDecidesWhatIsIssued(t *testing.T) {
	_, user := keypair(t)
	base, _ := signedRequest(t, "owner", user)
	require.NoError(t, base.VerifySignature())

	for _, tc := range []struct {
		name   string
		break_ func(*edgeauth.Request)
	}{
		{"the node id", func(r *edgeauth.Request) { r.NodeID = "n_" + strings.Repeat("00", 24) }},
		{"the node key", func(r *edgeauth.Request) { r.NodeKey = strings.Repeat("ab", 32) }},
		{"the account", func(r *edgeauth.Request) { r.Account = "somebody-else" }},
		{"the name", func(r *edgeauth.Request) { r.Name = "not-workshop" }},
		{"the kind", func(r *edgeauth.Request) { r.Kind = "board" }},
		{"the nonce", func(r *edgeauth.Request) { r.Nonce = "00" }},
		{"the timestamp", func(r *edgeauth.Request) { r.TS++ }},
		{"the signing key it names", func(r *edgeauth.Request) {
			pub, _ := keypair(t)
			r.UserKey = hex.EncodeToString(pub)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := base
			tc.break_(&tampered)
			require.ErrorIs(t, tampered.VerifySignature(), edgeauth.ErrBadSignature)
		})
	}

	unusable := base
	unusable.UserKey = "zz"
	require.ErrorIs(t, unusable.VerifySignature(), edgeauth.ErrBadSignature)
	unusable = base
	unusable.Sig = "zz"
	require.ErrorIs(t, unusable.VerifySignature(), edgeauth.ErrBadSignature)

	bad := base
	bad.NodeKey = "zz"
	_, err := bad.NodePublicKey()
	require.Error(t, err)
	got, err := base.NodePublicKey()
	require.NoError(t, err)
	require.Equal(t, base.NodeID, edgeauth.NodeID(got))
}

func TestTheIssuerRefusesEveryWayARequestCanBeWrong(t *testing.T) {
	_, user := keypair(t)
	userHex := hex.EncodeToString(user.Public().(ed25519.PublicKey))
	known := func(k string) (string, bool) {
		if k == userHex {
			return "owner", true
		}
		return "", false
	}

	t.Run("a good request is issued, once", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		req, pub := signedRequest(t, "owner", user)
		resp, err := iss.Issue(req)
		require.NoError(t, err)
		require.Equal(t, "owner", resp.Account)
		require.Equal(t, edgeauth.NodeID(pub), resp.NodeID)
		leaf, err := edgeauth.DecodeCert(resp.Cert)
		require.NoError(t, err)
		serial, ok := iss.SerialOf(resp.NodeID)
		require.True(t, ok)
		require.Equal(t, leaf.SerialNumber.String(), serial)
		require.Equal(t, []edgeauth.Request{req}, iss.Requests())
		require.NotEmpty(t, iss.RootPEM())

		// THE SAME signed request, again: good once and not forever.
		_, err = iss.Issue(req)
		require.ErrorIs(t, err, edgeauth.ErrReplayed)
	})

	t.Run("an unsigned request", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		pub, _ := keypair(t)
		_, err := iss.Issue(edgeauth.NewRequest("owner", "workshop", "host", pub, time.Now()))
		require.ErrorIs(t, err, edgeauth.ErrBadSignature)
	})

	t.Run("a stale request", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		pub, _ := keypair(t)
		old := edgeauth.NewRequest("owner", "workshop", "host", pub, time.Now().Add(-time.Hour))
		old.Sign(user)
		_, err := iss.Issue(old)
		require.ErrorIs(t, err, edgeauth.ErrStaleRequest)

		ahead := edgeauth.NewRequest("owner", "workshop", "host", pub, time.Now().Add(time.Hour))
		ahead.Sign(user)
		_, err = iss.Issue(ahead)
		require.ErrorIs(t, err, edgeauth.ErrStaleRequest)
	})

	t.Run("a machine the account does not know", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		_, stranger := keypair(t)
		req, _ := signedRequest(t, "owner", stranger)
		_, err := iss.Issue(req)
		require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	})

	t.Run("an authority that cannot tell who is asking issues to nobody", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t)})
		req, _ := signedRequest(t, "owner", user)
		_, err := iss.Issue(req)
		require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	})

	t.Run("a request naming another account", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		req, _ := signedRequest(t, "somebody-else", user)
		_, err := iss.Issue(req)
		require.ErrorIs(t, err, edgeauth.ErrForeignAccount)
		require.NotContains(t, err.Error(), "somebody-else",
			"a refusal must not repeat the other account back")
	})

	t.Run("an id its key does not derive", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		pub, _ := keypair(t)
		req := edgeauth.NewRequest("owner", "workshop", "host", pub, time.Now())
		other, _ := keypair(t)
		req.NodeID = edgeauth.NodeID(other)
		req.Sign(user) // properly signed, and asking to be somebody else
		_, err := iss.Issue(req)
		require.ErrorIs(t, err, edgeauth.ErrKeyMismatch)
	})

	t.Run("a request carrying no usable node key", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		pub, _ := keypair(t)
		req := edgeauth.NewRequest("owner", "workshop", "host", pub, time.Now())
		req.NodeKey = "zz"
		req.Sign(user)
		_, err := iss.Issue(req)
		require.Error(t, err)
	})

	t.Run("an authority with no root cannot issue", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Accounts: known})
		req, _ := signedRequest(t, "owner", user)
		_, err := iss.Issue(req)
		require.Error(t, err)
		require.Nil(t, iss.Root())
		require.Empty(t, iss.RootPEM())
		require.Empty(t, edgeauth.NewIssuer(edgeauth.IssuerConfig{}).Requests())
	})

	t.Run("the attempt log is bounded", func(t *testing.T) {
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: authority(t), Accounts: known})
		for range 40 {
			req, _ := signedRequest(t, "owner", user)
			_, _ = iss.Issue(req)
		}
		require.LessOrEqual(t, len(iss.Requests()), 32)
	})
}

func TestCheckResponseRefusesEveryDefectiveAnswer(t *testing.T) {
	a := authority(t)
	pub, _ := keypair(t)
	id := edgeauth.NodeID(pub)
	leaf, err := a.Issue(id, pub)
	require.NoError(t, err)
	good := edgeauth.Response{
		NodeID: id, Account: "owner",
		Cert: edgeauth.EncodeCert(leaf), Root: edgeauth.EncodeCert(a.Root()),
	}
	want := edgeauth.Expect{NodeID: id, Account: "owner", Key: pub, Now: time.Now()}

	t.Run("a good answer", func(t *testing.T) {
		got, err := edgeauth.CheckResponse(good, want)
		require.NoError(t, err)
		require.Equal(t, id, got.NodeID)
		require.Equal(t, "owner", got.Account)
		require.True(t, got.Root.Equal(a.Root()))
	})

	t.Run("with no clock given it uses now", func(t *testing.T) {
		_, err := edgeauth.CheckResponse(good, edgeauth.Expect{NodeID: id, Account: "owner"})
		require.NoError(t, err)
	})

	otherPub, _ := keypair(t)
	otherLeaf, err := a.Issue(edgeauth.NodeID(otherPub), otherPub)
	require.NoError(t, err)
	stranger := authority(t)
	strangerLeaf, err := stranger.Issue(id, pub)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		resp edgeauth.Response
		is   error
	}{
		{"a root that is not a certificate",
			edgeauth.Response{NodeID: id, Account: "owner", Cert: good.Cert, Root: "rubbish"},
			edgeauth.ErrMalformed},
		{"a certificate that is not a certificate",
			edgeauth.Response{NodeID: id, Account: "owner", Cert: "rubbish", Root: good.Root},
			edgeauth.ErrMalformed},
		{"an answer naming no node",
			edgeauth.Response{Account: "owner", Cert: good.Cert, Root: good.Root},
			edgeauth.ErrMalformed},
		{"an answer naming no account",
			edgeauth.Response{NodeID: id, Cert: good.Cert, Root: good.Root},
			edgeauth.ErrMalformed},
		{"a certificate for a different account",
			edgeauth.Response{NodeID: id, Account: "somebody-else", Cert: good.Cert, Root: good.Root},
			edgeauth.ErrWrongAccount},
		{"a certificate signed by an unexpected root",
			edgeauth.Response{NodeID: id, Account: "owner",
				Cert: edgeauth.EncodeCert(strangerLeaf), Root: good.Root},
			edgeauth.ErrUnknownAuthority},
		{"a certificate for a different node id",
			edgeauth.Response{NodeID: edgeauth.NodeID(otherPub), Account: "owner",
				Cert: edgeauth.EncodeCert(otherLeaf), Root: good.Root},
			edgeauth.ErrIdentityMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := edgeauth.CheckResponse(tc.resp, want)
			require.ErrorIs(t, err, tc.is)
		})
	}

	t.Run("a certificate already expired", func(t *testing.T) {
		expired := expiredCert(t, a, id, pub)
		_, err := edgeauth.CheckResponse(edgeauth.Response{
			NodeID: id, Account: "owner",
			Cert: edgeauth.EncodeCert(expired), Root: good.Root}, want)
		require.ErrorIs(t, err, edgeauth.ErrCertExpired)
	})

	t.Run("an answer whose id disagrees with its own certificate", func(t *testing.T) {
		_, err := edgeauth.CheckResponse(edgeauth.Response{
			NodeID: edgeauth.NodeID(otherPub), Account: "owner",
			Cert: good.Cert, Root: good.Root}, edgeauth.Expect{Account: "owner"})
		require.ErrorIs(t, err, edgeauth.ErrIdentityMismatch)
	})

	t.Run("a certificate bound to another key", func(t *testing.T) {
		_, err := edgeauth.CheckResponse(good, edgeauth.Expect{
			NodeID: id, Account: "owner", Key: otherPub, Now: time.Now()})
		require.ErrorIs(t, err, edgeauth.ErrIdentityMismatch)
	})

	t.Run("a root this Edge does not use", func(t *testing.T) {
		_, err := edgeauth.CheckResponse(good, edgeauth.Expect{
			NodeID: id, Account: "owner", Key: pub, Now: time.Now(), Root: stranger.Root()})
		require.ErrorIs(t, err, edgeauth.ErrDifferentAuthority)
	})

	t.Run("a root that is not a certificate authority", func(t *testing.T) {
		_, err := edgeauth.CheckResponse(edgeauth.Response{
			NodeID: id, Account: "owner", Cert: good.Cert, Root: good.Cert}, want)
		require.ErrorIs(t, err, edgeauth.ErrUnknownAuthority)
	})
}

// expiredCert mints a certificate this authority really signed whose window has already
// closed: the only defect is the expiry.
func expiredCert(t *testing.T, a *cert.Authority, id string, pub ed25519.PublicKey) *x509.Certificate {
	t.Helper()
	u, err := url.Parse("spiffe://rogerai.fm/tower/" + id)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: id},
		URIs:                  []*url.URL{u},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(-time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.Root(), pub, a.RootKey())
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return leaf
}

func TestNeedsRenewalIsTwoThirdsOfTheWayThrough(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	id := &edgeauth.Identity{Cert: &x509.Certificate{
		NotBefore: base, NotAfter: base.Add(3 * time.Hour)}}
	require.False(t, id.NeedsRenewal(base))
	require.False(t, id.NeedsRenewal(base.Add(time.Hour)))
	require.True(t, id.NeedsRenewal(base.Add(2*time.Hour+time.Minute)))
	require.Equal(t, base.Add(3*time.Hour), id.NotAfter())

	require.True(t, (*edgeauth.Identity)(nil).NeedsRenewal(base))
	require.True(t, (&edgeauth.Identity{}).NeedsRenewal(base))
	require.True(t, (&edgeauth.Identity{Cert: &x509.Certificate{}}).NeedsRenewal(base))
	require.True(t, (*edgeauth.Identity)(nil).NotAfter().IsZero())
	require.True(t, (&edgeauth.Identity{}).NotAfter().IsZero())
}

func TestTheStoreKeepsAWholeIdentityOrNone(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}

	id, key, ok, err := st.LoadIdentity()
	require.NoError(t, err)
	require.False(t, ok, "a machine that has never enrolled is an ordinary state")
	require.Nil(t, id)
	require.Nil(t, key)
	name, err := st.AccountName()
	require.NoError(t, err)
	require.Empty(t, name)
	d, stored, err := st.Descriptor()
	require.NoError(t, err)
	require.False(t, stored)
	require.Equal(t, edgeauth.KindCore, d.Kind)
	_, _, err = st.Trust(time.Now())
	require.Error(t, err, "no root means no verification, and it says so")

	a := authority(t)
	pub, priv := keypair(t)
	nodeID := edgeauth.NodeID(pub)
	leaf, err := a.Issue(nodeID, pub)
	require.NoError(t, err)
	held := &edgeauth.Identity{
		NodeID: nodeID, Account: "owner", Cert: leaf, Root: a.Root(),
		CertPEM: edgeauth.EncodeCert(leaf), RootPEM: edgeauth.EncodeCert(a.Root()),
	}
	require.NoError(t, st.SaveAccount("owner"))
	require.NoError(t, st.SaveIdentity(held, priv, edgeauth.Descriptor{Kind: edgeauth.KindCore}))

	// The private key is readable by its owner and nobody else.
	fi, err := os.Stat(filepath.Join(dir, edgeauth.NodeKeyFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	got, gotKey, ok, err := st.LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, nodeID, got.NodeID)
	require.Equal(t, "owner", got.Account)
	require.Equal(t, priv, gotKey)
	d, stored, err = st.Descriptor()
	require.NoError(t, err)
	require.True(t, stored)
	require.Equal(t, edgeauth.RootFingerprint(a.Root()), d.Fingerprint)

	require.Error(t, st.SaveIdentity(nil, priv, d))
	require.Error(t, st.SaveIdentity(held, nil, d))

	// Forgetting gives up MEMBERSHIP and keeps the Edge: the root, the revocation list
	// and the account the fleet is filed under all survive.
	require.NoError(t, st.ForgetIdentity())
	_, _, ok, err = st.LoadIdentity()
	require.NoError(t, err)
	require.False(t, ok)
	name, err = st.AccountName()
	require.NoError(t, err)
	require.Equal(t, "owner", name)
	verifier, _, err := st.Trust(time.Now())
	require.NoError(t, err)
	require.True(t, verifier.Root().Equal(a.Root()))
}

func TestAVerifyOnlyTrustStoreCanCheckAndCannotIssue(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	a := authority(t)
	pub, priv := keypair(t)
	nodeID := edgeauth.NodeID(pub)
	leaf, err := a.Issue(nodeID, pub)
	require.NoError(t, err)
	require.NoError(t, st.SaveIdentity(&edgeauth.Identity{
		NodeID: nodeID, Cert: leaf, Root: a.Root(),
		CertPEM: edgeauth.EncodeCert(leaf), RootPEM: edgeauth.EncodeCert(a.Root()),
	}, priv, edgeauth.Descriptor{Kind: edgeauth.KindCore}))

	verifier, trust, err := st.Trust(time.Now())
	require.NoError(t, err)
	named, err := verifier.Authenticate(leaf)
	require.NoError(t, err)
	require.Equal(t, nodeID, named)
	_, err = verifier.Issue("n_whatever", pub)
	require.Error(t, err, "a node holds no private half, so it can never issue")
	require.Empty(t, trust.Revoked)

	// A revocation this machine records is honoured at once, and after a restart -
	// which is a new Store over the same directory.
	require.NoError(t, st.Revoke(leaf.SerialNumber.String(), time.Now()))
	require.NoError(t, st.Revoke(leaf.SerialNumber.String(), time.Now()), "revoking twice is not an error")
	restarted, _, err := edgeauth.Store{Dir: dir}.Trust(time.Now())
	require.NoError(t, err)
	require.True(t, restarted.SerialRevoked(leaf.SerialNumber.String()))
	_, err = restarted.Authenticate(leaf)
	require.Error(t, err)
}

func TestStaleTrustIsStatedNeverAssumed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	require.False(t, edgeauth.Trust{}.Stale(now, edgeauth.FreshnessWindow),
		"never refreshed because nothing is known yet is not the same as lapsed")
	require.Zero(t, edgeauth.Trust{}.Age(now))

	fresh := edgeauth.Trust{RefreshedAt: now.Add(-time.Hour).Unix()}
	require.False(t, fresh.Stale(now, edgeauth.FreshnessWindow))
	require.Equal(t, time.Hour, fresh.Age(now))

	old := edgeauth.Trust{RefreshedAt: now.Add(-30 * 24 * time.Hour).Unix()}
	require.True(t, old.Stale(now, edgeauth.FreshnessWindow))

	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	got, err := st.LoadTrust()
	require.NoError(t, err)
	require.Zero(t, got.RefreshedAt)
	require.NoError(t, st.SaveTrust([]string{"7"}, now))
	got, err = st.LoadTrust()
	require.NoError(t, err)
	require.Equal(t, []string{"7"}, got.Revoked)
	require.Equal(t, now.Unix(), got.RefreshedAt)
}

func TestAnUnreadableStoreIsAnErrorAndNotAnEmptyEdge(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	require.NoError(t, os.MkdirAll(dir, 0o700))

	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.NodeKeyFile), []byte("zz"), 0o600))
	_, _, _, err := st.LoadIdentity()
	require.Error(t, err)

	_, priv := keypair(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.NodeKeyFile),
		[]byte(hex.EncodeToString(priv)), 0o600))
	_, _, _, err = st.LoadIdentity()
	require.Error(t, err, "a key with no certificate is not an identity")

	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.NodeCertFile), []byte("rubbish"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.RootCertFile), []byte("rubbish"), 0o600))
	_, _, _, err = st.LoadIdentity()
	require.ErrorIs(t, err, edgeauth.ErrMalformed)
	_, _, err = st.Trust(time.Now())
	require.ErrorIs(t, err, edgeauth.ErrMalformed)

	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.DescriptorFile), []byte("{"), 0o600))
	_, _, err = st.Descriptor()
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.TrustFile), []byte("{"), 0o600))
	_, err = st.LoadTrust()
	require.Error(t, err)
	require.Error(t, st.Revoke("7", time.Now()))
}

func TestALocalAuthorityGeneratesOneRootAndKeepsThePrivateHalfAtHome(t *testing.T) {
	dir := t.TempDir()

	missing, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.False(t, ok, "not being an authority is not an error")
	require.Nil(t, missing)

	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	require.Equal(t, "shed", local.Descriptor().Where)
	require.Equal(t, edgeauth.KindLocal, local.Descriptor().Kind)
	require.Equal(t, edgeauth.LocalAccount, local.Account())
	require.True(t, local.Authority().Root().IsCA)
	require.Contains(t, local.RootPEM(), "BEGIN CERTIFICATE")
	require.NotContains(t, local.RootPEM(), "PRIVATE", "only the public half is ever published")

	keyPath := filepath.Join(dir, edgeauth.AuthorityDir, edgeauth.RootKeyFile)
	fi, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	_, err = edgeauth.Designate(dir, "another-shed")
	require.ErrorIs(t, err, edgeauth.ErrRootExists, "an Edge has exactly one root")

	reopened, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reopened.Authority().Root().Equal(local.Authority().Root()))
}

func TestALocalAuthorityIssuesOnlyToMachinesTheOwnerAllowed(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)

	_, user := keypair(t)
	userHex := hex.EncodeToString(user.Public().(ed25519.PublicKey))
	req, _ := signedRequest(t, "", user)

	_, err = local.Issue(req)
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine,
		"no internet is not a reason to let any machine on the LAN mint an identity")

	require.NoError(t, local.Allow(userHex))
	require.NoError(t, local.Allow(userHex), "allowing twice is not an error")
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Equal(t, []string{userHex}, allowed)

	// A NEW request: a signed one is spent the moment it is presented, refused or not,
	// so the owner's next attempt is a fresh ask rather than a replay of the last.
	next, _ := signedRequest(t, "", user)
	resp, err := local.Issue(next)
	require.NoError(t, err)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)

	// The serial is remembered across a restart, so forgetting the node later can
	// revoke exactly that certificate.
	restarted, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	serial, err := restarted.Revoke(resp.NodeID)
	require.NoError(t, err)
	require.Equal(t, leaf.SerialNumber.String(), serial)
	require.Contains(t, restarted.Revocations(), serial)

	none, err := restarted.Revoke("n_never-issued-here")
	require.NoError(t, err)
	require.Empty(t, none, "there is nothing to revoke for a node this authority never issued to")

	// And it survives ANOTHER restart: a revocation that lives only in the process that
	// made it is undone by the next launch.
	again, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, again.Revocations(), serial)
	require.True(t, again.Authority().SerialRevoked(serial))
}

func TestALocalAuthorityRefusesRubbishItFinds(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, edgeauth.AuthorityDir)
	require.NoError(t, os.MkdirAll(authDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(authDir, edgeauth.RootKeyFile), []byte("nope"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(authDir, edgeauth.RootCertFile), []byte("nope"), 0o600))
	_, _, err := edgeauth.OpenLocal(dir)
	require.Error(t, err)

	// A key with no certificate beside it is a half-configured root, and generating one
	// instead would issue under a root nobody chose.
	only := t.TempDir()
	onlyAuth := filepath.Join(only, edgeauth.AuthorityDir)
	require.NoError(t, os.MkdirAll(onlyAuth, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(onlyAuth, edgeauth.RootKeyFile), []byte("x"), 0o600))
	_, _, err = edgeauth.OpenLocal(only)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist) || err != nil)
}

func TestTheDescriptorAndTheIssuerAreReadableBackFromDisk(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	want := edgeauth.Descriptor{Kind: edgeauth.KindLocal, Where: "shed", Fingerprint: "ff00"}
	require.NoError(t, st.SaveDescriptor(want))
	got, stored, err := st.Descriptor()
	require.NoError(t, err)
	require.True(t, stored)
	require.Equal(t, want, got)

	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	require.NotNil(t, local.Issuer())
	require.True(t, local.Issuer().Root().Equal(local.Authority().Root()))
	require.Empty(t, local.Revocations())
}
