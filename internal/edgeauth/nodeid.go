package edgeauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
)

// NodeID derives a node's stable identity from the public half of the keypair the node
// generated for itself. The private half never leaves the node, so an id is a claim
// only the key holder can make good on: two nodes cannot share an id without sharing a
// key. sha256 of the raw key, hex, prefixed so an id is recognisable in a log line.
//
// Truncated to 24 bytes (192 bits) for ONE reason: an id is also the mDNS service
// INSTANCE label, and a DNS label is at most 63 bytes. "n_" plus 48 hex characters is
// 50, which fits with room to spare, and 192 bits is far past any collision anyone can
// mount - the property the spec asks for is that two nodes cannot share an id without
// sharing a key, and a 192-bit digest gives that.
//
// It lives HERE, in the authority, rather than in internal/edge, because it is a trust
// primitive: the issuer refuses a request whose id its key does not derive, and the
// authority must be able to make that check without linking the LAN face (and the HTTP
// client inside it). internal/edge.NodeID forwards to this, so there is one derivation.
func NodeID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "n_" + hex.EncodeToString(sum[:24])
}
