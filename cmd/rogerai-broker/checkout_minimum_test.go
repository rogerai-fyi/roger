package main

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/store"
)

// The checkout handler used to read the amount like this:
//
//	_ = json.Unmarshal(body, &req)
//	if req.USD < 1 { req.USD = 10 }
//
// so a request for $0.50 opened a Stripe session for $10, and a body that did not parse
// at all opened one for $10 as well - the Unmarshal error was discarded. That is the
// enforcement point, which made it the last and worst of four disagreeing floors: the CLI
// refused <= $0, the web console refused <= $0, and the broker quietly substituted a
// different charge for anything under a dollar.
//
// Substituting an amount is never the right answer on a money path. The floor is one
// constant (client.MinTopupUSD) and a request under it is refused.
func newCheckoutBroker(t *testing.T) (*broker, ed25519.PrivateKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{db: store.NewMem(), pubOfUser: map[string]string{}, priv: brokerPriv}
	// Billing configured, so these exercise the amount check on the real path rather
	// than the not-configured branch (TestCheckoutDisabled owns that one). Every case
	// below is refused before any Stripe call is made.
	b.bill = billing{secretKey: "sk_test_not_used", creditUSD: 1, successURL: "https://x/ok", cancelURL: "https://x/no"}
	return b, priv
}

func postCheckout(t *testing.T, b *broker, priv ed25519.PrivateKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	raw := []byte(body)
	r := httptest.NewRequest(http.MethodPost, "/billing/checkout", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	signReq(r, priv, raw)
	w := httptest.NewRecorder()
	b.checkout(w, r)
	return w
}

func TestCheckoutRefusesAnAmountBelowTheMinimum(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	for _, body := range []string{
		`{"usd":0.5}`,
		`{"usd":0.99}`,
		`{"usd":0}`,
		`{"usd":-5}`,
	} {
		w := postCheckout(t, b, priv, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("checkout %s = %d, want 400 (a below-minimum amount is refused, never substituted)", body, w.Code)
		}
		if bytes.Contains(w.Body.Bytes(), []byte("checkout.stripe.com")) {
			t.Errorf("checkout %s opened a session anyway", body)
		}
	}
}

func TestCheckoutRefusesAnUnreadableBody(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	for _, body := range []string{
		`{"usd":"twenty"}`,
		`not json at all`,
		`{`,
	} {
		w := postCheckout(t, b, priv, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("checkout %q = %d, want 400 (an unparseable body is not a $10 top-up)", body, w.Code)
		}
	}
}

// The refusal has to name the floor, and the floor has to be the shared one. A message
// that says "invalid amount" leaves the caller guessing at a number we already know.
func TestCheckoutRefusalNamesTheMinimum(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	w := postCheckout(t, b, priv, `{"usd":0.5}`)
	want := fmt.Sprintf("top-up minimum is $%.0f", client.MinTopupUSD)
	if !bytes.Contains(w.Body.Bytes(), []byte(want)) {
		t.Errorf("refusal = %s, want it to say %q", w.Body.String(), want)
	}
}

// A fraction of a cent cannot be charged, so an amount like $1.999 has to become some
// other number on the way to Stripe. The enforcement point refuses it rather than
// choosing that number on the caller's behalf.
func TestCheckoutRefusesASubCentAmount(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	for _, body := range []string{`{"usd":1.999}`, `{"usd":1.333}`, `{"usd":10.005}`} {
		w := postCheckout(t, b, priv, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("checkout %s = %d, want 400 (a sub-cent amount is refused, not truncated)", body, w.Code)
		}
	}
}

// Nothing bounded the amount from above, so a request large enough to overflow int64 on
// the way to integer cents reached Stripe as a NEGATIVE unit_amount. The ceiling is
// Stripe's own line-item maximum, refused here with a message rather than there with a 502.
func TestCheckoutRefusesAnAmountAboveTheMaximum(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	for _, body := range []string{
		`{"usd":1000000}`,
		`{"usd":1e18}`,
	} {
		w := postCheckout(t, b, priv, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("checkout %s = %d, want 400 (above the maximum is refused here, not at Stripe)", body, w.Code)
		}
	}
}
