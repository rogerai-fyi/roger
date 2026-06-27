package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeStripe routes the Stripe REST calls the broker makes to canned responses, and
// points stripeAPIBase at it (restored on cleanup). It fakes the EXTERNAL Stripe API
// boundary, not any broker logic.
func fakeStripe(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test" {
			t.Errorf("stripe call missing bearer key, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/accounts" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"acct_test"}`))
		case r.URL.Path == "/v1/account_links":
			_, _ = w.Write([]byte(`{"url":"https://connect.stripe.test/onboard"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/accounts/"): // GET account (refresh status)
			_, _ = w.Write([]byte(`{"capabilities":{"transfers":"active"},"requirements":{"disabled_reason":""}}`))
		case r.URL.Path == "/v1/checkout/sessions":
			_, _ = w.Write([]byte(`{"id":"cs_test","url":"https://checkout.stripe.test/pay"}`))
		default: // transfers + reversals
			_, _ = w.Write([]byte(`{"id":"obj_test"}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := stripeAPIBase
	stripeAPIBase = srv.URL
	t.Cleanup(func() { stripeAPIBase = old })
}

// TestStripeForm covers the low-level Stripe request helper: it sends the bearer key,
// decodes the response, and returns the status code.
func TestStripeForm(t *testing.T) {
	fakeStripe(t)
	c := connect{secretKey: "sk_test"}
	var out struct {
		ID string `json:"id"`
	}
	code, err := c.stripeForm(http.MethodPost, "/v1/accounts", url.Values{"type": {"express"}}, &out)
	if err != nil || code != http.StatusOK || out.ID != "acct_test" {
		t.Fatalf("stripeForm = %d / %q / %v, want 200 acct_test", code, out.ID, err)
	}
}

// TestConnectOnboardStripe covers the real (keyed) onboarding path: create the connected
// account, create the account link, and return the Stripe URL.
func TestConnectOnboardStripe(t *testing.T) {
	fakeStripe(t)
	b, _ := brokerWithOwner(t)
	b.conn = connect{secretKey: "sk_test", returnURL: "https://ret", refreshURL: "https://ref"}

	r := sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7)
	w := httptest.NewRecorder()
	b.connectOnboard(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("connectOnboard(stripe) = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["url"] != "https://connect.stripe.test/onboard" || resp["status"] != "onboarding" {
		t.Errorf("onboard resp = %+v, want the stripe account link", resp)
	}
	// The created connected-account id was persisted.
	if o, _, _ := b.db.OwnerByLogin("octocat"); o.ConnectID != "acct_test" {
		t.Errorf("connect id not persisted: %q", o.ConnectID)
	}
}

// TestConnectStatusStripe covers connectStatus's keyed branch: it refreshes from Stripe
// (transfers capability active -> status active) and reports can_payout.
func TestConnectStatusStripe(t *testing.T) {
	fakeStripe(t)
	b, _ := brokerWithOwner(t)
	b.conn = connect{secretKey: "sk_test"}
	// Bind a Connect account on the owner so the refresh path runs.
	_ = b.db.SetConnect("octocat", "acct_test", "onboarding")

	r := sessionReq(b, http.MethodGet, "/connect/status", "octocat", 7)
	w := httptest.NewRecorder()
	b.connectStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("connectStatus = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "active" || resp["can_payout"] != true {
		t.Errorf("connectStatus = %+v, want active + can_payout", resp)
	}
}

// TestCheckoutStripe covers the full top-up checkout against a fake Stripe: a signed
// session creates a checkout session and returns its URL + credit count.
func TestCheckoutStripe(t *testing.T) {
	fakeStripe(t)
	b, _ := brokerWithOwner(t)
	b.bill.secretKey = "sk_test"
	b.bill.creditUSD = 1
	b.bill.successURL = "https://ok"
	b.bill.cancelURL = "https://no"

	r := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(`{"usd":15}`))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())})
	w := httptest.NewRecorder()
	b.checkout(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("checkout = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["url"] != "https://checkout.stripe.test/pay" || resp["credits"].(float64) != 15 {
		t.Errorf("checkout resp = %+v, want the stripe session url + 15 credits", resp)
	}
}

// TestPayoutTransferHTTP covers the real Stripe-HTTP transfer + reversal paths (no seam,
// keyed, non-stub ids) against a fake Stripe.
func TestPayoutTransferHTTP(t *testing.T) {
	fakeStripe(t)
	b := &broker{}
	b.bill.creditUSD = 1
	b.conn = connect{secretKey: "sk_test"} // no transfer seam -> HTTP path

	id, err := b.payoutTransfer("acct_real", "octocat", 3.0, "idem")
	if err != nil || id != "obj_test" {
		t.Fatalf("payoutTransfer(http) = %q/%v, want obj_test", id, err)
	}
	rid, err := b.payoutTransferReversal("tr_real", 2.0, "idem")
	if err != nil || rid != "obj_test" {
		t.Fatalf("payoutTransferReversal(http) = %q/%v, want obj_test", rid, err)
	}
}

// TestRefreshConnectStatus covers the capability->status mapping directly: transfers
// "active" -> "active", persisted on the owner.
func TestRefreshConnectStatus(t *testing.T) {
	fakeStripe(t)
	b, _ := brokerWithOwner(t)
	b.conn = connect{secretKey: "sk_test"}
	if st := b.refreshConnectStatus("octocat", "acct_test"); st != "active" {
		t.Errorf("refreshConnectStatus = %q, want active", st)
	}
}
