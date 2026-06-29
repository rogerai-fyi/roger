package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestAuthAppleWebLogin: the authorize redirect carries the right params and the nonce sent to
// Apple is SHA256(raw-nonce cookie); unconfigured degrades to 503.
func TestAuthAppleWebLogin(t *testing.T) {
	b := &broker{}
	w := httptest.NewRecorder()
	b.authAppleWebLogin(w, httptest.NewRequest(http.MethodGet, "/auth/apple/web/login", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d, want 503", w.Code)
	}

	t.Setenv("APPLE_SERVICES_ID", "fyi.rogerai.web")
	t.Setenv("APPLE_WEB_REDIRECT", "https://broker.rogerai.fyi/auth/apple/web/callback")
	w = httptest.NewRecorder()
	b.authAppleWebLogin(w, httptest.NewRequest(http.MethodGet, "/auth/apple/web/login", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302", w.Code)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Host != "appleid.apple.com" || u.Path != "/auth/authorize" {
		t.Fatalf("redirect = %s, want appleid.apple.com/auth/authorize", w.Header().Get("Location"))
	}
	q := u.Query()
	if q.Get("client_id") != "fyi.rogerai.web" {
		t.Errorf("client_id = %q, want the Services ID", q.Get("client_id"))
	}
	if q.Get("response_mode") != "form_post" {
		t.Errorf("response_mode = %q, want form_post", q.Get("response_mode"))
	}
	if q.Get("redirect_uri") != "https://broker.rogerai.fyi/auth/apple/web/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	// The raw nonce lives in a cookie; the authorize param is its SHA256 (anti-replay).
	var rawCookie, state string
	for _, c := range w.Result().Cookies() {
		if c.Name == appleNonceCookie {
			rawCookie = c.Value
		}
		if c.Name == appleStateCookie {
			state = c.Value
		}
	}
	if rawCookie == "" || state == "" {
		t.Fatal("state and raw-nonce cookies must be set")
	}
	if q.Get("nonce") != appleNonceHash(rawCookie) {
		t.Errorf("authorize nonce = %q, want SHA256(raw cookie)", q.Get("nonce"))
	}
	if q.Get("state") != state {
		t.Errorf("authorize state %q != cookie state %q", q.Get("state"), state)
	}
}

// TestAuthAppleWebCallback: a valid form_post sets an Apple-wallet session (seeded), and the
// CSRF / token gates redirect to the login page with an error.
func TestAuthAppleWebCallback(t *testing.T) {
	t.Setenv("APPLE_SERVICES_ID", "fyi.rogerai.web")
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := newJWKS(jwkFor("k1", &rsaKey.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	mem := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil) // the broker key that signs the session cookie
	b := &broker{db: mem, priv: priv, pubOfUser: map[string]string{}, seedFunds: 100}
	const sub = "web.sub.1"
	raw := "web-raw-nonce"
	claims := goodClaims(sub, raw)
	claims["aud"] = "fyi.rogerai.web" // web tokens carry the Services ID as aud
	idToken := mintToken(rsaKey, "k1", "RS256", claims)

	post := func(formState, cookieState, idtok, nonceCookie string) *httptest.ResponseRecorder {
		form := url.Values{"state": {formState}, "id_token": {idtok}, "code": {"auth-code"}}
		r := httptest.NewRequest(http.MethodPost, "/auth/apple/web/callback", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookieState != "" {
			r.AddCookie(&http.Cookie{Name: appleStateCookie, Value: cookieState})
		}
		if nonceCookie != "" {
			r.AddCookie(&http.Cookie{Name: appleNonceCookie, Value: nonceCookie})
		}
		w := httptest.NewRecorder()
		b.authAppleWebCallback(w, r)
		return w
	}

	// Happy path → 302 to the dashboard (no error), a session cookie carrying the apple wallet,
	// and a seeded balance.
	w := post("st1", "st1", idToken, raw)
	if w.Code != http.StatusFound || strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("callback = %d loc=%s, want 302 to dashboard", w.Code, w.Header().Get("Location"))
	}
	wallet := walletForAppleSub(sub)
	var sess string
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c.Value
		}
	}
	if sess == "" {
		t.Fatal("no session cookie set")
	}
	if _, _, gotWallet, ok := b.verifySession(sess); !ok || gotWallet != wallet {
		t.Errorf("session wallet = %q ok=%v, want %s", gotWallet, ok, wallet)
	}
	if !walletLoggedIn(wallet) {
		t.Error("apple web wallet must read as logged-in")
	}
	if bal, _ := mem.PeekBalance(wallet); bal != 100 {
		t.Errorf("web apple wallet balance = %.2f, want seeded 100", bal)
	}

	// CSRF: form state != cookie state → error=state, no session.
	if got := post("st1", "different", idToken, raw).Header().Get("Location"); !strings.Contains(got, "error=state") {
		t.Errorf("state mismatch → %s, want error=state", got)
	}
	// Missing id_token → error=token.
	if got := post("st1", "st1", "", raw).Header().Get("Location"); !strings.Contains(got, "error=token") {
		t.Errorf("missing token → %s, want error=token", got)
	}
	// Nonce mismatch (wrong raw cookie) → error=token.
	if got := post("st1", "st1", idToken, "wrong-raw").Header().Get("Location"); !strings.Contains(got, "error=token") {
		t.Errorf("bad nonce → %s, want error=token", got)
	}
}
