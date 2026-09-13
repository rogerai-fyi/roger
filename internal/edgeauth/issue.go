package edgeauth

// issue.go is the SIGNING side, and it is the same code whether it runs inside Core or
// inside a shed on a network that has never had internet. That is not a coincidence,
// it is the requirement: one certificate shape, one issuing path, so a node can never
// tell which kind of authority signed its peer.

import (
	"crypto/x509"
	"errors"
	"fmt"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// Refusals the issuer makes. Each is worded for the party that will read it: the owner
// of the machine that asked. None of them says anything about an account that is not
// this machine's own - a refusal that confirms another account exists is an
// account-enumeration oracle on any LAN.
var (
	ErrUnknownMachine = errors.New("this machine is not known to that account")
	ErrForeignAccount = errors.New("that enrollment was not for this machine's account")
	ErrReplayed       = errors.New("that enrollment request has already been used")
	ErrStaleRequest   = errors.New("that enrollment request is too old to use")
	ErrKeyMismatch    = errors.New("that request's node id is not derived from the key it presents")
)

// requestLog bounds how many attempts the issuer keeps. It is an audit trail an owner
// can look at, not a database: the last few are what answer "did that machine even
// ask", and an unbounded one is a memory leak with a nice name.
const requestLog = 32

// Registry answers the only question the issuer cannot answer for itself: does this
// user key belong to an account, and which. Core looks it up in the account record
// login wrote; a local authority looks it up in the allow-list the owner keeps.
type Registry func(userKeyHex string) (account string, ok bool)

// IssuerConfig wires an issuer.
type IssuerConfig struct {
	// Authority signs. It must hold a root private half; a verify-only authority
	// cannot issue, which is exactly why a node may be given one.
	Authority *cert.Authority
	// Accounts says who may enroll. A nil registry refuses everything: an authority
	// that cannot tell who is asking must not hand out identities.
	Accounts Registry
	Now      func() time.Time
}

// Issuer mints Edge certificates.
type Issuer struct {
	cfg IssuerConfig

	mu     sync.Mutex
	used   map[string]bool   // signature -> spent. A signed request is good ONCE.
	issued map[string]string // node id -> certificate serial, so a forget can revoke it
	log    []Request
}

// NewIssuer builds one.
func NewIssuer(cfg IssuerConfig) *Issuer {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Issuer{cfg: cfg, used: map[string]bool{}, issued: map[string]string{}}
}

// Response is what an issuer hands back: the node's certificate and the PUBLIC root it
// verifies its peers with. Nothing else, and never the root's private half.
type Response struct {
	NodeID  string `json:"node_id"`
	Account string `json:"account"`
	Cert    string `json:"cert"`
	Root    string `json:"root"`
}

// Issue mints one certificate, or refuses and mints nothing.
//
// The order is the security core of this side:
//
//  1. THE SIGNATURE FIRST. Nothing else in the request means anything until we know who
//     signed it, so no lookup, no allocation and no state change happens before this.
//  2. FRESHNESS, then REPLAY. A signed request is good once and not forever.
//  3. WHO. The user key names an account, or it does not; a machine that names some
//     OTHER account is refused without being told anything about it.
//  4. THE KEY BINDS THE ID. A node id is the hash of the node's public key, so a
//     request asking for an id its key does not derive is asking to be somebody else.
func (i *Issuer) Issue(r Request) (Response, error) {
	i.record(r)

	if err := r.VerifySignature(); err != nil {
		return Response{}, err
	}
	now := i.cfg.Now()
	if d := now.Sub(time.Unix(r.TS, 0)); d > RequestFreshness || d < -RequestFreshness {
		return Response{}, ErrStaleRequest
	}
	if i.spend(r.Sig) {
		return Response{}, ErrReplayed
	}
	if i.cfg.Accounts == nil {
		return Response{}, ErrUnknownMachine
	}
	account, ok := i.cfg.Accounts(r.UserKey)
	if !ok {
		return Response{}, ErrUnknownMachine
	}
	if r.Account != "" && r.Account != account {
		return Response{}, ErrForeignAccount
	}
	pub, err := r.NodePublicKey()
	if err != nil {
		return Response{}, err
	}
	if NodeID(pub) != r.NodeID {
		return Response{}, ErrKeyMismatch
	}
	if i.cfg.Authority == nil {
		return Response{}, errors.New("this authority holds no root and cannot issue")
	}
	leaf, err := i.cfg.Authority.Issue(r.NodeID, pub)
	if err != nil {
		return Response{}, fmt.Errorf("that certificate could not be issued: %w", err)
	}
	i.mu.Lock()
	i.issued[r.NodeID] = leaf.SerialNumber.String()
	i.mu.Unlock()
	return Response{
		NodeID:  r.NodeID,
		Account: account,
		Cert:    EncodeCert(leaf),
		Root:    EncodeCert(i.cfg.Authority.Root()),
	}, nil
}

// spend marks a signature used and reports whether it ALREADY was.
func (i *Issuer) spend(sig string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.used[sig] {
		return true
	}
	i.used[sig] = true
	return false
}

// record keeps the attempt, for the owner and for the "what actually crossed the wire"
// question. It stores the request as received: public keys, a nonce and a signature.
func (i *Issuer) record(r Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.log = append(i.log, r)
	if len(i.log) > requestLog {
		i.log = i.log[len(i.log)-requestLog:]
	}
}

// Requests is the recent enrollment attempts, oldest first.
func (i *Issuer) Requests() []Request {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Request(nil), i.log...)
}

// LastRequest is the most recent attempt, or a zero Request if there has been none.
func (i *Issuer) LastRequest() Request {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.log) == 0 {
		return Request{}
	}
	return i.log[len(i.log)-1]
}

// SerialOf is the certificate serial this issuer last handed a node, which is what a
// revocation names.
func (i *Issuer) SerialOf(nodeID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, ok := i.issued[nodeID]
	return s, ok
}

// Root is the PUBLIC root this issuer signs under.
func (i *Issuer) Root() *x509.Certificate {
	if i.cfg.Authority == nil {
		return nil
	}
	return i.cfg.Authority.Root()
}
