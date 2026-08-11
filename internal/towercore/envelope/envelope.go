// Package envelope makes the content a Tower relays opaque to it.
//
// # THE PROPERTY
//
// features/tower/job_and_settlement.feature: "packet capture and Tower logs reveal no prompt,
// tool argument, image, audio, transcript, or completion plaintext... only documented routing
// metadata, opaque ciphertext digests, timing, sizes, peer addresses, and error classes are
// observable."
//
// A Tower is somebody else's machine. It has to be able to CARRY a request to reach the
// Station behind it, and it must not be able to READ one. Integrity was already covered - the
// grant commits to a digest of the request and the receipt to a digest of the response, so a
// Tower that alters either is caught - but a relay that cannot alter your prompt and can still
// read it is not much comfort.
//
// # HOW
//
// Each direction is sealed to the RECIPIENT'S static X25519 key with a fresh ephemeral of the
// sender's, so neither end has to remember anything between the two legs:
//
//	Core     seals the request to the Station's SECURE-SESSION key, recorded at attachment
//	         and unused until now.
//	Station  opens it, executes, seals the result to CORE'S envelope key, which it pinned
//	         alongside the grant key.
//	Core     opens that.
//
// STATELESS ON PURPOSE, and this is the detail that made the first design wrong. A per-
// exchange session key would live in the memory of the broker that sealed the request - and
// the answer comes back to whichever broker the Tower happened to reach, which is very often
// a different one. Any instance holding Core's envelope key can open a response; none of them
// has to have been the one that sent the request.
//
// The Tower sees an ephemeral public key, a nonce and ciphertext, in both directions. It
// cannot derive either shared secret without a private key it does not hold and must never
// hold.
//
// # HOW THIS DIFFERS FROM THE SPEC, stated rather than glossed
//
// The spec describes the inner session as mutual TLS 1.3 between Core and the Station,
// tunnelled through the Tower. This is not that. It gives the same CONFIDENTIALITY property
// over the transport that exists today, and authentication is carried by the objects instead:
// the Station authenticates Core by the signature on the grant, and Core authenticates the
// Station by the signature on the receipt. What it does NOT give is a TLS-level channel
// binding, forward secrecy across a compromised Station session key, or the certificate
// machinery the spec's version has. A real inner TLS session is still the destination; this
// removes the plaintext today without pretending to be it.
package envelope

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"crypto/sha256"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// envelopeLabel domain-separates this key agreement from every other use of X25519 here.
const envelopeLabel = "rogerai tower envelope v1"

// Sealed is what a Tower carries: an ephemeral public key, a nonce, and ciphertext.
//
// Nothing here identifies the content. The digests a Tower is allowed to observe are of the
// SEALED bytes, and are computed by whoever needs them rather than carried in the clear.
type Sealed struct {
	// EphemeralKey is the one-use X25519 public key the request was sealed with. The response
	// leg reuses the same exchange, so it does not repeat it.
	EphemeralKey []byte `json:"epk,omitempty"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ct"`
}

// SealTo produces an envelope only the holder of recipient's private key can open.
//
// aad binds it to ONE attempt. Without that, a valid envelope for attempt A could be relayed
// as attempt B by a Tower holding both: the ciphertext would decrypt perfectly and the
// Station would serve the wrong request under the right authorization.
func SealTo(recipient []byte, plaintext []byte, aad string) (Sealed, error) {
	pub, err := parseX25519(recipient)
	if err != nil {
		return Sealed{}, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Sealed{}, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		// A low-order point drives the exchange to a known secret, so this is refused rather
		// than sealed to something the other side chose.
		return Sealed{}, errors.New("that recipient key cannot be used for key agreement")
	}
	sealed := seal(newAEAD(shared, aad), plaintext, aad)
	sealed.EphemeralKey = eph.PublicKey().Bytes()
	return sealed, nil
}

// OpenWith reads an envelope addressed to this private key.
func OpenWith(recipientPriv []byte, sealed Sealed, aad string) ([]byte, error) {
	priv, err := ecdh.X25519().NewPrivateKey(recipientPriv)
	if err != nil {
		return nil, errors.New("that is not an X25519 private key")
	}
	eph, err := parseX25519(sealed.EphemeralKey)
	if err != nil {
		return nil, fmt.Errorf("the envelope's ephemeral key is unusable: %w", err)
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, errors.New("that envelope's ephemeral key cannot be used for key agreement")
	}
	return unseal(newAEAD(shared, aad), sealed, aad)
}

// PublicKeyOf returns the public half of an X25519 private key.
func PublicKeyOf(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, errors.New("that is not an X25519 private key")
	}
	return k.PublicKey().Bytes(), nil
}

// NewKey mints an X25519 keypair.
func NewKey() (pub, priv []byte, err error) {
	k, gerr := ecdh.X25519().GenerateKey(rand.Reader)
	if gerr != nil {
		return nil, nil, gerr
	}
	return k.PublicKey().Bytes(), k.Bytes(), nil
}

func seal(aead cipher.AEAD, plaintext []byte, aad string) Sealed {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		// A predictable nonce is a broken AEAD, not a condition to carry on through: reusing
		// one under the same key reveals the xor of two plaintexts. The rest of this codebase
		// panics on crypto/rand for the same reason.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return Sealed{
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, []byte(aad)),
	}
}

func unseal(aead cipher.AEAD, sealed Sealed, aad string) ([]byte, error) {
	if len(sealed.Nonce) != aead.NonceSize() {
		return nil, errors.New("the envelope's nonce is the wrong size")
	}
	out, err := aead.Open(nil, sealed.Nonce, sealed.Ciphertext, []byte(aad))
	if err != nil {
		// One message for every failure: a wrong key, altered ciphertext and an envelope for
		// another attempt are all "this is not for me", and distinguishing them would tell a
		// relay which of its attempts got closest.
		return nil, errors.New("this envelope could not be opened")
	}
	return out, nil
}

// newAEAD derives one key and builds its cipher.
//
// Both failure modes here are INVARIANTS rather than conditions: HKDF cannot fail asking for
// 32 bytes, and ChaCha20-Poly1305 cannot fail on a 32-byte key. A panic says that plainly,
// where an error return would be a branch no input can reach and no test can cover honestly.
func newAEAD(shared []byte, aad string) cipher.AEAD {
	info := envelopeLabel + aad
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, []byte(info)), key); err != nil {
		panic("envelope: HKDF refused a 32-byte read: " + err.Error())
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		panic("envelope: ChaCha20-Poly1305 refused a 32-byte key: " + err.Error())
	}
	return aead
}

func parseX25519(raw []byte) (*ecdh.PublicKey, error) {
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, errors.New("that is not an X25519 public key")
	}
	return pub, nil
}

// Marshal and Unmarshal keep the wire shape in one place, so the Tower's relay and both ends
// agree on what an envelope looks like without any of them defining it.
func (s Sealed) Marshal() (json.RawMessage, error) { return json.Marshal(s) }

// Parse reads an envelope off the wire.
func Parse(raw json.RawMessage) (Sealed, error) {
	var s Sealed
	if err := json.Unmarshal(raw, &s); err != nil {
		return Sealed{}, errors.New("that is not an envelope")
	}
	if len(s.Ciphertext) == 0 || len(s.Nonce) == 0 {
		return Sealed{}, errors.New("that envelope carries nothing")
	}
	return s, nil
}
