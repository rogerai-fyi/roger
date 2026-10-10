// Package joincode is the one-time join code and its stretched proof, shared by every local join:
// a device joining an Edge and a client or Station joining a Tower (features/join/invite.feature,
// features/edge/enrollment_root_trust.feature).
//
// A code is Crockford base32 from the operating system's secure random source. A proof is an
// HMAC-SHA256, keyed by the code stretched with PBKDF2, over the domain string, the domain's
// fields and the request's nonce. The domain string separates the uses, so a proof made for one
// can never verify as the other.
//
// The Edge domain salts the stretch with each request's nonce (the joiner verifies the authority,
// which holds the code); its derivation is pinned byte for byte by the Edge golden vectors the
// phone shares. The Tower domain salts the stretch with the domain alone, so the Tower keeps only
// that stretched value (Verifier) and never the code: a client proves it holds the code, and a
// 128-bit code leaves nothing to search.
//
// This package imports nothing from the rest of roger, so a Core-free package may use it.
package joincode

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Domain is one use of a join code.
type Domain struct {
	// Name is the proof domain string, the first line of every MAC'd message.
	Name string
	// CodeLen is the code's length in Crockford symbols (5 bits each).
	CodeLen int
	// Group is how many symbols a group shows when formatted (0 = no grouping).
	Group int
	// Lifetime is how long a minted code stays live.
	Lifetime time.Duration
	// Iterations is the PBKDF2 work factor.
	Iterations int
	// SaltWithNonce salts the stretch with each request's nonce instead of the domain alone.
	SaltWithNonce bool
}

// Version prefixes a proof on the wire, so a checker refuses one it does not know.
const Version = "v1"

// Edge is the Edge enrollment code (rulings D2-D4 of enrollment_root_trust.feature).
var Edge = Domain{
	Name: "rogerai-edge-root-proof/v1", CodeLen: 10, Group: 5,
	Lifetime: 15 * time.Minute, Iterations: 200000, SaltWithNonce: true,
}

// EdgeJoin is the proof a device sends WITH a key-less ("bearer") Edge invite's code, so the
// authority can admit the key that holds it (features/join/invite.feature, ruling 2026-10-10). It
// keeps the Edge code's format and stretch; its own name keeps it from ever verifying as the
// authority's root proof, or the reverse.
var EdgeJoin = Domain{
	Name: "rogerai-edge-join/v1", CodeLen: 10, Group: 5,
	Lifetime: 15 * time.Minute, Iterations: 200000, SaltWithNonce: true,
}

// Tower is the Tower join code: at least 128 bits (modes.feature:194), pasted or scanned rather
// than typed, so it carries no grouping requirement beyond readability.
var Tower = Domain{
	Name: "rogerai-tower-join/v1", CodeLen: 26, Group: 0,
	Lifetime: 15 * time.Minute, Iterations: 200000,
}

// Alphabet is Crockford's base32, the symbols every code is drawn from.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	// ErrBadCode is input that is not a join code.
	ErrBadCode = errors.New("that is not a join code")
	// ErrNoProof is an answer or request that carried no proof.
	ErrNoProof = errors.New("no proof was given for the code")
	// ErrProofVersion is a proof in a version this roger does not know.
	ErrProofVersion = errors.New("the proof is in a version this roger does not know")
	// ErrProofMismatch is a proof that does not verify.
	ErrProofMismatch = errors.New("the code does not match")
)

// NewCode draws a fresh canonical code: one symbol per byte, masked to five bits, so every
// symbol is exactly uniform.
func (d Domain) NewCode() string {
	raw := make([]byte, d.CodeLen)
	if _, err := rand.Read(raw); err != nil {
		panic("joincode: the system random source failed: " + err.Error())
	}
	b := make([]byte, d.CodeLen)
	for i, r := range raw {
		b[i] = Alphabet[r&0x1f]
	}
	return string(b)
}

// Format shows a canonical code in its groups.
func (d Domain) Format(code string) string {
	if d.Group <= 0 || len(code) != d.CodeLen {
		return code
	}
	var sb strings.Builder
	for i := 0; i < len(code); i += d.Group {
		if i > 0 {
			sb.WriteByte('-')
		}
		sb.WriteString(code[i:min(i+d.Group, len(code))])
	}
	return sb.String()
}

// Canonical reads a code however it was transcribed: case-folded, with Crockford's confusables
// (I and L read as 1, O as 0), hyphens and spaces dropped. Anything else, or the wrong length,
// is refused.
func (d Domain) Canonical(typed string) (string, error) {
	var sb strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(typed)) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if !strings.ContainsRune(Alphabet, r) {
			return "", fmt.Errorf("%w: %q is not a code symbol (a code is %d symbols)", ErrBadCode, r, d.CodeLen)
		}
		sb.WriteRune(r)
	}
	if sb.Len() != d.CodeLen {
		return "", fmt.Errorf("%w: a code is %d symbols", ErrBadCode, d.CodeLen)
	}
	return sb.String(), nil
}

func (d Domain) stretch(code, nonce string) []byte {
	salt := d.Name
	if d.SaltWithNonce {
		salt += "\n" + nonce
	}
	k, err := pbkdf2.Key(sha256.New, code, []byte(salt), d.Iterations, 32)
	if err != nil {
		panic("joincode: pbkdf2: " + err.Error()) // only for impossible parameters
	}
	return k
}

func (d Domain) mac(key []byte, nonce string, fields []string) []byte {
	m := hmac.New(sha256.New, key)
	parts := append(append([]string{d.Name}, fields...), nonce)
	m.Write([]byte(strings.Join(parts, "\n")))
	return m.Sum(nil)
}

// Proof is the proof that whoever made it holds code, bound to fields and this request's nonce.
// code must be canonical.
func (d Domain) Proof(code, nonce string, fields ...string) string {
	return Version + ":" + hex.EncodeToString(d.mac(d.stretch(code, nonce), nonce, fields))
}

// Verifier is what a holder that must not keep the code stores in its place. Only meaningful for
// a domain that does not salt with the nonce.
func (d Domain) Verifier(code string) []byte {
	if d.SaltWithNonce {
		panic("joincode: a nonce-salted domain has no stored verifier")
	}
	return d.stretch(code, "")
}

// Check verifies a proof against a typed code.
func (d Domain) Check(typedCode, proof, nonce string, fields ...string) error {
	code, err := d.Canonical(typedCode)
	if err != nil {
		return err
	}
	return d.check(d.stretch(code, nonce), proof, nonce, fields)
}

// CheckWithVerifier verifies a proof against a stored Verifier.
func (d Domain) CheckWithVerifier(verifier []byte, proof, nonce string, fields ...string) error {
	if d.SaltWithNonce {
		return ErrProofMismatch
	}
	return d.check(verifier, proof, nonce, fields)
}

func (d Domain) check(key []byte, proof, nonce string, fields []string) error {
	if proof == "" {
		return ErrNoProof
	}
	ver, mac, ok := strings.Cut(proof, ":")
	if !ok || ver != Version {
		return ErrProofVersion
	}
	got, err := hex.DecodeString(mac)
	if err != nil || !hmac.Equal(got, d.mac(key, nonce, fields)) {
		return ErrProofMismatch
	}
	return nil
}
