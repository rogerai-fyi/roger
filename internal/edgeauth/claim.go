package edgeauth

// claim.go is what an ADOPTED candidate presents to receive its certificate (features/edge/
// claim.feature). Unlike an enrolment - which is signed by the account key the owner put on the
// allow-list - a claim is authorised by the owner's ADOPT of the node id, and proven by a signature
// from the NODE key that id derives from. So a phone can be adopted from the machine and become a
// real member without the owner typing an address or a key into the phone.
//
// Nothing here is secret: the node key's public half and a signature bound to one nonce and one
// moment. An attacker who copies a claim cannot use it - it is bound to a node key they do not hold,
// and the grant is consumed on first use.

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

// claimPrefix domain-separates a claim signature from every other thing a node key signs.
const claimPrefix = "rogerai-edge-claim/v1"

// ErrNotAdopted is a claim for a node id the owner has not adopted - there is no grant to satisfy.
var ErrNotAdopted = errors.New("that node has not been adopted on this Edge")

// ClaimRequest is one adopted candidate asking for its certificate.
type ClaimRequest struct {
	NodeID  string `json:"node_id"`
	NodeKey string `json:"node_key"` // hex ed25519 node PUBLIC key - the id derives from it
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Nonce   string `json:"nonce"`
	TS      int64  `json:"ts"`
	Sig     string `json:"sig"` // hex ed25519 signature over canonical(), by the node key
}

// NewClaimRequest builds an unsigned claim for a node key this phone generated.
func NewClaimRequest(name, kind string, nodePub ed25519.PublicKey, now time.Time) ClaimRequest {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	return ClaimRequest{
		NodeID:  NodeID(nodePub),
		NodeKey: hex.EncodeToString(nodePub),
		Name:    name,
		Kind:    kind,
		Nonce:   hex.EncodeToString(nonce[:]),
		TS:      now.Unix(),
	}
}

// canonical is the exact byte string a claim is signed over.
func (c ClaimRequest) canonical() []byte {
	return []byte(strings.Join([]string{
		claimPrefix, c.NodeID, c.NodeKey, c.Name, c.Kind, c.Nonce, strconv.FormatInt(c.TS, 10),
	}, "\n"))
}

// Sign signs the claim with the NODE key. The node key is already named by NodeKey.
func (c *ClaimRequest) Sign(nodePriv ed25519.PrivateKey) {
	c.Sig = hex.EncodeToString(ed25519.Sign(nodePriv, c.canonical()))
}

// NodePublicKey returns the parsed node public key, or an error if it is not a usable ed25519 key.
func (c ClaimRequest) NodePublicKey() (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(c.NodeKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: it names no usable node key", ErrBadSignature)
	}
	return ed25519.PublicKey(b), nil
}

// VerifySignature checks the claim was signed by the node key it names, over exactly these fields,
// and that the node id is the one that key derives - a claim that names one node while proving
// another is not a claim, it is a swap.
func (c ClaimRequest) VerifySignature() error {
	pub, err := c.NodePublicKey()
	if err != nil {
		return err
	}
	if NodeID(pub) != c.NodeID {
		return fmt.Errorf("%w: its node id is not the one its key derives", ErrBadSignature)
	}
	sig, err := hex.DecodeString(c.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: it carries no usable signature", ErrBadSignature)
	}
	if !ed25519.Verify(pub, c.canonical(), sig) {
		return ErrBadSignature
	}
	return nil
}
