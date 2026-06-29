package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// Sign in with Apple (App Store Guideline 4.8). The SiwA analogue of authGitHub: the iOS
// app (or web console) authenticates with Apple, then posts Apple's identity token on a
// SIGNED request so the broker binds the Apple `sub` to the signing pubkey - exactly like
// /auth/github binds github_id. The bind is pure token verification (no client secret).
// See docs/SIGN-IN-WITH-APPLE.md in the roger-ios repo for the full contract.

// appleJWKSURL is Apple's public-key (JWKS) endpoint; overridable in tests, like gitHubAPI.
var appleJWKSURL = "https://appleid.apple.com/auth/keys"

// appleIssuer is the pinned `iss` claim every Apple identity token must carry.
const appleIssuer = "https://appleid.apple.com"

// appleSkew is the leeway on exp/iat, matching the request-signing SigMaxSkew (±5 min).
const appleSkew = 5 * time.Minute

// appleJWKSTTL is how long fetched Apple keys are cached before a refresh (Apple rotates).
const appleJWKSTTL = time.Hour

// appleAudiences is the set of acceptable `aud` values. NATIVE tokens carry the app bundle
// id; WEB (Services ID) tokens carry the services id. Both are accepted so one /auth/apple
// verifies the iOS app AND the web console. Pinning aud is what stops a token Apple minted
// for a DIFFERENT relying party being replayed at us (docs §3 step 7, §6).
func appleAudiences() map[string]bool {
	auds := map[string]bool{}
	if bundle := envOr("APPLE_BUNDLE_ID", "fyi.rogerai.app"); bundle != "" {
		auds[bundle] = true
	}
	if svc := os.Getenv("APPLE_SERVICES_ID"); svc != "" { // the web Services ID (optional)
		auds[svc] = true
	}
	return auds
}

// appleClaims is the subset of the identity-token payload the bind needs. `sub` is the
// stable, app-scoped Apple user id (the binding key); email is best-effort (welcome email
// only, never a gate), matching the GitHub email posture.
type appleClaims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Aud   string `json:"aud"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Nonce string `json:"nonce"`
	Email string `json:"email"`
}

// appleJWKS caches Apple's signing keys (kid -> RSA public key) with a TTL. A kid miss on a
// fresh cache still triggers one refetch (handles key rotation between TTLs) before failing.
type appleJWKS struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var appleKeys = &appleJWKS{}

// key returns the RSA public key for kid, fetching/refetching the JWKS as needed.
func (c *appleJWKS) key(kid string) (*rsa.PublicKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetched) < appleJWKSTTL && c.keys != nil {
		if k, ok := c.keys[kid]; ok {
			return k, true
		}
		// Fresh cache but unknown kid: fall through to a single refetch (rotation).
	}
	if !c.refetchLocked() {
		// Refetch failed: last-resort, serve a still-cached key if we have one.
		k, ok := c.keys[kid]
		return k, ok
	}
	k, ok := c.keys[kid]
	return k, ok
}

// refetchLocked pulls the JWKS and replaces the cache. Caller holds c.mu.
func (c *appleJWKS) refetchLocked() bool {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(appleJWKSURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var doc struct {
		Keys []struct {
			Kty, Kid, Use, Alg, N, E string
		} `json:"keys"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc) != nil {
		return false
	}
	next := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if pub, perr := rsaPublicKeyFromJWK(k.N, k.E); perr == nil {
			next[k.Kid] = pub
		}
	}
	if len(next) == 0 {
		return false
	}
	c.keys = next
	c.fetched = time.Now()
	return true
}

// rsaPublicKeyFromJWK builds an *rsa.PublicKey from a JWK's base64url modulus (n) and
// exponent (e).
func rsaPublicKeyFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, errors.New("empty modulus/exponent")
	}
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 2 {
		return nil, errors.New("bad exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e.Int64())}, nil
}

// verifyAppleIdentityToken validates an Apple SiwA identity token (RS256 JWT) per
// docs/SIGN-IN-WITH-APPLE.md §3 and returns its claims. Every step is a hard gate; any
// failure returns ok=false and the caller maps that to ONE opaque 401 (never leak which
// check failed). NEVER log the token, sub, or rawNonce.
func verifyAppleIdentityToken(token, rawNonce string) (appleClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return appleClaims{}, false
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return appleClaims{}, false
	}
	var hdr struct{ Alg, Kid string }
	if json.Unmarshal(headerJSON, &hdr) != nil {
		return appleClaims{}, false
	}
	if hdr.Alg != "RS256" { // alg-confusion / key-substitution defense: reject none/HS*/etc.
		return appleClaims{}, false
	}
	pub, ok := appleKeys.key(hdr.Kid)
	if !ok {
		return appleClaims{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return appleClaims{}, false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return appleClaims{}, false
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return appleClaims{}, false
	}
	var c appleClaims
	if json.Unmarshal(payloadJSON, &c) != nil {
		return appleClaims{}, false
	}
	if c.Iss != appleIssuer {
		return appleClaims{}, false
	}
	if !appleAudiences()[c.Aud] {
		return appleClaims{}, false
	}
	now := time.Now()
	if c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(appleSkew)) { // expired
		return appleClaims{}, false
	}
	if c.Iat != 0 && time.Unix(c.Iat, 0).After(now.Add(appleSkew)) { // iat in the future
		return appleClaims{}, false
	}
	// Nonce anti-replay: the token carries only SHA256(rawNonce); require the caller to
	// present the pre-image so a passively captured token can't be replayed (docs §6).
	if c.Nonce == "" || rawNonce == "" {
		return appleClaims{}, false
	}
	if subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(appleNonceHash(rawNonce))) != 1 {
		return appleClaims{}, false
	}
	if c.Sub == "" {
		return appleClaims{}, false
	}
	return c, true
}

// appleNonceHash is the lowercase-hex SHA256 of the raw nonce - the value the client puts in
// the SiwA request (native: ASAuthorizationAppleIDRequest.nonce; web: the authorize `nonce`
// param) and the broker matches against the token's `nonce` claim (docs §6).
func appleNonceHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// walletForAppleSub is the Apple account-wallet namespace, mirroring u_gh_<githubID>. The
// raw `sub` is stored in owners.apple_sub; the wallet id is its hash so the id is tidy and
// bounded and the sub isn't exposed as a wallet identifier. Two devices that bind the SAME
// sub resolve to the SAME wallet (one wallet per Apple account) - the SeedOnce key dedupes.
func walletForAppleSub(sub string) string {
	h := sha256.Sum256([]byte("apple|" + sub))
	return "u_apple_" + hex.EncodeToString(h[:])[:16]
}

// authApple handles POST /auth/apple: bind an Apple owner to the signing pubkey. Line-for-
// line the authGitHub handler with verifyAppleIdentityToken in place of fetchGitHubUser and
// apple_sub in place of github_id. The request MUST be signed (so we know which pubkey to
// bind). NEVER logs the token, sub, or nonce.
func (b *broker) authApple(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_, authed, ok := b.identityOf(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	if !authed {
		jsonErr(w, http.StatusUnauthorized, "binding an Apple account requires a signed request")
		return
	}
	pubkey := r.Header.Get("X-Roger-Pubkey")
	var req struct {
		IdentityToken     string `json:"identity_token"`
		RawNonce          string `json:"raw_nonce"`
		AuthorizationCode string `json:"authorization_code"` // captured for later refresh/revoke; unused here
		Name              string `json:"name"`               // first-auth-only; welcome email personalization
	}
	if err := json.Unmarshal(body, &req); err != nil || req.IdentityToken == "" {
		jsonErr(w, http.StatusBadRequest, "identity_token required")
		return
	}
	claims, vok := verifyAppleIdentityToken(req.IdentityToken, req.RawNonce)
	if !vok {
		jsonErr(w, http.StatusUnauthorized, "Apple token rejected")
		return
	}
	// sub + email come from the VERIFIED token only; name is the lone non-authoritative
	// client value (Apple never puts it in the token). BindOwner stores name/email
	// fill-if-empty and preserves a GitHub link on the same pubkey (dual-link), so an
	// Apple bind never clobbers a user-set email or an existing GitHub owner.
	if err := b.db.BindOwner(store.Owner{AppleSub: claims.Sub, Pubkey: pubkey, Name: req.Name, Email: claims.Email}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not bind owner")
		return
	}
	b.invalidateOwnerWallet(pubkey)
	// Seed the Apple ACCOUNT wallet once (idempotent per sub across every device that binds it).
	wallet := walletForAppleSub(claims.Sub)
	if _, seeded, _ := b.db.SeedOnce(wallet, b.seedFunds); seeded {
		b.invalidateSeedRemaining()
	}
	if o, ok, _ := b.db.OwnerByPubkey(pubkey); ok {
		b.maybeSendWelcome(o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apple_sub": claims.Sub})
}
