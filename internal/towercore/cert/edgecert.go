package cert

// edgecert.go issues a STATION's edge TLS certificate: the one a consumer verifies when it
// connects through a Tower, and the one whose private key staying on the Station is what
// makes the relay blind.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY THIS IS A DIFFERENT ISSUANCE FROM Issue
//
// Issue mints a Tower's CLIENT-auth channel identity, named by a Tower URI. This mints a
// Station's SERVER-auth certificate, named by the DNS name a consumer dials - and a consumer
// verifies it with the ordinary web PKI path, so it must carry the name as a SAN and the
// server-auth usage. Folding the two into one function would invite a client cert to be
// accepted where a server cert was meant, which is exactly the confusion certificate usages
// exist to prevent.
//
// # WHY CORE ISSUES THE NAME
//
// The name lives under a domain Core controls, and the STATION does not choose it: a Station
// that named itself could request a certificate for another Station's name. The caller passes
// the name it wants and Core is the one that decides whether that name is this Station's to
// have - see the broker endpoint. Here the CA only signs what it is told, over a key the CSR
// proved possession of.

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// IssueEdgeCert signs a server certificate for a Station's relay name, over the public key in
// the request.
//
// It takes the name and key already extracted and checked by the caller rather than a raw
// CSR, because "is this the right name for this Station" is a question only the broker can
// answer (it holds the attachment) and the CA must not grow an opinion about attachment it
// would then have to keep in step. The CA's job is narrow: bind this name to this key under
// our root, as a server certificate, for a bounded life.
// validEdgeName is what IssueEdgeCert will sign: at least two dot-separated labels, each a
// plain DNS label, no wildcard. It is deliberately stricter than "a valid DNS name" - Core
// issues under a domain it controls, so a legitimate relay name is always several labels of
// [a-z0-9-], and anything else is a caller doing something it should not.
func validEdgeName(name string) bool {
	return edgeNamePattern.MatchString(name)
}

var edgeNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (a *Authority) IssueEdgeCert(relayName string, pub crypto.PublicKey) (*x509.Certificate, error) {
	if relayName == "" {
		return nil, errors.New("an edge certificate needs the relay name it is for")
	}
	// FAIL CLOSED ON A NAME THE CA WOULD NOT WANT TO SIGN, independently of whatever validated
	// the caller. A wildcard, a bare label, whitespace or a control character in a certificate
	// SAN is how one Station's certificate comes to cover another's name - the exact break a
	// review found upstream of here - so the CA refuses it even though the broker also should
	// never send one. Two gates, because the cost of the second is a regexp and the cost of a
	// mis-issued wildcard is the whole edge confidentiality model.
	if !validEdgeName(relayName) {
		return nil, fmt.Errorf("%q is not a name this authority will issue for", relayName)
	}
	if pub == nil {
		return nil, errors.New("an edge certificate must bind a public key")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: relayName},
		DNSNames:     []string{relayName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(a.cfg.TTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// SERVER auth, deliberately not client: this is a certificate a consumer connects TO,
		// and it must not be usable as a Tower's channel identity.
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.root, pub, a.key)
	if err != nil {
		return nil, fmt.Errorf("could not sign the edge certificate: %w", err)
	}
	return x509.ParseCertificate(der)
}
