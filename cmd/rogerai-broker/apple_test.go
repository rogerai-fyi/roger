package main

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// --- Apple SiwA test helpers -------------------------------------------------

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func nonceHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// jwkFor renders an RSA public key as an Apple-style JWK.
func jwkFor(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64url(pub.N.Bytes()), "e": b64url(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// mutableJWKS is an httptest JWKS server whose key set can be swapped mid-test (to
// exercise Apple's key rotation: a token kid absent from the cache forces a refetch).
type mutableJWKS struct {
	*httptest.Server
	mu   sync.Mutex
	keys []map[string]any
}

func newJWKS(keys ...map[string]any) *mutableJWKS {
	m := &mutableJWKS{keys: keys}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": m.keys})
	}))
	return m
}

func (m *mutableJWKS) set(keys ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = keys
}

// mintToken builds and signs a JWT with the given alg. alg "RS256" signs with priv;
// "none" leaves the signature empty; "HS256" signs with a symmetric key (alg-confusion).
func mintToken(priv *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	hb, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
	cb, _ := json.Marshal(claims)
	input := b64url(hb) + "." + b64url(cb)
	var sig []byte
	switch alg {
	case "RS256":
		sum := sha256.Sum256([]byte(input))
		sig, _ = rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	case "HS256":
		mac := hmac.New(sha256.New, []byte("attacker-key"))
		mac.Write([]byte(input))
		sig = mac.Sum(nil)
	case "none":
		sig = nil
	}
	return input + "." + b64url(sig)
}

// goodClaims is a valid claim set for the given sub/nonce (10-min exp, correct iss/aud).
func goodClaims(sub, rawNonce string) map[string]any {
	return map[string]any{
		"iss":   appleIssuer,
		"sub":   sub,
		"aud":   "fyi.rogerai.app",
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
		"nonce": nonceHash(rawNonce),
		"email": "user@privaterelay.appleid.com",
	}
}

func appleBody(token, rawNonce, name string) []byte {
	b, _ := json.Marshal(map[string]any{"identity_token": token, "raw_nonce": rawNonce, "name": name})
	return b
}

// useJWKS resets the package key cache and points appleJWKSURL at srv for the test.
func useJWKS(t *testing.T, srv *mutableJWKS) {
	t.Helper()
	appleKeys = &appleJWKS{}
	old := appleJWKSURL
	appleJWKSURL = srv.URL
	t.Cleanup(func() { appleJWKSURL = old; appleKeys = &appleJWKS{} })
}

// --- tests -------------------------------------------------------------------

// TestAuthAppleBindsOwner: the happy path + the multi-device wallet-share guarantee +
// seed-once + the logged-in gate, mirroring TestKeypairBindsToOneWallet for GitHub.
func TestAuthAppleBindsOwner(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := newJWKS(jwkFor("k1", &rsaKey.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 100}

	const sub = "000777.applesub.roger"
	raw := "device-nonce-1"
	token := mintToken(rsaKey, "k1", "RS256", goodClaims(sub, raw))

	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	post := func(body []byte, p ed25519.PrivateKey, sign bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/auth/apple", bytes.NewReader(body))
		if sign {
			signReq(r, p, body)
		}
		w := httptest.NewRecorder()
		b.authApple(w, r)
		return w
	}
	// The logged-in gate, as the prod spend/dashboard paths apply it (dashIdentityBody +
	// walletLoggedIn) - the standalone loggedInWallet wrapper was dropped upstream.
	loggedInWallet := func(r *http.Request) (string, bool) {
		if id, ok := b.dashIdentityBody(r, nil); ok && walletLoggedIn(id) {
			return id, true
		}
		return "", false
	}

	w := post(appleBody(token, raw, "Ada Lovelace"), priv, true)
	if w.Code != http.StatusOK {
		t.Fatalf("bind = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out struct {
		OK       bool   `json:"ok"`
		AppleSub string `json:"apple_sub"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.OK || out.AppleSub != sub {
		t.Fatalf("response = %+v, want ok + apple_sub=%s", out, sub)
	}
	o, ok, _ := mem.OwnerByPubkey(pubHex)
	if !ok || o.AppleSub != sub {
		t.Fatalf("owner = %+v ok=%v, want apple_sub=%s", o, ok, sub)
	}
	// Name captured for the welcome email; email came from the verified token.
	if o.Name != "Ada Lovelace" || o.Email == "" {
		t.Errorf("owner name/email = %q/%q, want captured", o.Name, o.Email)
	}
	wallet := walletForAppleSub(sub)
	if bal, _ := mem.PeekBalance(wallet); bal != 100 {
		t.Errorf("apple wallet balance = %.2f, want seeded 100", bal)
	}
	// The signed keypair now resolves to the apple wallet and is logged in.
	gr := httptest.NewRequest(http.MethodGet, "/x", nil)
	signReq(gr, priv, nil)
	if wal, lok := loggedInWallet(gr); !lok || wal != wallet {
		t.Fatalf("post-bind loggedInWallet = %q ok=%v, want %s", wal, lok, wallet)
	}

	// Multi-device: a DIFFERENT pubkey binding the SAME sub resolves to the SAME wallet,
	// and the seed is NOT granted twice (idempotent per account).
	_, priv2, _ := ed25519.GenerateKey(nil)
	token2 := mintToken(rsaKey, "k1", "RS256", goodClaims(sub, "device-nonce-2"))
	if w := post(appleBody(token2, "device-nonce-2", ""), priv2, true); w.Code != http.StatusOK {
		t.Fatalf("second device bind = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	gr2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	signReq(gr2, priv2, nil)
	if wal, _ := loggedInWallet(gr2); wal != wallet {
		t.Errorf("second device wallet = %q, want shared %s", wal, wallet)
	}
	if bal, _ := mem.PeekBalance(wallet); bal != 100 {
		t.Errorf("balance after 2nd device = %.2f, want 100 (seed once per account)", bal)
	}
}

// TestAuthAppleWebAudience: a token whose aud is the configured web Services ID verifies
// too (native + web share one /auth/apple).
func TestAuthAppleWebAudience(t *testing.T) {
	t.Setenv("APPLE_SERVICES_ID", "fyi.rogerai.web")
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := newJWKS(jwkFor("k1", &rsaKey.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 50}
	_, priv, _ := ed25519.GenerateKey(nil)

	claims := goodClaims("websub.1", "wn")
	claims["aud"] = "fyi.rogerai.web" // the Services ID, not the bundle id
	token := mintToken(rsaKey, "k1", "RS256", claims)

	r := httptest.NewRequest(http.MethodPost, "/auth/apple", bytes.NewReader(appleBody(token, "wn", "")))
	signReq(r, priv, appleBody(token, "wn", ""))
	w := httptest.NewRecorder()
	b.authApple(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("web-aud bind = %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

// TestAuthAppleRejections covers every adversarial gate: each must fail closed with the
// documented status, never binding an owner.
func TestAuthAppleRejections(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048) // a non-Apple signing key
	jwks := newJWKS(jwkFor("k1", &rsaKey.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 100}
	_, priv, _ := ed25519.GenerateKey(nil)
	raw := "nonce-x"

	// helper: POST a body, optionally signing / tampering the signature.
	do := func(body []byte, sign, tamper bool) int {
		r := httptest.NewRequest(http.MethodPost, "/auth/apple", bytes.NewReader(body))
		if sign {
			signReq(r, priv, body)
		}
		if tamper {
			r.Header.Set(protocol.HeaderSig, "deadbeef")
		}
		w := httptest.NewRecorder()
		b.authApple(w, r)
		return w.Code
	}
	tok := func(kid, alg string, mut func(map[string]any)) string {
		c := goodClaims("sub-adv", raw)
		if mut != nil {
			mut(c)
		}
		key := rsaKey
		if alg == "RS256-wrongkey" {
			key, alg = otherKey, "RS256"
		}
		return mintToken(key, kid, alg, c)
	}

	cases := []struct {
		name string
		body []byte
		sign bool
		tamp bool
		want int
	}{
		{"unsigned", appleBody(tok("k1", "RS256", nil), raw, ""), false, false, http.StatusUnauthorized},
		{"bad-signature", appleBody(tok("k1", "RS256", nil), raw, ""), true, true, http.StatusUnauthorized},
		{"missing-token", appleBody("", raw, ""), true, false, http.StatusBadRequest},
		{"wrong-aud", appleBody(tok("k1", "RS256", func(c map[string]any) { c["aud"] = "com.evil.app" }), raw, ""), true, false, http.StatusUnauthorized},
		{"wrong-iss", appleBody(tok("k1", "RS256", func(c map[string]any) { c["iss"] = "https://evil.example" }), raw, ""), true, false, http.StatusUnauthorized},
		{"expired", appleBody(tok("k1", "RS256", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), raw, ""), true, false, http.StatusUnauthorized},
		{"iat-future", appleBody(tok("k1", "RS256", func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() }), raw, ""), true, false, http.StatusUnauthorized},
		{"alg-none", appleBody(tok("k1", "none", nil), raw, ""), true, false, http.StatusUnauthorized},
		{"alg-hs256", appleBody(tok("k1", "HS256", nil), raw, ""), true, false, http.StatusUnauthorized},
		{"unknown-kid", appleBody(tok("nope", "RS256", nil), raw, ""), true, false, http.StatusUnauthorized},
		{"non-apple-key", appleBody(tok("k1", "RS256-wrongkey", nil), raw, ""), true, false, http.StatusUnauthorized},
		{"nonce-mismatch", appleBody(tok("k1", "RS256", func(c map[string]any) { c["nonce"] = nonceHash("different") }), raw, ""), true, false, http.StatusUnauthorized},
		{"missing-nonce", appleBody(tok("k1", "RS256", func(c map[string]any) { delete(c, "nonce") }), raw, ""), true, false, http.StatusUnauthorized},
		{"malformed-jwt", appleBody("not.a.jwt", raw, ""), true, false, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if got := do(tc.body, tc.sign, tc.tamp); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
	// No owner was ever bound by a rejected request.
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if _, ok, _ := mem.OwnerByPubkey(pubHex); ok {
		t.Error("a rejected request bound an owner — must bind nothing")
	}
}

// TestAppleJWKSRotation: a token signed by a freshly-rotated key (new kid absent from the
// cache) triggers exactly one refetch, then verifies.
func TestAppleJWKSRotation(t *testing.T) {
	keyA, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyB, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := newJWKS(jwkFor("kA", &keyA.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	// Prime the cache with kA.
	if _, ok := verifyAppleIdentityToken(mintToken(keyA, "kA", "RS256", goodClaims("s", "n")), "n"); !ok {
		t.Fatal("initial kA token should verify")
	}
	// Apple rotates: serve only kB now. A kB token (kid not in cache) forces a refetch.
	jwks.set(jwkFor("kB", &keyB.PublicKey))
	if _, ok := verifyAppleIdentityToken(mintToken(keyB, "kB", "RS256", goodClaims("s", "n")), "n"); !ok {
		t.Error("post-rotation kB token should verify after refetch")
	}
}

// TestAppleWalletNamespace: the account-wallet precedence (GitHub wins, then Apple), the
// reservedID guard, the unsigned-impersonation rejection, and dual-link preservation.
func TestAppleWalletNamespace(t *testing.T) {
	// Precedence: a pubkey bound to BOTH providers resolves to the GitHub wallet.
	if w, ok := accountWalletForOwner(store.Owner{GitHubID: 7, AppleSub: "s"}); !ok || w != "u_gh_7" {
		t.Errorf("dual-link wallet = %q ok=%v, want u_gh_7 (GitHub wins)", w, ok)
	}
	// Apple-only resolves to the apple namespace.
	if w, ok := accountWalletForOwner(store.Owner{AppleSub: "s"}); !ok || w != walletForAppleSub("s") {
		t.Errorf("apple-only wallet = %q ok=%v", w, ok)
	}
	// Anonymized / unbound → no account wallet.
	if _, ok := accountWalletForOwner(store.Owner{AppleSub: "s", Anonymized: true}); ok {
		t.Error("anonymized owner must have no account wallet")
	}
	if _, ok := accountWalletForOwner(store.Owner{}); ok {
		t.Error("unbound owner must have no account wallet")
	}
	// reservedID guards the apple namespace, and isAccountWallet recognizes it.
	if !reservedID("u_apple_deadbeefdeadbeef") || !isAccountWallet("u_apple_deadbeefdeadbeef") {
		t.Error("u_apple_ must be reserved + recognized as an account wallet")
	}
	// An UNSIGNED legacy header claiming an apple wallet is rejected (no balance leak).
	b := &broker{pubOfUser: map[string]string{}}
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set(protocol.HeaderUser, "u_apple_deadbeefdeadbeef")
	if _, _, ok := b.identityOf(r, nil); ok {
		t.Error("unsigned u_apple_ impersonation must be rejected")
	}

	// Dual-link preservation in the store: binding Apple on a GitHub-bound pubkey keeps both.
	mem := store.NewMem()
	const pub = "pubkeyhex"
	_ = mem.BindOwner(store.Owner{GitHubID: 9, Login: "octocat", Pubkey: pub})
	_ = mem.BindOwner(store.Owner{AppleSub: "apple-sub-9", Pubkey: pub})
	o, _, _ := mem.OwnerByPubkey(pub)
	if o.GitHubID != 9 || o.Login != "octocat" || o.AppleSub != "apple-sub-9" {
		t.Errorf("dual-link owner = %+v, want both github(9/octocat) + apple-sub-9 preserved", o)
	}
}
