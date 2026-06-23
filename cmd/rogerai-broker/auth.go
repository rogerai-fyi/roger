package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bownux/rogerai/internal/store"
)

// gitHubAPI is the GitHub REST base (overridable in tests).
var gitHubAPI = "https://api.github.com"

// ghAccessTokenURL is GitHub's OAuth token endpoint (overridable in tests).
var ghAccessTokenURL = "https://github.com/login/oauth/access_token"

// gitHubUser is the subset of GET /user we need to identify an owner.
type gitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// fetchGitHubUser verifies a GitHub access token by calling GET /user server-side
// and returns the authenticated user. A non-200 means the token is bad/expired.
func fetchGitHubUser(token string) (gitHubUser, bool) {
	req, _ := http.NewRequest(http.MethodGet, gitHubAPI+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rogerai-broker")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return gitHubUser{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return gitHubUser{}, false
	}
	var u gitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil || u.ID == 0 {
		return gitHubUser{}, false
	}
	return u, true
}

// authGitHub handles POST /auth/github: the CLI (after a GitHub device-flow login)
// posts its GitHub access token. The request MUST be signed (so the broker knows
// which pubkey to bind). The broker verifies the token against GitHub server-side,
// then binds github_id<->login<->pubkey as an owner. NEVER logs the token.
func (b *broker) authGitHub(w http.ResponseWriter, r *http.Request) {
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
		jsonErr(w, http.StatusUnauthorized, "binding a GitHub account requires a signed request")
		return
	}
	pubkey := r.Header.Get("X-Roger-Pubkey")
	var req struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.AccessToken == "" {
		jsonErr(w, http.StatusBadRequest, "access_token required")
		return
	}
	gu, vok := fetchGitHubUser(req.AccessToken)
	if !vok {
		jsonErr(w, http.StatusUnauthorized, "GitHub token rejected")
		return
	}
	if err := b.db.BindOwner(store.Owner{GitHubID: gu.ID, Login: gu.Login, Pubkey: pubkey}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not bind owner")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "github_login": gu.Login, "github_id": gu.ID})
}

// requireOwner reports whether the signed pubkey on r is bound to a GitHub owner.
// Earning operations gate on this; consume/free paths never call it.
func (b *broker) requireOwner(r *http.Request) (store.Owner, bool) {
	pubkey := r.Header.Get("X-Roger-Pubkey")
	if pubkey == "" {
		return store.Owner{}, false
	}
	o, ok, err := b.db.OwnerByPubkey(pubkey)
	if err != nil || !ok {
		return store.Owner{}, false
	}
	return o, true
}

// --- Web GitHub OAuth (browser flow for the web /console) ---
//
// This is the BROWSER login (separate from the CLI device flow): the only place a
// client SECRET is used. It is a standard authorization-code exchange that sets a
// signed, http-only session cookie. See AUTH-DESIGN section 2 / AUTH-IMPL.md.

const sessionCookie = "roger_session"

func githubClientID() string { return os.Getenv("GITHUB_OAUTH_CLIENT_ID") }
func githubSecret() string   { return os.Getenv("GITHUB_OAUTH_CLIENT_SECRET") }
func webRedirectURI() string {
	return envOr("GITHUB_OAUTH_REDIRECT", "https://rogerai.fyi/auth/github/callback")
}
func dashboardURL() string { return envOr("ROGERAI_DASHBOARD_URL", "https://rogerai.fyi/dashboard") }
func loginURL() string     { return envOr("ROGERAI_LOGIN_URL", "https://rogerai.fyi/login") }

// sessionKey is the HMAC key for signing session cookies. Reuse the broker's
// Ed25519 seed so it is stable across restarts when BROKER_PRIVATE_KEY is set.
func (b *broker) sessionKey() []byte {
	h := sha256.Sum256(append([]byte("roger-session|"), b.priv.Seed()...))
	return h[:]
}

// signSession returns a tamper-evident cookie value "payloadB64.sigB64" where
// payload is "login|githubID|expiresUnix".
func (b *broker) signSession(login string, githubID, exp int64) string {
	payload := login + "|" + strconv.FormatInt(githubID, 10) + "|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySession checks a cookie value's HMAC + expiry and returns the login.
func (b *broker) verifySession(val string) (login string, githubID int64, ok bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, false
	}
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", 0, false
	}
	f := strings.Split(string(payload), "|")
	if len(f) != 3 {
		return "", 0, false
	}
	gid, _ := strconv.ParseInt(f[1], 10, 64)
	exp, _ := strconv.ParseInt(f[2], 10, 64)
	if time.Now().Unix() > exp {
		return "", 0, false
	}
	return f[0], gid, true
}

// authGitHubLogin handles GET /auth/github/login: 302 to GitHub authorize with a
// short-lived signed state cookie (CSRF). Owners hit this from the web /login.
func (b *broker) authGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if githubClientID() == "" || githubSecret() == "" {
		jsonErr(w, http.StatusServiceUnavailable, "web GitHub login not configured")
		return
	}
	state := randState()
	http.SetCookie(w, &http.Cookie{
		Name: "roger_oauth_state", Value: state, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	q := url.Values{
		"client_id":    {githubClientID()},
		"redirect_uri": {webRedirectURI()},
		"scope":        {"read:user"},
		"state":        {state},
	}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

// authGitHubCallback handles GET /auth/github/callback: validates state, exchanges
// the code for a token WITH the client secret, fetches the user, sets a signed
// http-only session cookie, and 302s to the dashboard.
func (b *broker) authGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if githubSecret() == "" {
		jsonErr(w, http.StatusServiceUnavailable, "web GitHub login not configured")
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	sc, err := r.Cookie("roger_oauth_state")
	if code == "" || state == "" || err != nil || sc.Value == "" || !hmac.Equal([]byte(sc.Value), []byte(state)) {
		http.Redirect(w, r, loginURL()+"?error=state", http.StatusFound)
		return
	}
	token, vok := exchangeCode(code)
	if !vok {
		http.Redirect(w, r, loginURL()+"?error=exchange", http.StatusFound)
		return
	}
	gu, uok := fetchGitHubUser(token)
	if !uok {
		http.Redirect(w, r, loginURL()+"?error=user", http.StatusFound)
		return
	}
	exp := time.Now().Add(24 * time.Hour).Unix()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: b.signSession(gu.Login, gu.ID, exp), Path: "/",
		Expires: time.Unix(exp, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{Name: "roger_oauth_state", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, dashboardURL(), http.StatusFound)
}

// exchangeCode swaps an authorization code for a GitHub access token using the
// client secret (server-side only).
func exchangeCode(code string) (string, bool) {
	form := url.Values{
		"client_id":     {githubClientID()},
		"client_secret": {githubSecret()},
		"code":          {code},
		"redirect_uri":  {webRedirectURI()},
	}
	req, _ := http.NewRequest(http.MethodPost, ghAccessTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var r struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || r.AccessToken == "" {
		return "", false
	}
	return r.AccessToken, true
}

func randState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// account handles GET /account: returns the web session owner (from the signed
// session cookie) so the minimal web dashboard can show who is logged in and
// route logged-out visitors to /login. 401 when there is no valid session.
func (b *broker) account(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsCreds(w, r)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	login, gid, ok := b.verifySession(c.Value)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "session expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"github_login": login, "github_id": gid})
}

// authLogout handles POST /auth/logout: clears the web session cookie.
func (b *broker) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsCreds(w, r)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// corsCreds allows the web origin to send the session cookie (credentialed CORS:
// an explicit origin, never "*"). Only the apex site is allowed.
func corsCreds(w http.ResponseWriter, r *http.Request) {
	origin := envOr("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi")
	if o := r.Header.Get("Origin"); o == origin {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Vary", "Origin")
	}
}
