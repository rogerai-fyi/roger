package main

// dual_link_merge_test.go pins that a funded Apple wallet is MERGED into the GitHub account
// wallet when both providers are linked on one pubkey, instead of being stranded by the
// GitHub-wins wallet precedence (audit finding #6, founder decision: merge at link time).
// Real /auth/apple + /auth/github handlers over store.NewMem(), no mocks; reuses the Apple
// JWKS harness (apple_test.go) and the GitHub /user stub pattern (auth_test.go).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

func TestDualLinkMergesAppleBalanceIntoGitHub(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := newJWKS(jwkFor("k1", &rsaKey.PublicKey))
	defer jwks.Close()
	useJWKS(t, jwks)

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "octocat"})
	}))
	defer gh.Close()
	oldGH := gitHubAPI
	gitHubAPI = gh.URL
	defer func() { gitHubAPI = oldGH }()

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 100}

	// One device pubkey binds BOTH providers.
	_, priv, _ := ed25519.GenerateKey(nil)
	appleWallet := "" // resolved after the Apple bind

	// 1) Apple bind first: seeds u_apple_<sub> = 100.
	const sub = "000777.applesub.roger"
	const raw = "device-nonce-1"
	aTok := mintToken(rsaKey, "k1", "RS256", goodClaims(sub, raw))
	rA := httptest.NewRequest(http.MethodPost, "/auth/apple", bytes.NewReader(appleBody(aTok, raw, "Ada")))
	signReq(rA, priv, appleBody(aTok, raw, "Ada"))
	wA := httptest.NewRecorder()
	b.authApple(wA, rA)
	if wA.Code != http.StatusOK {
		t.Fatalf("apple bind = %d (%s)", wA.Code, wA.Body.String())
	}
	appleWallet = walletForAppleSub(sub)
	if bal, _ := mem.PeekBalance(appleWallet); bal != 100 {
		t.Fatalf("apple wallet after bind = %.2f, want 100 (seed)", bal)
	}

	// 2) The user tops up their Apple wallet (real money) -> 150.
	if _, err := mem.AddCredits(appleWallet, 50); err != nil {
		t.Fatal(err)
	}

	// 3) GitHub link on the SAME pubkey.
	body, _ := json.Marshal(map[string]string{"access_token": "good-token"})
	rG := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	signReq(rG, priv, body)
	wG := httptest.NewRecorder()
	b.authGitHub(wG, rG)
	if wG.Code != http.StatusOK {
		t.Fatalf("github link = %d (%s)", wG.Code, wG.Body.String())
	}

	// The account wallet is now u_gh_7 and it must hold the full 150 (100 seed + 50 topup),
	// merged from Apple - NOT a fresh second 100 seed. The Apple wallet must be drained to 0.
	if bal, _ := mem.PeekBalance("u_gh_7"); bal != 150 {
		t.Fatalf("u_gh_7 = %.2f, want 150 (apple balance merged, no double seed)", bal)
	}
	if bal, _ := mem.PeekBalance(appleWallet); bal != 0 {
		t.Fatalf("apple wallet = %.2f, want 0 (merged into u_gh_7, not stranded)", bal)
	}
	// Derived balance (ledger) agrees with the cached balance on both wallets - the paired
	// adjustment rows keep the drift check consistent.
	if d, _ := mem.DeriveBalance("u_gh_7"); d != 150 {
		t.Errorf("u_gh_7 derived = %.2f, want 150", d)
	}
	if d, _ := mem.DeriveBalance(appleWallet); d != 0 {
		t.Errorf("apple derived = %.2f, want 0", d)
	}

	// 4) Idempotent: a re-login (re-link) does not move funds again or re-seed.
	rG2 := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	signReq(rG2, priv, body)
	b.authGitHub(httptest.NewRecorder(), rG2)
	if bal, _ := mem.PeekBalance("u_gh_7"); bal != 150 {
		t.Errorf("u_gh_7 after re-login = %.2f, want 150 (idempotent)", bal)
	}
}

// TestMergeWalletStorePrimitive checks the store primitive directly: balance + seed move, the
// source is drained, derived balances stay consistent, and a second merge is a no-op.
func TestMergeWalletStorePrimitive(t *testing.T) {
	mem := store.NewMem()
	if _, err := mem.AddCredits("from", 150); err != nil {
		t.Fatal(err)
	}
	moved, err := mem.MergeWallet("from", "to")
	if err != nil || moved != 150 {
		t.Fatalf("MergeWallet moved=%.2f err=%v, want 150", moved, err)
	}
	if b, _ := mem.PeekBalance("from"); b != 0 {
		t.Errorf("from = %.2f, want 0", b)
	}
	if b, _ := mem.PeekBalance("to"); b != 150 {
		t.Errorf("to = %.2f, want 150", b)
	}
	if d, _ := mem.DeriveBalance("to"); d != 150 {
		t.Errorf("to derived = %.2f, want 150", d)
	}
	if again, _ := mem.MergeWallet("from", "to"); again != 0 {
		t.Errorf("second merge moved %.2f, want 0 (idempotent)", again)
	}
}

var _ = hex.EncodeToString // keep hex import stable if the harness signature shifts
