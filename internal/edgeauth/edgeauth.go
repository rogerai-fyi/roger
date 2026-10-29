// Package edgeauth is the trust root of a Roger Edge: who signs a node's certificate,
// and everything a node needs in order to ask for one, check the answer, keep it, and
// verify its peers with it.
//
// Contract: features/edge/enrollment.feature.
//
// ONE EDGE, ONE ROOT, AND THE OWNER CHOOSES WHERE IT LIVES.
//
//	CORE AUTHORITY (the default, zero setup). Core issues, exactly as a Tower already
//	enrolls. It costs one online moment per node, and nothing after that.
//
//	LOCAL AUTHORITY (the airgap answer). The owner designates one machine. It generates
//	the Edge root, keeps the private half where it was made, and issues certificates
//	over the LAN. CORE IS NEVER CONTACTED, NOT ONCE.
//
// ONE CERTIFICATE SHAPE AND ONE VERIFICATION PATH EITHER WAY. Both authorities are the
// same *towercore/cert.Authority, issuing the same template through the same code, and
// edge.VerifyPeer takes that authority without ever learning which kind it was handed.
// A node cannot tell, and must not care, how its peer was signed.
//
// CORE-FREE BY CONSTRUCTION. This package makes no network call and CANNOT make one:
// it links neither net/http nor any Core-dialing package, and structural_test.go fails
// the build if that ever stops being true. The wire lives one directory down in
// enrollhttp, so "the local authority never contacts Core" is a property of the
// linkage rather than a promise in a comment - the same guarantee, and the same kind
// of test, that cmd/roger-tower-local already earns.
//
// THE ROOT DOES NOT TRAVEL. Only the PUBLIC root is ever distributed. A node's trust
// store holds a VERIFY-ONLY authority (cert.NewVerifier): it can check every peer of
// this Edge and it cannot issue anything, because it holds no private half to issue
// with.
package edgeauth

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// Kind is where this Edge's root lives.
type Kind string

const (
	// KindCore: Core issues. The default, and the one that needs the network.
	KindCore Kind = "core"
	// KindLocal: a machine the owner designated issues, over the LAN, forever offline.
	KindLocal Kind = "local"
)

// The refusal reasons a node gives for an enrollment answer it will not apply. They are
// constants because they are contract: the owner reads them, and "this is not your
// certificate" and "this is not your authority" are different problems.
const (
	ReasonIdentityMismatch = "identity mismatch"
	ReasonWrongAccount     = "wrong account"
	ReasonCertExpired      = "certificate expired"
	ReasonUnknownAuthority = "unknown authority"
	ReasonMalformed        = "malformed response"
)

// Refusals a node makes about an answer it was given. Every one leaves the machine
// exactly as it was: nothing is written until all of them have passed.
var (
	ErrIdentityMismatch = errors.New(ReasonIdentityMismatch)
	ErrWrongAccount     = errors.New(ReasonWrongAccount)
	ErrCertExpired      = errors.New(ReasonCertExpired)
	ErrUnknownAuthority = errors.New(ReasonUnknownAuthority)
	ErrMalformed        = errors.New(ReasonMalformed)
)

// FreshnessWindow is how long a node's copy of the revocation list may go unrefreshed
// before the fleet view calls it stale. A day: long enough that an Edge on a plant
// network is not nagged hourly, short enough that "this trust information is a month
// old" can never be shown as if it were current.
const FreshnessWindow = 24 * time.Hour

// Descriptor is what roots this Edge, in the words the owner is shown.
type Descriptor struct {
	Kind Kind `json:"kind"`
	// Where names the designated machine for a local authority; empty for Core.
	Where string `json:"where,omitempty"`
	// Fingerprint is the SHA-256 of the PUBLIC root, so two machines can tell at a
	// glance whether they are on the same Edge.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Names the authority the way the owner asked to see it.
func (d Descriptor) Names() string {
	if d.Kind == KindLocal {
		if d.Where == "" {
			return "the designated machine"
		}
		return fmt.Sprintf("the designated machine %q", d.Where)
	}
	return "Core"
}

// NeedsNetwork reports whether enrolling a NEW node under this authority needs the
// network. Core does; a local authority needs nothing beyond the LAN it is on.
func (d Descriptor) NeedsNetwork() bool { return d.Kind != KindLocal }

// NetworkLine is the second thing `roger edge authority` prints: the owner's real
// question is not "who signs" but "can I add a machine here, right now, offline".
func (d Descriptor) NetworkLine() string {
	if d.NeedsNetwork() {
		return "enrolling a new node needs the network, to reach Core"
	}
	return "enrolling a new node needs no network beyond this LAN"
}

// EncodeCert renders a certificate as PEM, which is how one crosses the wire and how
// one is kept on disk.
func EncodeCert(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

// DecodeCert parses a PEM certificate. A body that is not one is malformed, full stop:
// there is no partial reading of a credential.
func DecodeCert(s string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: that is not a PEM certificate", ErrMalformed)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return c, nil
}

// RootFingerprint identifies a root by the hash of its certificate. It is shown, and it
// is compared: an Edge has exactly one root, and this is how a second one is spotted.
func RootFingerprint(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
