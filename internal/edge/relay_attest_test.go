package edge

// Only the Edge root can attest a relay (phone_consume.feature, "only the authority can relay"):
// an attestation over the right face and the right phone signature, signed by any other key - a
// member's own, another Edge's root - is refused exactly like no attestation at all.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

func TestARelayAttestationSignedByAnyKeyButTheRootIsRefused(t *testing.T) {
	root, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	memberKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	const face, phoneSig = "n_jetson", "aa11"
	sum := sha256.Sum256(RelayCanonical(face, phoneSig))
	header := func(sig []byte) http.Header {
		h := http.Header{}
		h.Set(HeaderSig, phoneSig)
		h.Set(HeaderRelay, base64.StdEncoding.EncodeToString(sig))
		return h
	}
	sign := func(k *ecdsa.PrivateKey) []byte {
		s, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
		require.NoError(t, err)
		return s
	}
	rootKey, ok := root.RootKey().(*ecdsa.PrivateKey)
	require.True(t, ok, "the Edge root is an ECDSA key")
	otherKey, ok := other.RootKey().(*ecdsa.PrivateKey)
	require.True(t, ok)

	require.True(t, verifyRelay(root.Root(), face, header(sign(rootKey))), "the root's own attestation holds")
	require.False(t, verifyRelay(root.Root(), face, header(sign(memberKey))), "a member's key")
	require.False(t, verifyRelay(root.Root(), face, header(sign(otherKey))), "another Edge's root")
	require.False(t, verifyRelay(root.Root(), "n_nas", header(sign(rootKey))), "the root's attestation for another face")
}
