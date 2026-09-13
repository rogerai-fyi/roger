package edge

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// The refusal reasons, verbatim. They are constants because they are contract: they
// appear in the discovery report an owner reads, and a mismatch on a home or plant
// network is either a misconfiguration or an attack - in both cases the owner has to be
// able to tell which one from the word.
const (
	ReasonFingerprintMismatch = "fingerprint mismatch"
	ReasonCertExpired         = "certificate expired"
	ReasonUnknownAuthority    = "unknown authority"
	ReasonIdentityMismatch    = "identity mismatch"
	ReasonIdentityCollision   = "identity collision"
	ReasonRevoked             = "revoked"
	ReasonUnreachable         = "unreachable"
)

// Peer is what a dial LEARNED: the certificate the peer actually served, its hash, and
// the description it gave over that channel.
type Peer struct {
	Cert        *x509.Certificate
	Fingerprint string
	Describe    Describe
}

// DialFunc goes and looks. It is a seam only so a test can drive an unreachable port
// deterministically; the production implementation is DialTLS, and every scenario in
// the spec runs against a real TLS listener.
type DialFunc func(ctx context.Context, addr string) (*Peer, error)

// dialTimeout bounds a single dial. A peer that advertises and then does not answer
// must cost one timeout, once, not a hung browse.
const dialTimeout = 3 * time.Second

// DialTLS connects to an advertised address, captures the certificate the peer really
// serves, and asks it to describe itself.
//
// It sets InsecureSkipVerify and does the verification itself, which is the correct
// thing here rather than a shortcut: the standard chain check answers "is this a
// certificate a browser would accept for this HOSTNAME", and the question on a LAN is
// "is this the certificate the advertisement promised, from this Edge's authority, for
// this node id". VerifyPeer below is that question. Nothing is believed from the
// connection until it has been asked.
func DialTLS(ctx context.Context, addr string) (*Peer, error) {
	var leaf *x509.Certificate
	tr := &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			d := &tls.Dialer{Config: &tls.Config{
				InsecureSkipVerify: true, // verified by VerifyPeer, against the pin
				MinVersion:         tls.VersionTLS12,
			}}
			c, err := d.DialContext(ctx, network, a)
			if err != nil {
				return nil, err
			}
			state := c.(*tls.Conn).ConnectionState()
			if len(state.PeerCertificates) == 0 {
				_ = c.Close()
				return nil, fmt.Errorf("the peer presented no certificate")
			}
			leaf = state.PeerCertificates[0]
			return c, nil
		},
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+DescribePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("describe answered %d", resp.StatusCode)
	}
	var d Describe
	// A bounded read: a peer we have not authenticated yet does not get to choose how
	// much memory its answer costs.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	return &Peer{Cert: leaf, Fingerprint: FingerprintOf(leaf), Describe: d}, nil
}

// VerifyPeer decides whether a dialed peer is who its advertisement claimed. It returns
// "" when the peer checks out, and one of the reason constants when it does not.
//
// The order is deliberate and is the security core of this feature:
//
//  1. THE FINGERPRINT FIRST. The advertisement is unauthenticated, so the only thing it
//     is good for is committing the advertiser to a certificate before we look. If the
//     certificate served does not hash to the fingerprint advertised, we are done: a
//     spoofed record has cost one dial and nothing else.
//  2. VALIDITY. Checked here rather than left to the chain verification, because
//     "expired" and "not from your authority" are different problems for the owner and
//     collapsing them into one message wastes the report.
//  3. THE AUTHORITY. The account's own CA, via the existing towercore/cert machinery.
//  4. THE IDENTITY. A certificate this authority really signed, for a DIFFERENT node,
//     is the interesting failure - it chains correctly and is still wrong.
//  5. THE PIN. For a node already on this Edge, the certificate we accepted before is
//     the one we accept now. A returning node with a different certificate does not
//     silently take its own place.
func VerifyPeer(ad Advert, p *Peer, auth *cert.Authority, pin string, now time.Time) string {
	if p == nil || p.Cert == nil {
		return ReasonUnreachable
	}
	if p.Fingerprint != ad.Fingerprint {
		return ReasonFingerprintMismatch
	}
	if now.Before(p.Cert.NotBefore) || now.After(p.Cert.NotAfter) {
		return ReasonCertExpired
	}
	if auth == nil {
		return ReasonUnknownAuthority
	}
	// REVOCATION, before the chain check, because the two are different problems and
	// the owner has to be able to tell them apart. The authority refuses a revoked
	// serial inside Authenticate as well, but as "this did not come from your
	// authority" - and "the machine you took off this Edge is still on your network"
	// deserves its own word.
	if p.Cert.SerialNumber != nil && auth.SerialRevoked(p.Cert.SerialNumber.String()) {
		return ReasonRevoked
	}
	id, err := auth.Authenticate(p.Cert)
	if err != nil {
		// Everything the authority refuses that is not expiry (checked above) or a
		// wrong identity (checked below) is a chain problem: an unknown issuer, a
		// self-signed certificate, a revoked serial, a credential shaped for some
		// other channel. They are all "this did not come from your authority".
		return ReasonUnknownAuthority
	}
	if id != ad.NodeID || (p.Describe.NodeID != "" && p.Describe.NodeID != ad.NodeID) {
		return ReasonIdentityMismatch
	}
	// 5. THE KEY ITSELF. A node id IS the hash of the node's public key, so a
	//    certificate that names this node while binding a DIFFERENT key is a misissue -
	//    the one failure that chains correctly and is still wrong. Deriving the id from
	//    the key catches it without needing to have met the node before, which the
	//    fingerprint pin does not.
	//
	//    It is also what makes the pin survive RENEWAL. Certificates are deliberately
	//    short-lived, so a fleet whose members stopped verifying every time one was
	//    reissued would be a fleet that breaks on schedule. What a peer is really
	//    pinning is the node's KEY: a renewed certificate is new paper for the same
	//    identity, and a returning node with a new KEY is a different node.
	if pub, ok := p.Cert.PublicKey.(ed25519.PublicKey); ok {
		if NodeID(pub) != ad.NodeID {
			return ReasonIdentityMismatch
		}
		return ""
	}
	// A key shape no node id can be derived from is not one of ours; fall back to the
	// certificate the owner actually accepted.
	if pin != "" && p.Fingerprint != pin {
		return ReasonIdentityMismatch
	}
	return ""
}
