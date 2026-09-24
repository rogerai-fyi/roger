package edgeauth_test

// The refusal paths of the on-disk store and the local authority: a store that cannot be
// written says so and leaves nothing half-written; rubbish on disk is an error, never an
// empty Edge; a second root is never minted over a first.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// fileAsDir returns a path that IS a file, so every MkdirAll under it fails.
func fileAsDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	return filepath.Join(p, "edge")
}

func TestEveryStoreWriteReportsAnUnwritableDirectory(t *testing.T) {
	st := edgeauth.Store{Dir: fileAsDir(t)}
	require.Error(t, st.SaveAccount("acct-1"))
	require.Error(t, st.SaveDescriptor(edgeauth.Descriptor{Kind: edgeauth.KindCore}))
	require.Error(t, st.SaveTrust([]string{"1"}, time.Now()))
	require.Error(t, st.Revoke("1", time.Now()))

	a := authority(t)
	pub, priv := keypair(t)
	leaf, err := a.Issue(edgeauth.NodeID(pub), pub)
	require.NoError(t, err)
	id := &edgeauth.Identity{NodeID: leaf.Subject.CommonName, Cert: leaf, Root: a.Root(),
		CertPEM: edgeauth.EncodeCert(leaf), RootPEM: edgeauth.EncodeCert(a.Root())}
	require.Error(t, st.SaveIdentity(id, priv, edgeauth.Descriptor{Kind: edgeauth.KindCore}))
	require.Error(t, st.SaveIdentity(nil, priv, edgeauth.Descriptor{}), "an identity is a certificate AND its key")
	require.Error(t, st.SaveIdentity(id, priv[:5], edgeauth.Descriptor{}))
}

func TestRubbishOnDiskIsAnErrorNotAnEmptyEdge(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}

	write(edgeauth.TrustFile, "{not json")
	_, err := st.LoadTrust()
	require.Error(t, err)
	require.Error(t, st.Revoke("1", time.Now()), "a revocation over an unreadable list is refused, not appended blindly")

	write(edgeauth.DescriptorFile, "{not json")
	_, _, err = st.Descriptor()
	require.Error(t, err)

	// Trust: no root at all, then a root that is not PEM, then a good root over a bad list.
	require.NoError(t, os.Remove(filepath.Join(dir, edgeauth.TrustFile)))
	_, _, err = st.Trust(time.Now())
	require.ErrorContains(t, err, "holds no Edge root")
	write(edgeauth.RootCertFile, "not a certificate")
	_, _, err = st.Trust(time.Now())
	require.Error(t, err)
	a := authority(t)
	write(edgeauth.RootCertFile, edgeauth.EncodeCert(a.Root()))
	write(edgeauth.TrustFile, "{not json")
	_, _, err = st.Trust(time.Now())
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, edgeauth.TrustFile)))
	auth, _, err := st.Trust(time.Now())
	require.NoError(t, err)
	require.Nil(t, auth.RootKey(), "the trust store is verify-only")

	// LoadIdentity: a key that is not hex, then a good key with no certificate beside it.
	write(edgeauth.NodeKeyFile, "zz")
	_, _, _, err = st.LoadIdentity()
	require.ErrorContains(t, err, "unreadable")
	_, priv := keypair(t)
	write(edgeauth.NodeKeyFile, hexOf(priv))
	_, _, _, err = st.LoadIdentity()
	require.Error(t, err, "a key with no certificate is half an identity")
	write(edgeauth.NodeCertFile, "not a certificate")
	_, _, _, err = st.LoadIdentity()
	require.Error(t, err)
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&15])
	}
	return string(out)
}

func TestALocalAuthorityIsMintedOnceAndRefusesWhatItCannotWrite(t *testing.T) {
	edgeDir := t.TempDir()
	l, err := edgeauth.Designate(edgeDir, "shed")
	require.NoError(t, err)
	_, err = edgeauth.Designate(edgeDir, "shed-again")
	require.ErrorIs(t, err, edgeauth.ErrRootExists)

	_, err = edgeauth.Designate(fileAsDir(t), "shed")
	require.Error(t, err, "a directory that cannot be made is a refusal, not a root")

	// Revoking a node this authority never issued to is not an error and revokes nothing.
	serials, err := l.Revoke("n_never")
	require.NoError(t, err)
	require.Empty(t, serials)

	// Allowing the same key twice keeps one entry.
	require.NoError(t, l.Allow("ab"))
	require.NoError(t, l.Allow("ab"))
	got, err := l.Allowed()
	require.NoError(t, err)
	count := 0
	for _, k := range got {
		if k == "ab" {
			count++
		}
	}
	require.Equal(t, 1, count)

	// Rubbish beside the root makes the authority unreadable, not empty.
	authDir := filepath.Join(edgeDir, edgeauth.AuthorityDir)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, edgeauth.IssuedFile), []byte("{nope"), 0o600))
	_, _, err = edgeauth.OpenLocal(edgeDir)
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(authDir, edgeauth.IssuedFile)))
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "revoked.json"), []byte("{nope"), 0o600))
	_, _, err = edgeauth.OpenLocal(edgeDir)
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(authDir, "revoked.json")))
	_, ok, err := edgeauth.OpenLocal(edgeDir)
	require.NoError(t, err)
	require.True(t, ok)

	// A key file that cannot be stat'ed (a file where the authority dir should be).
	bad := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bad, edgeauth.AuthorityDir), []byte("x"), 0o600))
	_, _, err = edgeauth.OpenLocal(bad)
	require.Error(t, err)
}
