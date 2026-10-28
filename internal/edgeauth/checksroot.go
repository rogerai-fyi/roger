package edgeauth

// checksroot.go is how a joiner PROVES it checks the root proof (ruling D9, fixed 2026-10-13; audit
// 2026-10-13). `checks_root` alone was unsigned: a device in the middle could set it, and replaying an
// old app's request with it set made the authority drop the warning that protects that old app from a
// root swap. So a joiner that checks also sends `checks_root_sig`: an Ed25519 signature, by the SAME
// key that signs the request (the user key for an enrolment or a bearer join, the node key for a
// claim), over the request's own canonical bytes followed by "\n" and checksRootDomain. The canonical
// forms are untouched, so every pinned golden and every older authority (which ignores both fields)
// is unaffected. Golden vectors: testdata/checks_root_vectors.json.

import (
	"crypto/ed25519"
	"encoding/hex"
)

// checksRootDomain is appended to a request's canonical bytes to make what checks_root_sig signs.
const checksRootDomain = "rogerai-checks-root/v1"

func checksRootBytes(canonical []byte) []byte {
	return append(append(append([]byte(nil), canonical...), '\n'), checksRootDomain...)
}

// ChecksRootBytes is exactly what checks_root_sig signs for an enrolment request.
func (r Request) ChecksRootBytes() []byte { return checksRootBytes(r.canonical()) }

// ChecksRootBytes is exactly what checks_root_sig signs for a claim.
func (c ClaimRequest) ChecksRootBytes() []byte { return checksRootBytes(c.canonical()) }

// SignChecksRoot says, provably, that this joiner checks the root proof. userPriv is the key Sign used.
func (r *Request) SignChecksRoot(userPriv ed25519.PrivateKey) {
	r.ChecksRoot = true
	r.ChecksRootSig = hex.EncodeToString(ed25519.Sign(userPriv, r.ChecksRootBytes()))
}

// SignChecksRoot is Request.SignChecksRoot for a claim, by the node key.
func (c *ClaimRequest) SignChecksRoot(nodePriv ed25519.PrivateKey) {
	c.ChecksRoot = true
	c.ChecksRootSig = hex.EncodeToString(ed25519.Sign(nodePriv, c.ChecksRootBytes()))
}

func checksRootVerified(flag bool, sigHex, keyHex string, msg []byte) bool {
	if !flag {
		return false
	}
	pub, err := hex.DecodeString(keyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	return err == nil && ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// ChecksRootProven reports whether the request says it checks codes AND proves it said so.
func (r Request) ChecksRootProven() bool {
	return checksRootVerified(r.ChecksRoot, r.ChecksRootSig, r.UserKey, r.ChecksRootBytes())
}

// ChecksRootProven is Request.ChecksRootProven for a claim.
func (c ClaimRequest) ChecksRootProven() bool {
	return checksRootVerified(c.ChecksRoot, c.ChecksRootSig, c.NodeKey, c.ChecksRootBytes())
}
