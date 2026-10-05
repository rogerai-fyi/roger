package main

// cap_notice_followups_bdd_test.go: steps for the cap_notice_emails.feature scenarios added
// after the audit (sponsored grants notify the grant owner) and by the founder's 2026-10-04
// ruling (an existing provider-account address is re-checked at the next GitHub or Apple
// sign-in). The sign-ins run through the REAL handlers (authGitHub against a stubbed GitHub
// /user, authApple against a stubbed JWKS and a minted identity token).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// stationOwnerCapSpent binds a GitHub-linked owner at `addr`, stands up a paid station it
// operates, and gives the owner's account wallet a cap and month spend.
func (s *cnState) stationOwnerCapSpent(addr, cap, spent string) error {
	addr = s.u(addr)
	pub, priv, _ := ed25519.GenerateKey(nil)
	ph := hex.EncodeToString(pub)
	s.acctOwner = store.Owner{GitHubID: eaGID(), Login: "own-" + s.nonce, Pubkey: ph, Email: addr}
	if err := s.db.BindOwner(s.acctOwner); err != nil {
		return err
	}
	s.standUp("n-own", stationOpts{model: "qwen3-32b", priceIn: 40, priceOut: 0.1, ctx: 8192, ownerPriv: priv})
	o, ok, err := s.db.OwnerByPubkey(ph)
	if err != nil || !ok {
		return fmt.Errorf("owner row after stand-up: ok=%v err=%v", ok, err)
	}
	if o.Email != addr {
		return fmt.Errorf("owner address is %q after stand-up, want %q", o.Email, addr)
	}
	s.acctOwner = o
	w, ok := accountWalletForOwner(o)
	if !ok {
		return fmt.Errorf("no account wallet for the station owner")
	}
	s.acctKey, s.acctAddr, s.acctWallet = priv, addr, w
	if err := s.fundAcct(); err != nil {
		return err
	}
	if err := s.setCap(cap); err != nil {
		return err
	}
	return s.spent(spent)
}

func (s *cnState) issueGrant(kind string) error {
	secret := "rog-grant_cn" + utNonce()
	sum := sha256.Sum256([]byte(secret))
	g := store.Grant{ID: "grant_cn" + utNonce(), SecretHash: hex.EncodeToString(sum[:]), Owner: s.acctOwner.Pubkey,
		Label: "bot", CreatedAt: time.Now().Unix()}
	if kind == "free" {
		g.Free = true
	} else {
		g.PriceIn, g.PriceOut = 40, 0.1
	}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	s.grantSecret = secret
	return nil
}

func (s *cnState) botRelaysWithGrant(paid bool) error {
	rec := s.relayAs("", nil, "qwen3-32b", "", false, map[string]string{"Authorization": "Bearer " + s.grantSecret})
	if rec.Code != 200 {
		return fmt.Errorf("the grant relay was not served: %d %s", rec.Code, rec.Body.String())
	}
	if paid {
		if c := rec.Header().Get("X-RogerAI-Cost"); c == "" || c == "0" {
			return fmt.Errorf("the grant relay was not billed (X-RogerAI-Cost=%q)", c)
		}
	}
	return nil
}

// legacyAccountCapSpent is a provider account whose address was stored before typed
// addresses were tracked: on the row, not proven by code, not marked unproven.
func (s *cnState) legacyAccountCapSpent(kind, addr, cap, spent string) error {
	if err := s.accountWithCapSpent(kind, addr, cap, spent); err != nil {
		return err
	}
	o, ok, err := s.db.OwnerByPubkey(s.acctOwner.Pubkey)
	if err != nil || !ok {
		return fmt.Errorf("owner row: ok=%v err=%v", ok, err)
	}
	if o.EmailUnproven || o.EmailVerifiedAt != 0 {
		return fmt.Errorf("setup: the legacy address is marked unproven=%v verified=%d", o.EmailUnproven, o.EmailVerifiedAt)
	}
	return nil
}

// signsInWith runs the real provider sign-in for the account's CLI key. reported "" = the
// provider reports no address.
func (s *cnState) signsInWith(provider, reported string) error {
	if reported != "" {
		reported = s.u(reported)
	}
	switch provider {
	case "GitHub":
		restore := ghUserStub(s.t, s.acctOwner.GitHubID, s.acctOwner.Login, "", reported)
		defer restore()
		body := []byte(`{"access_token":"tok-cn"}`)
		req := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
		signReq(req, s.acctKey, body)
		rec := httptest.NewRecorder()
		s.b.authGitHub(rec, req)
		if rec.Code != 200 {
			return fmt.Errorf("GitHub sign-in = %d %s", rec.Code, rec.Body.String())
		}
	case "Apple":
		rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		jwks := newJWKS(jwkFor("cn", &rsaKey.PublicKey))
		defer jwks.Close()
		useJWKS(s.t, jwks)
		raw := "cn-nonce-" + s.nonce
		claims := goodClaims(s.acctOwner.AppleSub, raw)
		if reported == "" {
			delete(claims, "email")
		} else {
			claims["email"] = reported
		}
		body := appleBody(mintToken(rsaKey, "cn", "RS256", claims), raw, "")
		req := httptest.NewRequest(http.MethodPost, "/auth/apple", bytes.NewReader(body))
		signReq(req, s.acctKey, body)
		rec := httptest.NewRecorder()
		s.b.authApple(rec, req)
		if rec.Code != 200 {
			return fmt.Errorf("Apple sign-in = %d %s", rec.Code, rec.Body.String())
		}
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
	return nil
}

var _ = strconv.Itoa
