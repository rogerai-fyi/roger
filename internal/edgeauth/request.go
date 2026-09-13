package edgeauth

// request.go is what a machine ASKS for, and the proof that it is entitled to ask.
//
// The node generates its own keypair and sends only the PUBLIC half; the request is
// signed with the user key login already left on this machine (internal/client
// identity.go), which is the only thing tying a machine to an account. Nothing secret
// is in here, by design: the whole request may be read by anyone who intercepts it and
// they still cannot use it, because it is bound to one node key, one nonce and one
// moment.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// canonicalPrefix domain-separates this signature from every other thing this account's
// user key signs. A signature over "some bytes" is a signature over any protocol that
// can be made to produce those bytes.
const canonicalPrefix = "rogerai-edge-enroll/v1"

// RequestFreshness bounds how far from now a request's timestamp may be. A signed
// request that never goes stale is a signed request somebody can keep.
const RequestFreshness = 5 * time.Minute

// Request is one machine asking to join one Edge.
type Request struct {
	Account string `json:"account"`
	NodeID  string `json:"node_id"`
	NodeKey string `json:"node_key"` // hex ed25519 PUBLIC key - never the private half
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Nonce   string `json:"nonce"`
	TS      int64  `json:"ts"`
	UserKey string `json:"user_key"` // hex ed25519 public half of this machine's user key
	Sig     string `json:"sig"`
}

// ErrBadSignature is a request whose signature does not check out - the wrong key, or a
// request altered after it was signed.
var ErrBadSignature = errors.New("that enrollment request's signature does not check out")

// NewRequest builds an unsigned request for a node key this machine just generated.
func NewRequest(account, name, kind string, nodePub ed25519.PublicKey, now time.Time) Request {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	return Request{
		Account: account,
		NodeID:  NodeID(nodePub),
		NodeKey: hex.EncodeToString(nodePub),
		Name:    name,
		Kind:    kind,
		Nonce:   hex.EncodeToString(nonce[:]),
		TS:      now.Unix(),
	}
}

// canonical is the exact byte string signed. Every field that decides what is issued is
// in it: change any of them after signing and the signature stops verifying, which is
// the "tampered request" case.
func (r Request) canonical() []byte {
	return []byte(strings.Join([]string{
		canonicalPrefix, r.Account, r.NodeID, r.NodeKey, r.Name, r.Kind, r.Nonce,
		strconv.FormatInt(r.TS, 10),
	}, "\n"))
}

// Sign signs the request as this machine's user key, and records the public half so the
// issuer knows which key to check it against.
func (r *Request) Sign(priv ed25519.PrivateKey) {
	r.UserKey = hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	r.Sig = hex.EncodeToString(ed25519.Sign(priv, r.canonical()))
}

// VerifySignature checks the request was signed by the key it names, over exactly these
// fields.
func (r Request) VerifySignature() error {
	pub, err := hex.DecodeString(r.UserKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: it names no usable signing key", ErrBadSignature)
	}
	sig, err := hex.DecodeString(r.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: it carries no usable signature", ErrBadSignature)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), r.canonical(), sig) {
		return ErrBadSignature
	}
	return nil
}

// NodePublicKey is the key the certificate will be bound to.
func (r Request) NodePublicKey() (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(r.NodeKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("that enrollment request carries no usable node key")
	}
	return ed25519.PublicKey(raw), nil
}
