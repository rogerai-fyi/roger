package cert

// THE AUTHORITY'S SERVER CERTIFICATE (docs/edge-authority-https.md; founder ruling 2026-10-13). A
// phone cannot do mutual TLS, so it proves itself by signing each request; this certificate is what
// proves the AUTHORITY to the phone, over a TLS listener the phone checks against the Edge root it
// already holds.
//
// It is unlike every certificate Issue mints, on purpose:
//   - a fresh P-256 key, generated here and held only in memory by the caller (iOS's TLS stack
//     handshakes with P-256 server keys; it is not known to with Ed25519);
//   - Extended Key Usage ServerAuth ONLY. Issue mints ClientAuth only, and Authenticate refuses
//     anything else, so this certificate can never be presented as a member, and no member
//     certificate can ever serve as the authority;
//   - the authority node's identity as its one URI SAN (the same spiffe form a member carries), and
//     an IP SAN for each LAN address it answers on; no DNS names;
//   - a short life: the caller reissues it, and never persists it.
// The chain is exactly leaf -> root: the root's MaxPathLen is 0, so no intermediate can appear.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// ServerCertTTL is how long the authority's server certificate lives before it must be reissued.
const ServerCertTTL = 24 * time.Hour

// IssueServer mints the authority's TLS server certificate for nodeID, valid for ips, living ttl.
func (a *Authority) IssueServer(nodeID string, ips []net.IP, ttl time.Duration) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if a.key == nil {
		return nil, nil, errors.New("this authority holds no root private key, so it can verify but never issue")
	}
	if !validTowerID(nodeID) {
		return nil, nil, fmt.Errorf("%q is not a usable node id", nodeID)
	}
	if len(ips) == 0 {
		return nil, nil, errors.New("a server certificate names at least one address")
	}
	if ttl <= 0 {
		ttl = ServerCertTTL
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(TowerURI(nodeID).String())
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: nodeID},
		URIs:                  []*url.URL{u},
		IPAddresses:           append([]net.IP(nil), ips...),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.root, &key.PublicKey, a.key)
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return leaf, key, nil
}

// VerifyServer is the check a phone makes (here so Go can prove the same leaf passes it): the leaf
// chains to root, is in date at `at`, carries ServerAuth and only ServerAuth, names authorityID as
// its one URI SAN, and names ip among its IP SANs.
func VerifyServer(root, leaf *x509.Certificate, authorityID string, ip net.IP, at time.Time) error {
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("that server certificate does not chain to this Edge's root: %w", err)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || leaf.IsCA {
		return errors.New("that certificate is not an authority's server certificate")
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != TowerURI(authorityID).String() {
		return errors.New("that server certificate names another node")
	}
	for _, a := range leaf.IPAddresses {
		if a.Equal(ip) {
			return nil
		}
	}
	return errors.New("that server certificate does not name the address dialed")
}
