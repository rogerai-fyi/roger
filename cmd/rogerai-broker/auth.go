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

	"github.com/rogerai-fyi/roger/internal/store"
)

// gitHubAPI is the GitHub REST base (overridable in tests).
var gitHubAPI = "https://api.github.com"

// ghAccessTokenURL is GitHub's OAuth token endpoint (overridable in tests).
var ghAccessTokenURL = "https://github.com/login/oauth/access_token"

// gitHubUser is the subset of GET /user we need to identify an owner. Name + Email are
// captured for the welcome email: both are best-effort (GitHub omits email unless the
// user has a PUBLIC email, and name may be empty), so neither is ever a gate.
type gitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
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
	// Capture the GitHub name + (public) email at bind. BindOwner stores them
	// fill-if-empty, so a user-set email is NEVER clobbered by a later login.
	if err := b.db.BindOwner(store.Owner{GitHubID: gu.ID, Login: gu.Login, Pubkey: pubkey, Name: gu.Name, Email: gu.Email}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not bind owner")
		return
	}
	// W1: a (re)login can change the pubkey->wallet binding, so drop the cached mapping
	// for this pubkey now rather than waiting out the TTL.
	b.invalidateOwnerWallet(pubkey)
	// Grant the starter balance to the GitHub ACCOUNT on first login, idempotent per
	// github id (the "seed:<wallet>" idem key guards re-login). Seed credits attach to
	// the account wallet, NOT to anonymous keypairs - those have no balance by design.
	wallet := "u_gh_" + strconv.FormatInt(gu.ID, 10)
	// Re-fetch the (post-BindOwner) owner so o reflects BOTH provider links on a dual-link.
	o, ownerOK, _ := b.db.OwnerByPubkey(pubkey)
	// Seed the account wallet only on the FIRST provider link (o.AppleSub == "", or the owner
	// isn't visible yet - the original always-seed fallback). On a dual-link (Apple already
	// bound) the seed instead travels across via mergeDualLinkWallet below, so we skip the
	// redundant u_gh_ seed and the account still gets exactly ONE seed (audit #6).
	if !ownerOK || o.AppleSub == "" {
		if _, seeded, _ := b.db.SeedOnce(wallet, b.seedFunds); seeded {
			b.invalidateSeedRemaining() // W6: refresh the seed-remaining promo mirror.
		}
	}
	if ownerOK {
		// Carry a funded Apple balance into the GitHub wallet so GitHub-wins precedence never
		// strands it (founder decision). No-op unless this is a dual-link with a funded Apple wallet.
		b.mergeDualLinkWallet(o)
		// Welcome the owner exactly once - the moment we first have an email for the account.
		// maybeSendWelcome claims atomically so a re-login (or a racing PATCH) can never double-send.
		b.maybeSendWelcome(o)
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

// signedInHint is a NON-secret, JS-READABLE companion to the HttpOnly sessionCookie. It
// carries no identity, no signature - just presence ("1") - so the web front-end can tell a
// logged-in visitor from a logged-out one WITHOUT a credentialed GET /account probe that
// 401s (red in the console) on every logged-out page load. The page's JS cannot read the
// HttpOnly, broker-domain session cookie, so this readable flag is what lets it skip the
// probe when there is no session. Set at login, cleared at logout, same lifetime as the
// session. Safe to be readable: it grants nothing; spends still require an Ed25519 signature.
const signedInHint = "roger_signed_in"

// webOriginHost is the host of ROGERAI_WEB_ORIGIN (e.g. "rogerai.fyi"), used as the Domain
// of the signed-in hint cookie so the web page's JS can read it (the broker, on a subdomain
// like broker.rogerai.fyi, may set a cookie for its parent domain). "" when it can't be
// parsed - the hint is then host-only on the broker (still set, just not cross-subdomain
// readable), and the front-end falls back to probing.
func webOriginHost() string {
	u, err := url.Parse(envOr("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi"))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// setWebSessionCookies sets the real credential (the HttpOnly, signed session cookie) AND
// the readable signed-in hint, both expiring at exp. Used by the OAuth callback so the two
// are always set together.
func (b *broker) setWebSessionCookies(w http.ResponseWriter, login string, id, exp int64) {
	b.setWebSessionWallet(w, login, id, "u_gh_"+strconv.FormatInt(id, 10), exp)
}

// setWebSessionWallet is setWebSessionCookies for a session carrying an EXPLICIT wallet, so a
// non-GitHub login (Sign in with Apple: githubID=0, wallet=u_apple_<…>) gets the same signed
// session credential + readable signed-in hint the GitHub callback issues.
func (b *broker) setWebSessionWallet(w http.ResponseWriter, login string, githubID int64, wallet string, exp int64) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: b.signSessionWallet(login, githubID, wallet, exp), Path: "/",
		Expires: time.Unix(exp, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: signedInHint, Value: "1", Path: "/", Domain: webOriginHost(),
		Expires: time.Unix(exp, 0), Secure: true, SameSite: http.SameSiteLaxMode,
		// Deliberately NOT HttpOnly: the web JS must read it to skip the logged-out probe.
	})
}

// clearWebSessionCookies expires BOTH the session cookie and the signed-in hint, so logging
// out leaves no stale "you're signed in" flag behind. Used by /auth/logout.
func clearWebSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode})
	http.SetCookie(w, &http.Cookie{Name: signedInHint, Value: "", Path: "/", Domain: webOriginHost(), MaxAge: -1, Secure: true, SameSite: http.SameSiteLaxMode})
}

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

// signSession signs a GitHub web session (wallet = u_gh_<githubID>). Thin wrapper over
// signSessionWallet for the GitHub callback, which only knows the github id.
func (b *broker) signSession(login string, githubID, exp int64) string {
	return b.signSessionWallet(login, githubID, "u_gh_"+strconv.FormatInt(githubID, 10), exp)
}

// signSessionWallet returns a tamper-evident cookie value "payloadB64.sigB64" where payload
// is "login|githubID|wallet|expiresUnix". Carrying the wallet EXPLICITLY lets a session
// represent a non-GitHub account - Sign in with Apple sets githubID=0 and wallet=u_apple_<…>
// - without the cookie reader having to know how to derive it. GitHub sessions keep
// wallet=u_gh_<githubID>, so their behavior is unchanged.
func (b *broker) signSessionWallet(login string, githubID int64, wallet string, exp int64) string {
	payload := login + "|" + strconv.FormatInt(githubID, 10) + "|" + wallet + "|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySession checks a cookie value's HMAC + expiry and returns the login, github id, and
// the session's wallet id. (A pre-wallet cookie - 3 fields - fails the len check and is
// treated as invalid, so old sessions simply re-login; no security impact.)
func (b *broker) verifySession(val string) (login string, githubID int64, wallet string, ok bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return "", 0, "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, "", false
	}
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", 0, "", false
	}
	f := strings.Split(string(payload), "|")
	if len(f) != 4 {
		return "", 0, "", false
	}
	gid, _ := strconv.ParseInt(f[1], 10, 64)
	exp, _ := strconv.ParseInt(f[3], 10, 64)
	if time.Now().Unix() > exp {
		return "", 0, "", false
	}
	return f[0], gid, f[2], true
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
	// SameSite=None so the browser sends this cookie on the dashboard's cross-ORIGIN
	// XHR to the broker. For the default deploy (rogerai.fyi <-> broker.rogerai.fyi,
	// same registrable domain) Lax would already suffice; None is what makes a
	// CROSS-SITE ROGERAI_WEB_ORIGIN (a different registrable domain) work too. None
	// REQUIRES Secure. Low risk: the cookie is HttpOnly, spends still require an
	// Ed25519 signature, and the only cookie-readable surfaces are GET reads + the
	// logout POST. The short-lived oauth_state cookie stays Lax (same-site callback).
	// Set the HttpOnly session credential AND the readable signed-in hint together, so the
	// web front-end can skip the logged-out /account probe (no 401 noise) - see signedInHint.
	b.setWebSessionCookies(w, gu.Login, gu.ID, exp)
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

// account handles /account: the account hub (ACCOUNT-PAYOUTS-DESIGN section 2).
//
//	GET   - profile (handle, email, github, payout status) + balances
//	PATCH - update the contact email
//
// Backed by the signed session cookie; 401 when there is no valid session.
func (b *broker) account(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	login, gid, wallet, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	switch r.Method {
	case http.MethodGet:
		b.accountGet(w, r, login, gid, wallet)
	case http.MethodPatch:
		b.accountPatch(w, r, login, gid, wallet)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// sessionOwner resolves the logged-in browser identity (login, github id, the
// github-scoped consumer wallet id). ok=false when there is no valid session.
func (b *broker) sessionOwner(r *http.Request) (login string, gid int64, wallet string, ok bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", 0, "", false
	}
	login, gid, wallet, vok := b.verifySession(c.Value)
	if !vok {
		return "", 0, "", false
	}
	return login, gid, wallet, true
}

// sessionGitHubOwner resolves a session's login to its GitHub owner row, enforcing the
// root invariant of features/security/apple_session_isolation.feature (audit finding #3):
// a session login may resolve a GitHub owner ONLY for a GitHub session, and a GitHub
// session is exactly githubID != 0. An Apple/web session (githubID == 0) never matches
// an owner row - not even on a colliding login (the literal "apple" a no-email Apple
// token used to produce vs the real github.com/apple operator).
func (b *broker) sessionGitHubOwner(login string, gid int64) (store.Owner, bool) {
	if gid == 0 {
		return store.Owner{}, false
	}
	o, found, _ := b.db.OwnerByLogin(login)
	return o, found
}

func (b *broker) accountGet(w http.ResponseWriter, r *http.Request, login string, gid int64, wallet string) {
	bal, _ := b.db.BalanceOf(wallet, b.seedFunds)
	out := map[string]any{
		"github_login": login,
		"github_id":    gid,
		"balance":      round6(bal),
		"connect":      map[string]any{"status": "none"},
	}
	// Enrich from the owner record if this login is a bound operator account
	// (GitHub sessions only - the gid gate, A1).
	if o, ok := b.sessionGitHubOwner(login, gid); ok {
		out["email"] = o.Email
		out["created_at"] = o.CreatedAt
		status := o.ConnectStatus
		if status == "" {
			status = "none"
		}
		out["connect"] = map[string]any{"status": status, "id": o.ConnectID}
		// Operator earnings split, keyed by the owner pubkey (the account id).
		if split, err := b.db.EarningSplitOf(o.Pubkey, time.Now()); err == nil {
			out["earnings"] = split
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *broker) accountPatch(w http.ResponseWriter, r *http.Request, login string, gid int64, wallet string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		jsonErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	// UpdateAccount is keyed on the GitHub login; an Apple/web session (gid==0) must never
	// write an owner row through a login collision (A1 write leg).
	if gid == 0 {
		jsonErr(w, http.StatusNotFound, "no operator account for this login (run `roger login` on a node first)")
		return
	}
	o, ok, err := b.db.UpdateAccount(login, req.Email)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !ok {
		jsonErr(w, http.StatusNotFound, "no operator account for this login (run `roger login` on a node first)")
		return
	}
	// An owner who set their email AFTER first bind still gets exactly one welcome (the
	// first-bind trigger no-ops without an email). maybeSendWelcome is a no-op when the
	// account was already welcomed, and claims the stamp atomically, so this never
	// double-sends with the bind path.
	b.maybeSendWelcome(o)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": o.Email})
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
	// Clear BOTH the session credential and the readable signed-in hint.
	clearWebSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// corsCreds allows the web origin to send the session cookie (credentialed CORS:
// an explicit origin, never "*"). Only the configured web origin is allowed. The
// allowed request headers include X-Roger-* so a signed XHR (a logged-in browser
// that ALSO carries the signing headers) preflights cleanly.
func corsCreds(w http.ResponseWriter, r *http.Request) {
	origin := envOr("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi")
	// Always vary on Origin: the response differs per origin even when we don't
	// emit the allow header (so a shared cache never serves the wrong one).
	w.Header().Add("Vary", "Origin")
	if o := r.Header.Get("Origin"); o == origin {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-Roger-Pubkey, X-Roger-TS, X-Roger-Sig, X-Roger-User, X-Roger-Admin, X-Roger-Attach")
		h.Set("Access-Control-Max-Age", "600")
	}
}

// corsCredsPreflight answers a credentialed OPTIONS preflight (204 + the explicit
// web-origin CORS headers) for the session/dashboard endpoints. Returns true when
// it handled the request so the caller can stop.
func corsCredsPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	corsCreds(w, r)
	w.WriteHeader(http.StatusNoContent)
	return true
}

// webSession returns the logged-in browser identity from the signed session cookie,
// or ok=false when there is no valid session. login is the GitHub login; wallet is
// a stable, github-scoped wallet id ("u_gh_<githubID>") distinct from the reserved
// pubkey-derived id space, so a logged-in browser (which holds the cookie, not the
// Ed25519 signing key) still has a consistent wallet to read.
func (b *broker) webSession(r *http.Request) (login, wallet string, ok bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", "", false
	}
	login, _, wallet, vok := b.verifySession(c.Value)
	if !vok {
		return "", "", false
	}
	return login, wallet, true
}

// dashIdentity resolves the wallet identity for a credentialed dashboard read
// (/me, /balance). It accepts EITHER a signed request (the CLI/proxy path, which
// owns the pubkey-derived wallet) OR a logged-in browser session cookie (which
// reads the github-scoped wallet). ok=false means neither was usable (caller 401s).
func (b *broker) dashIdentity(r *http.Request) (id string, ok bool) {
	return b.dashIdentityBody(r, nil)
}

// dashIdentityBody is dashIdentity for a request whose signature covers a body (e.g. a
// signed PATCH): the caller has already read the body and passes it so the Ed25519
// signature verifies over the same bytes. A nil body matches a GET (no body signed).
func (b *broker) dashIdentityBody(r *http.Request, body []byte) (id string, ok bool) {
	if _, w, sok := b.webSession(r); sok {
		return w, true
	}
	rid, _, iok := b.identityOf(r, body)
	if !iok {
		return "", false
	}
	// A logged-in keypair reads the SAME github-scoped wallet the web session uses
	// (one wallet); an unbound keypair reads its own pubkey-derived id.
	return b.walletOf(r, rid), true
}
