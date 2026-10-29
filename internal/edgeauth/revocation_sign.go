package edgeauth

// THE REVOCATION LIST IS SIGNED BY THE EDGE'S ROOT (features/edge/revocation.feature, approved
// 2026-10-03). A member fetches the list over the LAN; the root's signature over the serials and the
// time they were signed is what lets it tell a real list from a forged one, with no extra key.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SignedRevocationsList is the revocation list as an authority serves it, signed.
type SignedRevocationsList struct {
	Revoked []string `json:"revoked"`
	At      int64    `json:"at"`  // when it was signed, unix seconds
	Sig     []byte   `json:"sig"` // the root's ECDSA signature over revocationsCanonical
}

// revocationsCanonical is the exact bytes signed: a domain label, the time, and the serials sorted -
// the order they travel in is not part of what was signed.
func revocationsCanonical(revoked []string, at int64) []byte {
	s := append([]string(nil), revoked...)
	sort.Strings(s)
	return []byte("rogerai-edge-revocations/v1\n" + strconv.FormatInt(at, 10) + "\n" + strings.Join(s, "\n"))
}

// SignRevocationList signs a list with this authority's root.
func (l *Local) SignRevocationList(revoked []string, at time.Time) (SignedRevocationsList, error) {
	key := l.auth.RootKey()
	if key == nil {
		return SignedRevocationsList{}, fmt.Errorf("this authority holds no root key to sign with")
	}
	sum := sha256.Sum256(revocationsCanonical(revoked, at.Unix()))
	sig, err := key.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		return SignedRevocationsList{}, err
	}
	return SignedRevocationsList{Revoked: revoked, At: at.Unix(), Sig: sig}, nil
}

// SignedRevocations is this authority's current revocation list, signed now.
func (l *Local) SignedRevocations(at time.Time) (SignedRevocationsList, error) {
	revoked, err := l.Revocations()
	if err != nil {
		return SignedRevocationsList{}, err
	}
	return l.SignRevocationList(revoked, at)
}

// VerifyRevocations checks a signed list against the root a member enrolled under. Not one serial of a
// list that fails is to be used.
func VerifyRevocations(root *x509.Certificate, s SignedRevocationsList) error {
	pub, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok || len(s.Sig) == 0 {
		return ErrBadSignature
	}
	sum := sha256.Sum256(revocationsCanonical(s.Revoked, s.At))
	if !ecdsa.VerifyASN1(pub, sum[:], s.Sig) {
		return ErrBadSignature
	}
	return nil
}
