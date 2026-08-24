package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNonceSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nonce := NewNonce()
	require.NotEmpty(t, nonce)

	pubHex, ts, sig := SignRequestNonce(priv, "POST", "/v1/chat/completions", []byte(`{"model":"m"}`), nonce)
	require.Equal(t, hexOf(pub), pubHex)

	// Verifies with the right nonce.
	uid, ok := VerifyRequestNonce(pubHex, sig, ts, "POST", "/v1/chat/completions", []byte(`{"model":"m"}`), nonce)
	require.True(t, ok)
	require.Equal(t, UserIDFromPubkey(pubHex), uid)

	// A DIFFERENT nonce, same signature, does not verify - the nonce is bound in.
	_, ok = VerifyRequestNonce(pubHex, sig, ts, "POST", "/v1/chat/completions", []byte(`{"model":"m"}`), NewNonce())
	require.False(t, ok, "a signature made for one nonce cannot be presented with another")

	// An empty nonce is refused.
	_, ok = VerifyRequestNonce(pubHex, sig, ts, "POST", "/v1/chat/completions", []byte(`{"model":"m"}`), "")
	require.False(t, ok)

	// The plain (V1) verifier does NOT accept a nonce-bound signature (different signed string).
	_, ok = VerifyRequest(pubHex, sig, ts, "POST", "/v1/chat/completions", []byte(`{"model":"m"}`))
	require.False(t, ok, "a nonce-bound signature is not a plain one - the plain path is unaffected")
}

func hexOf(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2], out[i*2+1] = h[c>>4], h[c&0xf]
	}
	return string(out)
}

func TestVerifyRequestNonceRejectsMalformedNonce(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = pub
	body := []byte(`{"model":"m"}`)
	for _, bad := range []string{"", "short", "ZZZZ", "0123456789abcdef0123456789abcdeX", "0123456789abcdef0123456789abcdef00"} {
		// Sign with the bad nonce, then verify with the same bad nonce - must be refused for shape.
		pubHex, ts, sig := SignRequestNonce(priv, "POST", "/p", body, bad)
		_, ok := VerifyRequestNonce(pubHex, sig, ts, "POST", "/p", body, bad)
		require.False(t, ok, "a nonce %q that is not 32 lowercase-hex must be refused", bad)
	}
	// isNonce accepts exactly a NewNonce value.
	require.True(t, isNonce(NewNonce()))
}
