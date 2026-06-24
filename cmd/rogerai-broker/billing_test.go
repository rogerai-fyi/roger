package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestCheckoutWalletResolution verifies the top-up wallet resolution: a logged-in
// web session credits its session wallet; a logged-in keypair credits the SAME
// github-scoped wallet (one wallet per account); an anonymous keypair credits its
// own pubkey-derived wallet; an unsigned/unauthenticated request resolves nothing.
func TestCheckoutWalletResolution(t *testing.T) {
	mem := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{db: mem, pubOfUser: map[string]string{}, priv: brokerPriv}

	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	signedID := protocol.UserIDFromPubkey(pubHex)

	// (1) web session -> the session wallet "u_gh_<gid>".
	rWeb := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	rWeb.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	if w, ok := b.checkoutWallet(rWeb, nil); !ok || w != "u_gh_7" {
		t.Errorf("web-session wallet = %q ok=%v, want u_gh_7", w, ok)
	}

	// (2) anonymous (signed, not logged in) keypair -> its pubkey-derived wallet.
	rAnon := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	signReq(rAnon, priv, nil)
	if w, ok := b.checkoutWallet(rAnon, nil); !ok || w != signedID {
		t.Errorf("anon keypair wallet = %q ok=%v, want %q", w, ok, signedID)
	}

	// (3) the SAME keypair AFTER login binds to "u_gh_7" -> one wallet per account.
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	rBound := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	signReq(rBound, priv, nil)
	if w, ok := b.checkoutWallet(rBound, nil); !ok || w != "u_gh_7" {
		t.Errorf("logged-in keypair wallet = %q ok=%v, want u_gh_7 (one wallet)", w, ok)
	}

	// (4) a request that OFFERS a signature which does not verify -> rejected (a bad
	// signature is never silently downgraded to anonymous).
	rBad := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	rBad.Header.Set(protocol.HeaderPubkey, pubHex)
	rBad.Header.Set(protocol.HeaderTS, strconv.FormatInt(time.Now().Unix(), 10))
	rBad.Header.Set(protocol.HeaderSig, "deadbeef")
	if w, ok := b.checkoutWallet(rBad, nil); ok {
		t.Errorf("a bad signature must not resolve a wallet, got %q", w)
	}

	// (5) fully unsigned (no headers, no session) -> the legacy anonymous wallet
	// (anon top-up is allowed; the credit is claimable on login).
	rAnonLegacy := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	if w, ok := b.checkoutWallet(rAnonLegacy, nil); !ok || w != "anon" {
		t.Errorf("unsigned anon top-up = %q ok=%v, want anon/true", w, ok)
	}
}

// TestWebhookCreditOnceIdempotent verifies the Stripe checkout webhook credits a
// wallet EXACTLY ONCE even when Stripe redelivers the same session (at-least-once).
func TestWebhookCreditOnceIdempotent(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	mem := store.NewMem()
	b := &broker{db: mem, bill: loadBilling()}

	deliver := func() int {
		evt := map[string]any{
			"type": "checkout.session.completed",
			"data": map[string]any{"object": map[string]any{
				"id":                  "cs_test_123",
				"client_reference_id": "u_gh_7",
				"amount_total":        2500,
				"metadata":            map[string]any{"user": "u_gh_7", "credits": "25"},
			}},
		}
		payload, _ := json.Marshal(evt)
		r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(payload))
		r.Header.Set("Stripe-Signature", stripeSig(payload, "whsec_test", time.Now().Unix()))
		w := httptest.NewRecorder()
		b.webhook(w, r)
		return w.Code
	}

	if c := deliver(); c != http.StatusOK {
		t.Fatalf("first delivery = %d, want 200", c)
	}
	if c := deliver(); c != http.StatusOK {
		t.Fatalf("redelivery = %d, want 200 (idempotent)", c)
	}
	bal, _ := mem.PeekBalance("u_gh_7")
	if bal != 25 {
		t.Errorf("balance after two deliveries = %v, want 25 (credited once)", bal)
	}
}

// TestStripeSigReplayWindow verifies the webhook signature check: a valid current
// signature passes; a stale (or future) timestamp outside the +/-5min tolerance is
// rejected (replay deterrence); a wrong secret or malformed header is rejected.
func TestStripeSigReplayWindow(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"hello":"world"}`)
	now := time.Now().Unix()

	if !verifyStripeSig(stripeSig(payload, secret, now), payload, secret) {
		t.Error("a fresh, correctly-signed payload should verify")
	}
	// Stale: 10 minutes in the past (> 300s tolerance) -> rejected.
	if verifyStripeSig(stripeSig(payload, secret, now-600), payload, secret) {
		t.Error("a 10-minute-old signature must be rejected (replay window)")
	}
	// Future skew beyond tolerance -> rejected.
	if verifyStripeSig(stripeSig(payload, secret, now+600), payload, secret) {
		t.Error("a far-future timestamp must be rejected")
	}
	// Wrong secret -> rejected even with a current timestamp.
	if verifyStripeSig(stripeSig(payload, "whsec_wrong", now), payload, secret) {
		t.Error("a signature under the wrong secret must be rejected")
	}
	// Malformed / empty headers -> rejected.
	if verifyStripeSig("", payload, secret) || verifyStripeSig("t=,v1=", payload, secret) {
		t.Error("malformed signature headers must be rejected")
	}
	// Empty secret (billing not configured) -> never verifies.
	if verifyStripeSig(stripeSig(payload, secret, now), payload, "") {
		t.Error("an empty secret must never verify")
	}
}

// stripeSig builds a valid Stripe-Signature header (t=<ts>,v1=<hmac>) for payload.
func stripeSig(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
