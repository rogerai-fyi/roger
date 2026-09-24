package edgeauth

// EDGE GRANTS (features/edge/control.feature). An Edge grant is store.Grant SIGNED BY THE AUTHORITY's
// root key, so any member verifies it with the public root exactly as it verifies a certificate - no
// broker round trip, which is what an airgapped Edge requires. The signature covers the authorizing
// fields (owner, caller, callee nodes, capabilities, models, expiry, hops); it does not cover the
// secret, the rate limits, or timestamps. Verification is ECDSA over the root's P-256 key.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/store"
)

var (
	// ErrGrantUnsigned: an Edge grant with no signature is not a grant.
	ErrGrantUnsigned = errors.New("that grant is not signed by an authority")
	// ErrGrantBadSignature: the signature does not verify under this Edge's root.
	ErrGrantBadSignature = errors.New("that grant's signature does not check out under this Edge's root")
)

// GrantCanonical is the exact byte string the authority signs and a member verifies. It is
// deterministic (slices sorted) and excludes the signature, the secret and mutable bookkeeping.
func GrantCanonical(g store.Grant) []byte {
	nodes := append([]string(nil), g.Nodes...)
	caps := append([]string(nil), g.Caps...)
	models := append([]string(nil), g.Models...)
	sort.Strings(nodes)
	sort.Strings(caps)
	sort.Strings(models)
	fields := []string{
		"rogerai-edge-grant/v1",
		g.Owner,
		g.Caller,
		g.ID,
		strconv.Itoa(g.MaxHops),
		strconv.FormatBool(g.Free),
		strconv.FormatBool(g.Self),
		strconv.FormatInt(g.ExpiresAt, 10),
		strings.Join(nodes, ","),
		strings.Join(caps, ","),
		strings.Join(models, ","),
	}
	return []byte(strings.Join(fields, "\n"))
}

// SignGrant signs a grant with the authority's root key (ECDSA over the canonical bytes) and returns
// it with Sig set. The caller passes the authority's RootKey().
func SignGrant(g store.Grant, key crypto.Signer) (store.Grant, error) {
	if key == nil {
		return store.Grant{}, fmt.Errorf("no authority key to sign the grant")
	}
	h := sha256.Sum256(GrantCanonical(g))
	sig, err := key.Sign(rand.Reader, h[:], crypto.SHA256)
	if err != nil {
		return store.Grant{}, fmt.Errorf("that grant could not be signed: %w", err)
	}
	g.Sig = hex.EncodeToString(sig)
	return g, nil
}

// VerifyGrant checks a grant's signature under the given root certificate's public key. An unsigned
// grant is ErrGrantUnsigned; a bad or foreign signature is ErrGrantBadSignature.
func VerifyGrant(g store.Grant, root *x509.Certificate) error {
	if g.Sig == "" {
		return ErrGrantUnsigned
	}
	if root == nil {
		return ErrGrantBadSignature
	}
	sig, err := hex.DecodeString(g.Sig)
	if err != nil {
		return ErrGrantBadSignature
	}
	pub, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return ErrGrantBadSignature
	}
	h := sha256.Sum256(GrantCanonical(g))
	if !ecdsa.VerifyASN1(pub, h[:], sig) {
		return ErrGrantBadSignature
	}
	return nil
}
