// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 RogerAI
//
// This file is part of the RogerAI node-agent protocol + usage-receipt SDK, released
// under the Apache License 2.0 so anyone can implement a compatible node or verify a
// receipt independently. The rest of the RogerAI platform is licensed separately (see
// LICENSING.md). Do not add platform logic to this file.

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
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
	HeaderNonce  = "X-Roger-Nonce"  // optional per-request nonce, bound into the signature (see SignRequestNonce)
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

// canonicalWithNonce binds a per-request NONCE into the signed string, on top of everything
// CanonicalRequest already binds. Its purpose is anti-replay in a setting where the 5-minute
// timestamp window is too loose to rely on (a free local plane an eavesdropper can see): a
// signature covers only method, path, ts-to-the-second, and a body hash, so two otherwise
// identical requests in the same second - a discovery poll, a station re-poll with an empty
// body - share one signature, and a captured request can be replayed verbatim within the
// window. A random nonce makes every request's signature unique, and a verifier that refuses a
// nonce it has already seen turns a replay into a refusal. It is APPENDED, so a caller that
// does not use a nonce produces exactly the CanonicalRequest string - the plain path is
// unchanged and unaffected.
func canonicalWithNonce(method, path string, ts int64, body []byte, nonce string) string {
	return CanonicalRequest(method, path, ts, body) + "\n" + nonce
}

// NewNonce mints a random 128-bit per-request nonce, hex-encoded (32 lowercase-hex chars).
func NewNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The system CSPRNG failing is not a condition to paper over with a predictable nonce,
		// which would defeat the replay defense; fail loudly.
		panic("protocol: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// isNonce reports whether s is exactly a NewNonce value: 32 lowercase-hex characters. A verifier
// checks this so it never stores an oversized or malformed nonce a caller supplied.
func isNonce(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// SignRequestNonce signs the canonical request WITH a nonce bound in, returning the hex
// pubkey, the timestamp used, and the hex signature. The caller sends the nonce in the
// X-Roger-Nonce header alongside the usual three, and the verifier must use VerifyRequestNonce
// with the same nonce. A verifier that has never seen this nonce (within the freshness window)
// accepts it once; a replay carries the same nonce and is refused.
func SignRequestNonce(priv ed25519.PrivateKey, method, path string, body []byte, nonce string) (pubHex string, ts int64, sigHex string) {
	ts = time.Now().Unix()
	pub := priv.Public().(ed25519.PublicKey)
	pubHex = hex.EncodeToString(pub)
	sig := ed25519.Sign(priv, []byte(canonicalWithNonce(method, path, ts, body, nonce)))
	return pubHex, ts, hex.EncodeToString(sig)
}

// VerifyRequestNonce checks a nonce-bound signed request. Identical to VerifyRequest except the
// nonce is bound into the verified string, so a signature made for one nonce cannot be presented
// with another. It does NOT itself remember nonces - the caller keeps the seen-nonce set and
// decides replay - so this stays a pure function.
func VerifyRequestNonce(pubHex, sigHex string, ts int64, method, path string, body []byte, nonce string) (userID string, ok bool) {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", false
	}
	// The nonce must be exactly what NewNonce mints: 32 lowercase-hex chars (16 bytes). Fixing
	// the shape here bounds what a verifier can be made to store per nonce (defeating a
	// memory-exhaustion attack via huge nonce headers) and rejects garbage before any crypto.
	if !isNonce(nonce) {
		return "", false
	}
	if skew := time.Since(time.Unix(ts, 0)); skew > SigMaxSkew || skew < -SigMaxSkew {
		return "", false
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(canonicalWithNonce(method, path, ts, body, nonce)), sig) {
		return "", false
	}
	return UserIDFromPubkey(pubHex), true
}
