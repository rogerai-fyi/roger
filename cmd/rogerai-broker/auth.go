package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/bownux/rogerai/internal/store"
)

// gitHubAPI is the GitHub REST base (overridable in tests).
var gitHubAPI = "https://api.github.com"

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
