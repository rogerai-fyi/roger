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
	"strings"
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

// TestLoadBillingProdFlag locks the fail-closed production guard in loadBilling:
//
//	(a) an sk_live key -> billing ENABLED in LIVE mode;
//	(b) an sk_test key with ROGERAI_REQUIRE_LIVE unset -> ENABLED in test mode
//	    (so dev/local test keys keep working);
//	(c) ROGERAI_REQUIRE_LIVE=1 + a non-sk_live key -> billing DISABLED (secretKey
//	    AND webhookSecret cleared) so test cards can never run in production;
//	(d) an empty key -> DISABLED.
//
// This is the regression lock for the test-card-in-prod exploit fix.
func TestLoadBillingProdFlag(t *testing.T) {
	// mode reports the human-readable mode loadBilling would log for b.
	mode := func(b billing) string {
		switch {
		case b.secretKey == "":
			return "disabled"
		case strings.HasPrefix(b.secretKey, "sk_live"):
			return "LIVE"
		default:
			return "test"
		}
	}

	t.Run("sk_live key enables LIVE mode", func(t *testing.T) {
		t.Setenv("ROGERAI_REQUIRE_LIVE", "")
		t.Setenv("STRIPE_SECRET_KEY", "sk_live_realprodkey")
		t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_live")
		b := loadBilling()
		if b.secretKey == "" {
			t.Fatal("sk_live key must enable billing, got disabled")
		}
		if got := mode(b); got != "LIVE" {
			t.Errorf("mode = %q, want LIVE", got)
		}
		if b.webhookSecret != "whsec_live" {
			t.Errorf("webhookSecret = %q, want whsec_live (preserved)", b.webhookSecret)
		}
	})

	t.Run("sk_test key without REQUIRE_LIVE enables test mode", func(t *testing.T) {
		t.Setenv("ROGERAI_REQUIRE_LIVE", "")
		t.Setenv("STRIPE_SECRET_KEY", "sk_test_devkey")
		t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
		b := loadBilling()
		if b.secretKey != "sk_test_devkey" {
			t.Fatalf("sk_test key must enable billing locally, secretKey=%q", b.secretKey)
		}
		if got := mode(b); got != "test" {
			t.Errorf("mode = %q, want test", got)
		}
		if b.webhookSecret != "whsec_test" {
			t.Errorf("webhookSecret = %q, want whsec_test (preserved)", b.webhookSecret)
		}
	})

	t.Run("REQUIRE_LIVE plus non-live key disables billing (fail closed)", func(t *testing.T) {
		t.Setenv("ROGERAI_REQUIRE_LIVE", "1")
		t.Setenv("STRIPE_SECRET_KEY", "sk_test_devkey")
		t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
		b := loadBilling()
		if b.secretKey != "" {
			t.Errorf("REQUIRE_LIVE + sk_test must clear secretKey (fail closed), got %q", b.secretKey)
		}
		if b.webhookSecret != "" {
			t.Errorf("REQUIRE_LIVE + sk_test must ALSO clear webhookSecret, got %q", b.webhookSecret)
		}
	})

	t.Run("REQUIRE_LIVE plus sk_live key stays enabled", func(t *testing.T) {
		t.Setenv("ROGERAI_REQUIRE_LIVE", "1")
		t.Setenv("STRIPE_SECRET_KEY", "sk_live_realprodkey")
		t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_live")
		b := loadBilling()
		if got := mode(b); got != "LIVE" {
			t.Errorf("REQUIRE_LIVE + sk_live must stay LIVE, mode=%q key=%q", got, b.secretKey)
		}
	})

	t.Run("empty key disables billing", func(t *testing.T) {
		t.Setenv("ROGERAI_REQUIRE_LIVE", "")
		t.Setenv("STRIPE_SECRET_KEY", "")
		t.Setenv("STRIPE_WEBHOOK_SECRET", "")
		b := loadBilling()
		if b.secretKey != "" {
			t.Errorf("empty key must disable billing, got %q", b.secretKey)
		}
		if got := mode(b); got != "disabled" {
			t.Errorf("mode = %q, want disabled", got)
		}
	})
}

// TestRequireLive verifies the requireLive() truth table: only an explicit truthy
// value (1/true/yes/on, case-insensitive) opts into production; everything else,
// including an unset or empty value, is OFF (so prod is never inferred).
func TestRequireLive(t *testing.T) {
	on := []string{"1", "true", "TRUE", "yes", "YES", "on", "On"}
	for _, v := range on {
		t.Run("on/"+v, func(t *testing.T) {
			t.Setenv("ROGERAI_REQUIRE_LIVE", v)
			if !requireLive() {
				t.Errorf("ROGERAI_REQUIRE_LIVE=%q should be live", v)
			}
		})
	}
	off := []string{"", "0", "false", "no", "off", "garbage"}
	for _, v := range off {
		t.Run("off/"+v, func(t *testing.T) {
			t.Setenv("ROGERAI_REQUIRE_LIVE", v)
			if requireLive() {
				t.Errorf("ROGERAI_REQUIRE_LIVE=%q should NOT be live", v)
			}
		})
	}
}

// TestWebhookCreditsFromAmountTotal verifies credits are computed from amount_total
// (the REAL money charged), not from caller-supplied metadata: when metadata.credits
// is absent the amount_total drives the credit, and when metadata.credits DIVERGES
// (an inflated/forged value) the amount_total still wins.
func TestWebhookCreditsFromAmountTotal(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("ROGERAI_CREDIT_USD", "1")
	mem := store.NewMem()
	b := &broker{db: mem, bill: loadBilling()}

	deliver := func(sessionID, user string, amountTotal int, metaCredits string) {
		obj := map[string]any{
			"id":             sessionID,
			"amount_total":   amountTotal,
			"payment_intent": "pi_" + sessionID,
			"metadata":       map[string]any{"user": user},
		}
		if metaCredits != "" {
			obj["metadata"].(map[string]any)["credits"] = metaCredits
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "checkout.session.completed",
			"data": map[string]any{"object": obj},
		})
		r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(payload))
		r.Header.Set("Stripe-Signature", stripeSig(payload, "whsec_test", time.Now().Unix()))
		w := httptest.NewRecorder()
		b.webhook(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("webhook = %d, want 200", w.Code)
		}
	}

	// (1) No metadata.credits at all -> credit = amount_total/100 = 12.
	deliver("cs_a", "u_gh_1", 1200, "")
	if bal, _ := mem.PeekBalance("u_gh_1"); bal != 12 {
		t.Errorf("credits from amount_total (no metadata) = %v, want 12", bal)
	}

	// (2) metadata.credits DIVERGES (claims 999) but amount_total is $5 -> credit = 5
	// (metadata is advisory only; the real charge wins).
	deliver("cs_b", "u_gh_2", 500, "999")
	if bal, _ := mem.PeekBalance("u_gh_2"); bal != 5 {
		t.Errorf("credits with divergent metadata = %v, want 5 (amount_total wins)", bal)
	}
}

// TestDisputeResolvesWalletAndClawsLots is the chargeback P0 lock: a
// charge.dispute.created carries NONE of the checkout metadata (only payment_intent /
// charge), so the broker must resolve the consumer wallet via the mapping persisted at
// checkout.session.completed time, debit that consumer's wallet, and claw the still
// held/payable OPERATOR lots derived from that consumer. A redelivery is idempotent.
func TestDisputeResolvesWalletAndClawsLots(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("ROGERAI_CREDIT_USD", "1")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "90")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	mem := store.NewMem()
	b := &broker{db: mem, bill: loadBilling()}

	// An operator (pk1) owns node "n"; a consumer "u_gh_9" tops up $50 then spends 30
	// on a request served by n (so pk1 holds a 30-credit lot derived from this consumer).
	_ = mem.BindOwner(store.Owner{GitHubID: 9, Login: "op", Pubkey: "pk1"})
	_ = mem.BindNode("n", "pk1")

	// (a) checkout.session.completed -> credit the wallet AND persist the charge mapping.
	csPayload, _ := json.Marshal(map[string]any{
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id":             "cs_dispute",
			"amount_total":   5000,
			"payment_intent": "pi_dispute",
			"metadata":       map[string]any{"user": "u_gh_9"},
		}},
	})
	rcs := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(csPayload))
	rcs.Header.Set("Stripe-Signature", stripeSig(csPayload, "whsec_test", time.Now().Unix()))
	wcs := httptest.NewRecorder()
	b.webhook(wcs, rcs)
	if wcs.Code != http.StatusOK {
		t.Fatalf("session.completed = %d, want 200", wcs.Code)
	}
	if bal, _ := mem.PeekBalance("u_gh_9"); bal != 50 {
		t.Fatalf("post-topup balance = %v, want 50", bal)
	}
	// Spend 30 on a request served by node n: the consumer pays 30, pk1 earns a 30 lot.
	_, _ = mem.Hold("u_gh_9", 30)
	_, _ = mem.Finalize("u_gh_9", "n", 30, 30, 30, rec("rq1"))
	if s, _ := mem.EarningSplitOf("pk1", time.Now()); s.Held < 29.9 {
		t.Fatalf("operator held = %v, want ~30", s.Held)
	}
	walletBefore, _ := mem.PeekBalance("u_gh_9") // 50 - 30 = 20

	// (b) charge.dispute.created for $50 - carries ONLY payment_intent, no metadata.user
	// and no request_id. The wallet must be resolved via the stored mapping.
	disputeBody := func() []byte {
		p, _ := json.Marshal(map[string]any{
			"type": "charge.dispute.created",
			"data": map[string]any{"object": map[string]any{
				"id":             "dp_1",
				"amount":         5000,
				"payment_intent": "pi_dispute",
				"charge":         "ch_dispute",
			}},
		})
		return p
	}
	deliverDispute := func() int {
		p := disputeBody()
		r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
		r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
		w := httptest.NewRecorder()
		b.webhook(w, r)
		return w.Code
	}

	if c := deliverDispute(); c != http.StatusOK {
		t.Fatalf("dispute = %d, want 200", c)
	}
	// The consumer wallet is debited the full disputed amount (50).
	if bal, _ := mem.PeekBalance("u_gh_9"); bal != walletBefore-50 {
		t.Errorf("post-dispute wallet = %v, want %v (debited 50)", bal, walletBefore-50)
	}
	// The operator lot derived from this consumer is clawed (no longer held/payable).
	if s, _ := mem.EarningSplitOf("pk1", time.Now()); s.Held+s.Payable > 1e-6 {
		t.Errorf("operator held+payable after claw = %v, want 0 (lot clawed)", s.Held+s.Payable)
	}

	// (c) Idempotent: a redelivery of the SAME dispute id does not double-debit.
	walletAfterFirst, _ := mem.PeekBalance("u_gh_9")
	if c := deliverDispute(); c != http.StatusOK {
		t.Fatalf("dispute redelivery = %d, want 200", c)
	}
	if bal, _ := mem.PeekBalance("u_gh_9"); bal != walletAfterFirst {
		t.Errorf("redelivered dispute changed wallet %v -> %v (must be idempotent)", walletAfterFirst, bal)
	}
}

// stripeSig builds a valid Stripe-Signature header (t=<ts>,v1=<hmac>) for payload.
func stripeSig(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
