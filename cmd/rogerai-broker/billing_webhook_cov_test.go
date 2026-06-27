package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestWebhookMethodAndConfigGuards locks the webhook's two front-door guards: a non-POST
// is 405, and (when billing is unconfigured) any POST is 503 before signature work.
func TestWebhookMethodAndConfigGuards(t *testing.T) {
	// Billing NOT configured (empty secretKey) -> 503 on POST.
	b := &broker{db: store.NewMem(), bill: billing{}}
	w := httptest.NewRecorder()
	b.webhook(w, httptest.NewRequest(http.MethodPost, "/billing/webhook", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured webhook = %d, want 503", w.Code)
	}

	// Method guard: a GET is 405 (allow() rejects before the config check).
	b2 := &broker{db: store.NewMem(), bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test"}}
	w2 := httptest.NewRecorder()
	b2.webhook(w2, httptest.NewRequest(http.MethodGet, "/billing/webhook", nil))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET webhook = %d, want 405", w2.Code)
	}
}

// TestWebhookBadSignatureRejected locks the HMAC gate: a configured webhook with a bogus
// Stripe-Signature is rejected 400 (no event is processed).
func TestWebhookBadSignatureRejected(t *testing.T) {
	b := &broker{db: store.NewMem(), bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test"}}
	payload := []byte(`{"type":"checkout.session.completed"}`)
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(payload))
	r.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad-sig webhook = %d, want 400", w.Code)
	}
}

// TestWebhookDisputeMetadataFallback locks the dispute wallet-resolution fallback: when
// NO stored charge mapping exists (the charge predates LinkCharge), the dispute resolves
// the wallet from metadata.user and still runs the lineage clawback against it.
func TestWebhookDisputeMetadataFallback(t *testing.T) {
	t.Setenv("ROGERAI_CREDIT_USD", "1")
	mem := store.NewMem()
	b := &broker{db: mem, bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test", creditUSD: 1}}
	// Fund the consumer so the clawback debits a real wallet (no stored charge mapping).
	_, _ = mem.AddCredits("u_gh_5", 40)

	p, _ := json.Marshal(map[string]any{
		"type": "charge.dispute.created",
		"data": map[string]any{"object": map[string]any{
			"id":             "dp_meta",
			"amount":         1000, // $10 -> 10 credits
			"payment_intent": "pi_unmapped",
			"charge":         "ch_unmapped",
			"metadata":       map[string]any{"user": "u_gh_5"},
		}},
	})
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
	r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("metadata-fallback dispute = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if bal, _ := mem.PeekBalance("u_gh_5"); bal != 30 {
		t.Errorf("post-dispute wallet = %v, want 30 (clawed 10 via metadata fallback)", bal)
	}
}

// TestWebhookDisputeUnresolvable locks the "no wallet" branch: a dispute with no stored
// mapping and no metadata/client_reference_id resolves nothing -> 200, no clawback (the
// else-log path), and no wallet is touched.
func TestWebhookDisputeUnresolvable(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test", creditUSD: 1}}
	p, _ := json.Marshal(map[string]any{
		"type": "charge.dispute.created",
		"data": map[string]any{"object": map[string]any{
			"id":             "dp_orphan",
			"amount":         5000,
			"payment_intent": "pi_nowhere",
			"charge":         "ch_nowhere",
		}},
	})
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
	r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("orphan dispute = %d, want 200", w.Code)
	}
	var out map[string]bool
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out["received"] {
		t.Errorf("orphan dispute body = %v, want received:true", out)
	}
}

// TestWebhookCheckoutNoUser locks the credit-skip branch: a checkout.session.completed
// with no user and no client_reference_id credits nobody but still acks 200.
func TestWebhookCheckoutNoUser(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test", creditUSD: 1}}
	p, _ := json.Marshal(map[string]any{
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id":           "cs_nouser",
			"amount_total": 1000,
		}},
	})
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
	r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("no-user checkout = %d, want 200", w.Code)
	}
}

// TestWebhookStoreErrors covers the webhook's store-error 500 branches: a dispute whose
// WalletByCharge read errors, and a checkout whose CreditOnce write errors, both fail
// closed with a 500 rather than silently dropping the money event.
func TestWebhookStoreErrors(t *testing.T) {
	mkBroker := func() (*broker, *failStore) {
		mem := store.NewMem()
		fs := &failStore{Store: mem}
		return &broker{db: fs, bill: billing{secretKey: "sk_test", webhookSecret: "whsec_test", creditUSD: 1}}, fs
	}

	// Dispute: WalletByCharge errors -> 500.
	b, fs := mkBroker()
	fs.failWalletCh = true
	pd, _ := json.Marshal(map[string]any{
		"type": "charge.dispute.created",
		"data": map[string]any{"object": map[string]any{"id": "dp", "amount": 1000, "payment_intent": "pi"}},
	})
	rd := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(pd))
	rd.Header.Set("Stripe-Signature", stripeSig(pd, "whsec_test", time.Now().Unix()))
	wd := httptest.NewRecorder()
	b.webhook(wd, rd)
	if wd.Code != http.StatusInternalServerError {
		t.Fatalf("dispute WalletByCharge err = %d, want 500", wd.Code)
	}

	// Checkout: CreditOnce errors -> 500.
	b2, fs2 := mkBroker()
	fs2.failCreditOn = true
	pc, _ := json.Marshal(map[string]any{
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{"id": "cs", "amount_total": 1000, "metadata": map[string]any{"user": "u_gh_1"}}},
	})
	rc := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(pc))
	rc.Header.Set("Stripe-Signature", stripeSig(pc, "whsec_test", time.Now().Unix()))
	wc := httptest.NewRecorder()
	b2.webhook(wc, rc)
	if wc.Code != http.StatusInternalServerError {
		t.Fatalf("checkout CreditOnce err = %d, want 500", wc.Code)
	}
}
