package capsule

// transport.go is the CLIENT half of the encrypted stranger transport (Stage 3): it seals a
// signed, redacted capsule under a one-time CODE and opens it with the same code. The broker
// (cmd/rogerai-broker/capsule.go) only ever stores {lookup, ciphertext} and does ZERO crypto.
//
// THE LOAD-BEARING CONTENT-BLIND INVARIANT: the encryption KEY is DOMAIN-SEPARATED from the
// broker LOOKUP. Both derive from the code, but:
//
//	lookup = BandCodeHash(code)                       = sha256(canonical tail)   [sent to broker]
//	key    = HKDF-SHA256(ikm=CanonicalBandTail(code),                            [never sent]
//	                     salt="rogerai-capsule-transport-v1",
//	                     info=BandCodeHash(code))[:32]
//
// The IKM is the SECRET tail; the lookup is a one-way hash of that tail. Knowing the lookup
// (all the broker holds) reveals neither the tail nor the key, so from {lookup, ciphertext}
// the plaintext is unrecoverable without the raw code. key != lookup by construction
// (transport_test.go pins this). The code REUSES the 40-bit RC/band tail - no new code format.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"golang.org/x/crypto/hkdf"
)

// transportSalt is the fixed HKDF salt, versioned so a future key-derivation change is a new
// namespace (an old blob never opens under a new scheme). It is NOT secret.
const transportSalt = "rogerai-capsule-transport-v1"

// transportKeyLen is the AES-256 key length.
const transportKeyLen = 32

var (
	// ErrNoCode rejects a seal/open whose code carries no valid Crockford tail (no key
	// material). Terse by design (public repo).
	ErrNoCode = errors.New("capsule: code has no valid tail")
	// ErrBadBlob rejects a sealed blob too short to carry a nonce + GCM tag, or one whose
	// authentication fails (wrong code / tamper). One error for both so open leaks nothing
	// about which failed.
	ErrBadBlob = errors.New("capsule: sealed blob invalid or wrong code")
	// ErrNotSummary rejects sealing a non-summary (full) capsule for a stranger: the
	// redaction floor. A marketplace/stranger handoff may only carry a summary-only capsule.
	ErrNotSummary = errors.New("capsule: refusing to seal a non-summary capsule for a stranger")
)

// SealForStranger enforces the redaction FLOOR before sealing: a capsule handed to a
// marketplace/stranger operator MUST be summary-only (redaction=="summary"). It refuses any
// full/other capsule (ErrNotSummary) before it touches the code, so a stranger transport can
// never carry a full transcript even if a caller forgets to redact. capsuleJSON is the signed
// wire object; the redaction level is signed, so this checks the same field the signature
// covers. On acceptance it seals under code exactly like SealForCode.
func SealForStranger(capsuleJSON []byte, code string) ([]byte, error) {
	var c Capsule
	if err := json.Unmarshal(capsuleJSON, &c); err != nil {
		return nil, err
	}
	if c.Redaction != "summary" {
		return nil, ErrNotSummary
	}
	return SealForCode(capsuleJSON, code)
}

// TransportLookup is the broker lookup key for a code: BandCodeHash(code) = sha256 over the
// canonical secret tail (hex). It is what the client sends to mint/resolve; it is DISTINCT
// from the encryption key (which HKDFs over the tail with this as info). An empty/tail-less
// code hashes the empty string, which never matches a minted blob.
func TransportLookup(code string) string { return protocol.BandCodeHash(code) }

// transportKey derives the 32-byte AES-256-GCM key from the code via HKDF-SHA256 over the
// canonical tail (the secret), domain-separated from the lookup by the salt+info. It returns
// nil for a code with no valid tail (SealForCode/OpenWithCode reject that as ErrNoCode).
func transportKey(code string) []byte {
	tail := protocol.CanonicalBandTail(code)
	if tail == "" {
		return nil
	}
	info := []byte(protocol.BandCodeHash(code)) // public; binds the key to this exact lookup
	r := hkdf.New(sha256.New, []byte(tail), []byte(transportSalt), info)
	key := make([]byte, transportKeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil // HKDF over sha256 never under-delivers 32 bytes; defensive only
	}
	return key
}

// newGCM builds the AES-256-GCM AEAD for a code, or an error for a tail-less code.
func newGCM(code string) (cipher.AEAD, error) {
	key := transportKey(code)
	if key == nil {
		return nil, ErrNoCode
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealForCode encrypts plaintext under the code with AES-256-GCM: a fresh random 12-byte
// nonce is PREPENDED to the ciphertext (mirroring report.go encryptCSAM), and the AAD is the
// broker lookup (BandCodeHash) so a blob cannot be spliced under a different code. Returns
// ErrNoCode for a code with no valid tail.
func SealForCode(plaintext []byte, code string) ([]byte, error) {
	gcm, err := newGCM(code)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := []byte(TransportLookup(code))
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// OpenWithCode reverses SealForCode: it splits the prepended nonce, then AES-256-GCM-opens
// the remainder with the code-derived key and the lookup AAD. Any failure (too-short blob,
// wrong code, tamper) returns ErrBadBlob and never panics - the plaintext is unrecoverable.
func OpenWithCode(blob []byte, code string) ([]byte, error) {
	gcm, err := newGCM(code)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns+gcm.Overhead() {
		return nil, ErrBadBlob // no room for a nonce + a GCM tag
	}
	nonce, ct := blob[:ns], blob[ns:]
	aad := []byte(TransportLookup(code))
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrBadBlob
	}
	return pt, nil
}
