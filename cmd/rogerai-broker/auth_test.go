package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// signReq attaches the user signing headers to a request for the given body.
func signReq(r *http.Request, priv ed25519.PrivateKey, body []byte) {
	pub, ts, sig := protocol.SignRequest(priv, r.Method, r.URL.Path, body)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
}

// TestAuthGitHubBindsOwner verifies POST /auth/github verifies the token against
// GitHub server-side and binds github_id<->login<->signing pubkey.
func TestAuthGitHubBindsOwner(t *testing.T) {
	// Stub GitHub /user.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99, "login": "octocat"})
	}))
	defer gh.Close()
	old := gitHubAPI
	gitHubAPI = gh.URL
	defer func() { gitHubAPI = old }()

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}}
	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	post := func(token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"access_token": token})
		r := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
		signReq(r, priv, body)
		w := httptest.NewRecorder()
		b.authGitHub(w, r)
		return w
	}

	if w := post("good-token"); w.Code != http.StatusOK {
		t.Fatalf("valid bind = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	o, ok, _ := mem.OwnerByPubkey(pubHex)
	if !ok || o.GitHubID != 99 || o.Login != "octocat" {
		t.Errorf("owner = %+v ok=%v, want octocat/99", o, ok)
	}
	// A bad GitHub token is rejected and binds nothing new.
	if w := post("bad-token"); w.Code != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", w.Code)
	}
	// Unsigned request cannot bind (no pubkey to attach the owner to).
	body, _ := json.Marshal(map[string]string{"access_token": "good-token"})
	r := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	r.Header.Set(protocol.HeaderUser, "someone")
	w := httptest.NewRecorder()
	b.authGitHub(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned bind = %d, want 401", w.Code)
	}
}

// TestKeypairBindsToOneWallet verifies the identity+wallet unification: a signed
// keypair resolves to its anonymous pubkey-derived id BEFORE login, and to the SAME
// "u_gh_<githubID>" wallet the web session uses AFTER login - one wallet per account.
// It also checks the anon-vs-logged-in gate (dashIdentityBody + walletLoggedIn, the same
// primitives /balance uses) and that seed credits land on the github account exactly once.
func TestKeypairBindsToOneWallet(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "octocat"})
	}))
	defer gh.Close()
	old := gitHubAPI
	gitHubAPI = gh.URL
	defer func() { gitHubAPI = old }()

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 100}
	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	signedID := protocol.UserIDFromPubkey(pubHex)

	// loggedInWallet replicates the prod logged-in gate (dashIdentityBody + walletLoggedIn,
	// the SAME primitives /balance uses) now that the standalone wrapper is gone: a request
	// is "logged in" only when its identity resolves AND maps to a github-scoped wallet.
	loggedInWallet := func(r *http.Request) (string, bool) {
		if id, ok := b.dashIdentityBody(r, nil); ok && walletLoggedIn(id) {
			return id, true
		}
		return "", false
	}

	// A signed /balance read BEFORE login resolves to the anonymous pubkey-derived id,
	// and the gate reports not-logged-in (no wallet).
	balReq := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/balance", nil)
		signReq(r, priv, nil)
		w := httptest.NewRecorder()
		b.balance(w, r)
		return w
	}
	if id, ok := loggedInWallet(httptest.NewRequest(http.MethodGet, "/x", nil)); ok {
		t.Errorf("unsigned request should not be logged in (got %q)", id)
	}
	{
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		signReq(r, priv, nil)
		if id, ok := loggedInWallet(r); ok {
			t.Errorf("anon keypair should not be logged in (got %q)", id)
		}
	}
	w := balReq()
	var pre struct {
		User     string  `json:"user"`
		LoggedIn bool    `json:"logged_in"`
		Balance  float64 `json:"balance"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pre)
	if pre.LoggedIn || pre.User != signedID || pre.Balance != 0 {
		t.Fatalf("anon balance = %+v, want logged_in=false, anon id, no balance", pre)
	}
	// PeekBalance must NOT have seeded the anon wallet.
	if peek, _ := mem.PeekBalance(signedID); peek != 0 {
		t.Errorf("anon wallet was seeded (%.2f) - it must have no balance", peek)
	}

	// LOG IN: bind the keypair to the github account (seeds u_gh_7 once).
	body, _ := json.Marshal(map[string]string{"access_token": "tok"})
	ar := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	signReq(ar, priv, body)
	aw := httptest.NewRecorder()
	b.authGitHub(aw, ar)
	if aw.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", aw.Code, aw.Body.String())
	}

	// Now the SAME keypair resolves to the github wallet (one wallet) + is logged in.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	signReq(r, priv, nil)
	wal, ok := loggedInWallet(r)
	if !ok || wal != "u_gh_7" {
		t.Fatalf("post-login wallet = %q ok=%v, want u_gh_7", wal, ok)
	}
	// /balance now shows the github wallet's seeded balance (== the web wallet).
	w = balReq()
	var post struct {
		User     string  `json:"user"`
		LoggedIn bool    `json:"logged_in"`
		Balance  float64 `json:"balance"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &post)
	if !post.LoggedIn || post.User != "u_gh_7" || post.Balance != 100 {
		t.Fatalf("post-login balance = %+v, want logged_in=true u_gh_7 bal=100", post)
	}
	// The web session for the same github id reads the SAME wallet id (one wallet:
	// CLI keypair and web cookie resolve to identical "u_gh_<id>").
	bWeb := &broker{priv: priv}
	wr := httptest.NewRequest(http.MethodGet, "/me", nil)
	wr.AddCookie(&http.Cookie{Name: sessionCookie, Value: bWeb.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	if _, webWallet, sok := bWeb.webSession(wr); !sok || webWallet != post.User {
		t.Errorf("web wallet %q != CLI wallet %q - not one wallet", webWallet, post.User)
	}

	// Re-login must NOT re-seed (idempotent per github id).
	ar2 := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	signReq(ar2, priv, body)
	b.authGitHub(httptest.NewRecorder(), ar2)
	if bal, _ := mem.PeekBalance("u_gh_7"); bal != 100 {
		t.Errorf("re-login balance = %.2f, want 100 (seed once per account)", bal)
	}
}

// TestSessionRoundTrip verifies the web session cookie: a freshly signed cookie
// verifies, a tampered one fails, and an expired one fails.
func TestSessionRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv}

	exp := time.Now().Add(time.Hour).Unix()
	cookie := b.signSession("octocat", 42, exp)
	login, gid, wallet, ok := b.verifySession(cookie)
	if !ok || login != "octocat" || gid != 42 || wallet != "u_gh_42" {
		t.Fatalf("verify = (%q,%d,%q,%v), want (octocat,42,u_gh_42,true)", login, gid, wallet, ok)
	}
	// Tamper: flip a char in the signature half.
	if _, _, _, ok := b.verifySession(cookie + "x"); ok {
		t.Error("tampered cookie should not verify")
	}
	if _, _, _, ok := b.verifySession("garbage"); ok {
		t.Error("garbage cookie should not verify")
	}
	// Expired.
	old := b.signSession("octocat", 42, time.Now().Add(-time.Minute).Unix())
	if _, _, _, ok := b.verifySession(old); ok {
		t.Error("expired cookie should not verify")
	}
	// A different broker key must not validate this broker's cookie.
	_, priv2, _ := ed25519.GenerateKey(nil)
	b2 := &broker{priv: priv2}
	if _, _, _, ok := b2.verifySession(cookie); ok {
		t.Error("cookie from another broker key should not verify")
	}
}

// TestAccountEndpoint verifies GET /account: 401 with no/invalid session, 200 with
// a valid session cookie.
func TestAccountEndpoint(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv, db: store.NewMem()}

	// No cookie → 401.
	w := httptest.NewRecorder()
	b.account(w, httptest.NewRequest(http.MethodGet, "/account", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no session = %d, want 401", w.Code)
	}
	// Valid cookie → 200 with the login.
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	w = httptest.NewRecorder()
	b.account(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid session = %d, want 200", w.Code)
	}
	var out struct {
		GitHubLogin string `json:"github_login"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.GitHubLogin != "octocat" {
		t.Errorf("login = %q, want octocat", out.GitHubLogin)
	}
}

// TestWebLoginUnconfigured verifies the web flow degrades safely (503) when the
// GitHub client id/secret are not set.
func TestWebLoginUnconfigured(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	b := &broker{}
	w := httptest.NewRecorder()
	b.authGitHubLogin(w, httptest.NewRequest(http.MethodGet, "/auth/github/login", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured login = %d, want 503", w.Code)
	}
}

// TestMeCredentialedCORS verifies /me answers credentialed CORS for the web origin
// (explicit origin, not "*", with allow-credentials) and a preflight 204, and that
// it reads the github-scoped wallet from a session cookie.
func TestMeCredentialedCORS(t *testing.T) {
	t.Setenv("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv, db: store.NewMem(), pubOfUser: map[string]string{}, seedFunds: 100}

	// Preflight: OPTIONS from the web origin → 204 with credentialed CORS headers.
	pre := httptest.NewRequest(http.MethodOptions, "/me", nil)
	pre.Header.Set("Origin", "https://rogerai.fyi")
	w := httptest.NewRecorder()
	b.me(w, pre)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://rogerai.fyi" {
		t.Errorf("ACAO = %q, want the explicit web origin (never *)", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC = %q, want true", got)
	}

	// GET with a session cookie → 200, github_login echoed, github-scoped wallet.
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set("Origin", "https://rogerai.fyi")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	w = httptest.NewRecorder()
	b.me(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("session /me = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://rogerai.fyi" {
		t.Errorf("GET ACAO = %q, want explicit web origin", got)
	}
	var out struct {
		User        string  `json:"user"`
		GitHubLogin string  `json:"github_login"`
		Balance     float64 `json:"balance"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.GitHubLogin != "octocat" {
		t.Errorf("github_login = %q, want octocat", out.GitHubLogin)
	}
	if out.User != "u_gh_7" {
		t.Errorf("wallet = %q, want u_gh_7 (github-scoped)", out.User)
	}
	if out.Balance != 100 {
		t.Errorf("balance = %v, want seeded 100", out.Balance)
	}

	// A request from another origin gets no allow-origin header (cookie not honored).
	other := httptest.NewRequest(http.MethodGet, "/me", nil)
	other.Header.Set("Origin", "https://evil.example")
	other.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	w = httptest.NewRecorder()
	b.me(w, other)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("foreign origin ACAO = %q, want empty", got)
	}
}

// TestRegisterEarningGate verifies the login-to-monetize gate: a priced node must
// be registered with a signed request from a GitHub-linked owner; free nodes and
// unsigned-but-priced attempts behave as specified.
func TestRegisterEarningGate(t *testing.T) {
	mem := store.NewMem()
	b := &broker{
		db:           mem,
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
		pubOfUser:    map[string]string{},
	}
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))

	mkReg := func(nodeID string, priceOut float64) []byte {
		reg := protocol.NodeRegistration{
			NodeID: nodeID, PubKey: hex.EncodeToString(nodePub), TS: time.Now().Unix(),
			Offers: []protocol.ModelOffer{{Model: "m", PriceOut: priceOut}},
		}
		reg.SignRegistration(nodePriv)
		body, _ := json.Marshal(reg)
		return body
	}
	doRegister := func(body []byte, signUser bool) int {
		r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
		if signUser {
			signReq(r, userPriv, body)
		}
		w := httptest.NewRecorder()
		b.register(w, r)
		return w.Code
	}

	// Free node (price 0), unsigned → allowed (free supply never needs login).
	if code := doRegister(mkReg("free1", 0), false); code != http.StatusOK {
		t.Errorf("free unsigned register = %d, want 200", code)
	}
	// Priced node, signed but NOT a linked owner → 403.
	if code := doRegister(mkReg("paid1", 0.5), true); code != http.StatusForbidden {
		t.Errorf("priced non-owner register = %d, want 403", code)
	}
	// Priced node, unsigned → 401 (signature required to even check ownership).
	if code := doRegister(mkReg("paid2", 0.5), false); code != http.StatusUnauthorized {
		t.Errorf("priced unsigned register = %d, want 401", code)
	}
	// Link the owner, then a priced signed register succeeds.
	_ = mem.BindOwner(store.Owner{GitHubID: 1, Login: "owner", Pubkey: userPubHex})
	if code := doRegister(mkReg("paid3", 0.5), true); code != http.StatusOK {
		t.Errorf("priced owner register = %d, want 200", code)
	}
}
