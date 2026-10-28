package localplane

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
)

// newEphemeral is a fresh X25519 public key for one pairing, hex. The private half is not kept:
// the phrase covers the public keys, and nothing is derived from the shared secret.
func newEphemeral() (string, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k.PublicKey().Bytes()), nil
}
