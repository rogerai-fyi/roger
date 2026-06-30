package main

import (
	"crypto/hmac"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Web Sign in with Apple (the browser flow on rogerai.fyi), the SiwA analogue of the web
// GitHub OAuth in auth.go. Unlike the native bind (/auth/apple, which signs with the device
// Ed25519 key), the browser has no signing key, so the login is a standard Apple authorize
// redirect + form_post callback that verifies the returned identity token and sets the same
// signed session cookie the GitHub web login uses - carrying an Apple wallet (githubID=0).
//
// Login needs only the id_token (Apple-signed, JWKS-verifiable) - NO client secret. The
// authorization `code` (also returned) is for a LATER follow-up: exchanging it at Apple's
// token endpoint (with the .p8 client secret) for refresh tokens + the revocation call App
// Store account-deletion requires. That exchange is intentionally not done here.

const appleStateCookie = "roger_apple_state"
const appleNonceCookie = "roger_apple_nonce"

// appleServicesID is the web Services ID (the web client_id / token `aud`). Empty = web Apple
// login not configured (the endpoints 503, exactly like the GitHub web login without a client).
func appleServicesID() string { return os.Getenv("APPLE_SERVICES_ID") }

// appleWebRedirectURI is the Services ID Return URL registered in the Apple portal. Apple
// form_posts the result here; it must match the portal value exactly.
func appleWebRedirectURI() string {
	return envOr("APPLE_WEB_REDIRECT", "https://broker.rogerai.fyi/auth/apple/web/callback")
}

// authAppleWebLogin handles GET /auth/apple/web/login: 302 to Apple's authorize with short-
// lived signed state (CSRF) + raw-nonce cookies. The nonce sent to Apple is SHA256(raw) so the
// returned token carries only the hash (anti-replay, docs §6); the raw is kept in a cookie to
// match the token at the callback. Both cookies are SameSite=None so they survive Apple's
// cross-site form_post back to us.
func (b *broker) authAppleWebLogin(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if appleServicesID() == "" {
		jsonErr(w, http.StatusServiceUnavailable, "web Apple login not configured")
		return
	}
	state := randState()
	rawNonce := randState() + randState() // 32 random bytes (hex), kept server-side via cookie
	setCrossSiteCookie(w, appleStateCookie, state)
	setCrossSiteCookie(w, appleNonceCookie, rawNonce)
	q := url.Values{
		"client_id":     {appleServicesID()},
		"redirect_uri":  {appleWebRedirectURI()},
		"response_type": {"code id_token"}, // id_token authenticates; code is for later refresh/revoke
		"response_mode": {"form_post"},     // required by Apple whenever scope includes name/email
		"scope":         {"name email"},
		"state":         {state},
		"nonce":         {appleNonceHash(rawNonce)},
	}
	http.Redirect(w, r, "https://appleid.apple.com/auth/authorize?"+q.Encode(), http.StatusFound)
}

// authAppleWebCallback handles POST /auth/apple/web/callback: Apple form_posts {code, id_token,
// state, user}. Validate state (CSRF), verify the id_token (RS256/JWKS, aud=Services ID, nonce
// vs the cookie), then set the session cookie for the Apple wallet and 302 to the dashboard.
// Any failure 302s back to the login page with an error - no detail leaked.
func (b *broker) authAppleWebCallback(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if appleServicesID() == "" {
		jsonErr(w, http.StatusServiceUnavailable, "web Apple login not configured")
		return
	}
	_ = r.ParseForm()
	state := r.FormValue("state")
	sc, serr := r.Cookie(appleStateCookie)
	if state == "" || serr != nil || sc.Value == "" || !hmac.Equal([]byte(sc.Value), []byte(state)) {
		http.Redirect(w, r, loginURL()+"?error=state", http.StatusFound)
		return
	}
	nc, nerr := r.Cookie(appleNonceCookie)
	idToken := r.FormValue("id_token")
	if idToken == "" || nerr != nil || nc.Value == "" {
		http.Redirect(w, r, loginURL()+"?error=token", http.StatusFound)
		return
	}
	claims, vok := verifyAppleIdentityToken(idToken, nc.Value)
	if !vok {
		http.Redirect(w, r, loginURL()+"?error=token", http.StatusFound)
		return
	}
	// The browser has no device pubkey, so there's no owner row to bind - the wallet is keyed
	// purely off the verified sub. Seed it once (idempotent per account, SHARED with the native
	// u_apple_ wallet) so a web-only Apple user still gets their starter balance.
	wallet := walletForAppleSub(claims.Sub)
	if _, seeded, _ := b.db.SeedOnce(wallet, b.seedFunds); seeded {
		b.invalidateSeedRemaining()
	}
	login := claims.Email // Apple has no username; the email (or relay) is the display handle
	if login == "" {
		login = "apple"
	}
	exp := time.Now().Add(24 * time.Hour).Unix()
	b.setWebSessionWallet(w, login, 0, wallet, exp)
	clearCookie(w, appleStateCookie)
	clearCookie(w, appleNonceCookie)
	http.Redirect(w, r, dashboardURL(), http.StatusFound)
}

// setCrossSiteCookie sets a short-lived (10 min) httpOnly cookie that survives a cross-site
// POST back from Apple (SameSite=None; Secure). Used for the one-shot state + nonce.
func setCrossSiteCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
}

// clearCookie expires a cookie.
func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode})
}
