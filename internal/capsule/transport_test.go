package capsule

// transport_test.go pins the client-side encrypted stranger transport (Stage 3): the
// HKDF-SHA256 key derivation, the AES-256-GCM seal/open, and - the load-bearing security
// claim - that the encryption KEY is DOMAIN-SEPARATED from the broker LOOKUP. Real crypto,
// no mocks. Whitebox (package capsule) so it can assert transportKey vs TransportLookup.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// testCode mints a plausible reused RC/band code (40-bit Crockford tail); the transport
// never invents a new code format.
func testCode(t *testing.T) string {
	t.Helper()
	code, _, tail := protocol.NewRCLinkCode()
	if tail == "" {
		t.Fatal("mint produced a codeless tail")
	}
	return code
}

func TestSealOpenRoundTrip(t *testing.T) {
	code := testCode(t)
	plain := []byte(`{"capsule":"roger.context.v1","redaction":"summary"}`)

	blob, err := SealForCode(plain, code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("empty blob")
	}
	if bytes.Contains(blob, plain) {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := OpenWithCode(blob, code)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

// TestKeyIsDomainSeparatedFromLookup is THE LOAD-BEARING CONTENT-BLIND INVARIANT: the AES
// key derived from the code must be domain-separated from the sha256 lookup the broker
// stores. The broker holds {lookup, ciphertext}; it must never derive the key from the
// lookup.
func TestKeyIsDomainSeparatedFromLookup(t *testing.T) {
	code := testCode(t)

	key := transportKey(code)       // the AES-256 key (client-only)
	lookup := TransportLookup(code) // what the broker stores/receives (hex sha256 tail)
	if len(key) != 32 {
		t.Fatalf("AES-256 key must be 32 bytes, got %d", len(key))
	}
	if lookup == "" {
		t.Fatal("empty lookup")
	}
	// key != lookup, in every representation.
	if hex.EncodeToString(key) == lookup {
		t.Fatal("key must not equal the lookup (hex)")
	}
	if bytes.Equal([]byte(lookup), key) {
		t.Fatal("key must not equal the lookup (bytes)")
	}
	// The lookup is exactly sha256(canonical tail); the key is HKDF over that tail. Knowing
	// the lookup (a hash) yields neither the tail nor the key.
	if lookup != protocol.BandCodeHash(code) {
		t.Fatalf("lookup must be BandCodeHash(code): got %q want %q", lookup, protocol.BandCodeHash(code))
	}
	// Prove the broker's stored pair {lookup, ciphertext} is undecryptable without the raw
	// code: opening with the lookup-as-code, or any non-code, fails.
	blob, err := SealForCode([]byte("secret-plaintext"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenWithCode(blob, lookup); err == nil {
		t.Fatal("the broker's lookup must not open the blob")
	}
	if _, err := OpenWithCode(blob, ""); err == nil {
		t.Fatal("an empty code must not open the blob")
	}
}

func TestWrongCodeNoPlaintext(t *testing.T) {
	code := testCode(t)
	other := testCode(t)
	if protocol.BandCodeHash(code) == protocol.BandCodeHash(other) {
		t.Fatal("two fresh codes collided (astronomically unlikely)")
	}
	blob, err := SealForCode([]byte("top secret"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenWithCode(blob, other); err == nil {
		t.Fatal("a different code must not decrypt (GCM auth fails)")
	}
}

func TestFlippedByteFailsGCM(t *testing.T) {
	code := testCode(t)
	blob, err := SealForCode([]byte("integrity-protected"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// flip a byte in the ciphertext body (past the 12-byte nonce) -> GCM tag mismatch.
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := OpenWithCode(tampered, code); err == nil {
		t.Fatal("a flipped ciphertext byte must fail the GCM tag")
	}
	// flip a nonce byte -> also fails.
	tampered2 := append([]byte(nil), blob...)
	tampered2[0] ^= 0x01
	if _, err := OpenWithCode(tampered2, code); err == nil {
		t.Fatal("a flipped nonce byte must fail")
	}
}

func TestTruncatedBlob(t *testing.T) {
	code := testCode(t)
	blob, err := SealForCode([]byte("x"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, n := range []int{0, 1, 11, 12} { // shorter than nonce+tag: never a valid frame
		if _, err := OpenWithCode(blob[:n], code); err == nil {
			t.Fatalf("a truncated blob (%d bytes) must be rejected, not panic", n)
		}
	}
}

// TestNonceRandomPerSeal: every seal uses a fresh random nonce, so two seals of the same
// plaintext+code differ (no deterministic ciphertext).
func TestNonceRandomPerSeal(t *testing.T) {
	code := testCode(t)
	blob, err := SealForCode([]byte("bound to this code"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	blob2, err := SealForCode([]byte("bound to this code"), code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(blob, blob2) {
		t.Fatal("nonce must be random per seal (no deterministic ciphertext)")
	}
}

func TestSealEmptyCodeRejected(t *testing.T) {
	if _, err := SealForCode([]byte("x"), ""); err == nil {
		t.Fatal("a codeless seal must be refused (no tail => no key)")
	}
	// only dashes/spaces/dots/middots (all dropped by CanonicalBandTail) => no tail.
	if _, err := SealForCode([]byte("x"), " ---... · --- "); err == nil {
		t.Fatal("a code with no valid tail must be refused")
	}
}

// TestKeyDeterministicPerCode: the derived key is a pure function of the code, so the guest
// re-derives the SAME key from the same code (a fresh process opens what another sealed).
func TestKeyDeterministicPerCode(t *testing.T) {
	code := testCode(t)
	if !bytes.Equal(transportKey(code), transportKey(code)) {
		t.Fatal("key derivation must be deterministic per code")
	}
	if bytes.Equal(transportKey(code), transportKey(testCode(t))) {
		t.Fatal("different codes must give different keys")
	}
}

// TestSealOpenLarge: a large capsule (near the broker's 1MB cap) still round-trips.
func TestSealOpenLarge(t *testing.T) {
	code := testCode(t)
	plain := make([]byte, 900*1024)
	_, _ = rand.Read(plain)
	blob, err := SealForCode(plain, code)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := OpenWithCode(blob, code)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("large round-trip mismatch")
	}
}
