package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Request-signing headers. A consumer's local proxy signs every broker request
// with the user's Ed25519 key so the broker can verify WHO is spending - the P0
// fix for the previous "trust the X-Roger-User header" model where anyone could
// spend from anyone's wallet by setting a header. See SignRequest / VerifyRequest.
const (
	HeaderPubkey = "X-Roger-Pubkey" // hex ed25519 public key
	HeaderTS     = "X-Roger-TS"     // unix seconds (anti-replay window)
	HeaderSig    = "X-Roger-Sig"    // hex ed25519 signature over CanonicalRequest
	HeaderUser   = "X-Roger-User"   // legacy unauthenticated identity (transition only)
)

// SigMaxSkew is how far a request timestamp may be from the broker's clock before
// it is rejected as stale or skewed (anti-replay). Mirrors the node-registration
// freshness window.
const SigMaxSkew = 5 * time.Minute

// CanonicalRequest is the exact string a consumer signs (and the broker verifies):
//
//	method + "\n" + path + "\n" + ts + "\n" + hex(sha256(body))
//
// Binding the method, path, timestamp, and a body digest stops a captured
// signature from being replayed against a different route or with a swapped body.
func CanonicalRequest(method, path string, ts int64, body []byte) string {
	bodyHash := sha256.Sum256(body)
	return method + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + hex.EncodeToString(bodyHash[:])
}

// UserIDFromPubkey derives a stable, opaque user id from a hex public key:
// "u_" + first 16 hex chars of sha256(pubkey). The same key always maps to the
// same wallet id; the id is not reversible to the key holder's real identity.
func UserIDFromPubkey(pubHex string) string {
	h := sha256.Sum256([]byte(pubHex))
	return "u_" + hex.EncodeToString(h[:])[:16]
}

// SignRequest signs the canonical request string with priv, returning the hex
// pubkey, the timestamp it used, and the hex signature - the three values the
// caller puts in the X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig headers.
func SignRequest(priv ed25519.PrivateKey, method, path string, body []byte) (pubHex string, ts int64, sigHex string) {
	ts = time.Now().Unix()
	pub := priv.Public().(ed25519.PublicKey)
	pubHex = hex.EncodeToString(pub)
	sig := ed25519.Sign(priv, []byte(CanonicalRequest(method, path, ts, body)))
	return pubHex, ts, hex.EncodeToString(sig)
}

// VerifyRequest checks a signed request: the signature must be valid for pubHex
// over the canonical string, and ts must be within SigMaxSkew of now. Returns the
// derived user id on success. ok=false on any decode/verify/staleness failure.
func VerifyRequest(pubHex, sigHex string, ts int64, method, path string, body []byte) (userID string, ok bool) {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", false
	}
	if skew := time.Since(time.Unix(ts, 0)); skew > SigMaxSkew || skew < -SigMaxSkew {
		return "", false
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(CanonicalRequest(method, path, ts, body)), sig) {
		return "", false
	}
	return UserIDFromPubkey(pubHex), true
}
