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

	"github.com/bownux/rogerai/internal/protocol"
	"github.com/bownux/rogerai/internal/store"
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

// TestSessionRoundTrip verifies the web session cookie: a freshly signed cookie
// verifies, a tampered one fails, and an expired one fails.
func TestSessionRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv}

	exp := time.Now().Add(time.Hour).Unix()
	cookie := b.signSession("octocat", 42, exp)
	login, gid, ok := b.verifySession(cookie)
	if !ok || login != "octocat" || gid != 42 {
		t.Fatalf("verify = (%q,%d,%v), want (octocat,42,true)", login, gid, ok)
	}
	// Tamper: flip a char in the signature half.
	if _, _, ok := b.verifySession(cookie + "x"); ok {
		t.Error("tampered cookie should not verify")
	}
	if _, _, ok := b.verifySession("garbage"); ok {
		t.Error("garbage cookie should not verify")
	}
	// Expired.
	old := b.signSession("octocat", 42, time.Now().Add(-time.Minute).Unix())
	if _, _, ok := b.verifySession(old); ok {
		t.Error("expired cookie should not verify")
	}
	// A different broker key must not validate this broker's cookie.
	_, priv2, _ := ed25519.GenerateKey(nil)
	b2 := &broker{priv: priv2}
	if _, _, ok := b2.verifySession(cookie); ok {
		t.Error("cookie from another broker key should not verify")
	}
}

// TestAccountEndpoint verifies GET /account: 401 with no/invalid session, 200 with
// a valid session cookie.
func TestAccountEndpoint(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv}

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
