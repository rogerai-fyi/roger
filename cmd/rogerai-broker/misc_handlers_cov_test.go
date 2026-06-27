package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// setStripeBase points stripeAPIBase at a handler for the duration of the test.
func setStripeBase(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := stripeAPIBase
	stripeAPIBase = srv.URL
	t.Cleanup(func() { stripeAPIBase = old })
}

// badSigReq returns a GET carrying an offered-but-INVALID signature, so identity
// resolution fails (the !iok 401 branch) rather than resolving to anon.
func badSigReq(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	_, priv, _ := ed25519.GenerateKey(nil)
	signReq(r, priv, nil)
	r.Header.Set(protocol.HeaderSig, "deadbeef")
	return r
}

// TestCheckoutErrorPaths covers the checkout guards + the Stripe failure: a non-POST is
// 405, an invalid signature is 401, and a Stripe 5xx surfaces a 502.
func TestCheckoutErrorPaths(t *testing.T) {
	b, _ := brokerWithOwner(t)
	b.bill.secretKey = "sk_test"
	b.bill.creditUSD = 1

	// Method guard.
	wm := httptest.NewRecorder()
	b.checkout(wm, httptest.NewRequest(http.MethodGet, "/billing/checkout", nil))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET checkout = %d, want 405", wm.Code)
	}

	// Bad signature -> 401.
	body := []byte(`{"usd":5}`)
	r := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(string(body)))
	_, priv, _ := ed25519.GenerateKey(nil)
	signReq(r, priv, body)
	r.Header.Set(protocol.HeaderSig, "deadbeef")
	wbad := httptest.NewRecorder()
	b.checkout(wbad, r)
	if wbad.Code != http.StatusUnauthorized {
		t.Fatalf("bad-sig checkout = %d, want 401", wbad.Code)
	}

	// Stripe 5xx -> 502.
	setStripeBase(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	rs := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(`{"usd":5}`))
	rs.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, 9999999999)})
	ws := httptest.NewRecorder()
	b.checkout(ws, rs)
	if ws.Code != http.StatusBadGateway {
		t.Fatalf("stripe-error checkout = %d, want 502 (%s)", ws.Code, ws.Body.String())
	}
}

// TestConnectOnboardErrorPaths covers connectOnboard's guards + Stripe failures: a non-POST
// is 405, a non-operator session is 403, an account-create failure is 502, and (with a
// pre-existing connect id) an account-link failure is 502.
func TestConnectOnboardErrorPaths(t *testing.T) {
	b, _ := brokerWithOwner(t)

	// Method guard.
	wm := httptest.NewRecorder()
	b.connectOnboard(wm, httptest.NewRequest(http.MethodGet, "/connect/onboard", nil))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET onboard = %d, want 405", wm.Code)
	}

	// Non-operator session -> 403.
	wf := httptest.NewRecorder()
	b.connectOnboard(wf, sessionReq(b, http.MethodPost, "/connect/onboard", "stranger", 999))
	if wf.Code != http.StatusForbidden {
		t.Fatalf("non-operator onboard = %d, want 403", wf.Code)
	}

	// Keyed: account creation fails -> 502.
	b.conn.secretKey = "sk_test"
	setStripeBase(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	wa := httptest.NewRecorder()
	b.connectOnboard(wa, sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7))
	if wa.Code != http.StatusBadGateway {
		t.Fatalf("account-create-fail onboard = %d, want 502 (%s)", wa.Code, wa.Body.String())
	}

	// With a pre-existing connect id, account creation is skipped; the account_link call
	// fails -> 502.
	_ = b.db.SetConnect("octocat", "acct_existing", "onboarding")
	setStripeBase(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "account_links") {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"id":"acct_existing"}`))
	})
	wl := httptest.NewRecorder()
	b.connectOnboard(wl, sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7))
	if wl.Code != http.StatusBadGateway {
		t.Fatalf("link-fail onboard = %d, want 502 (%s)", wl.Code, wl.Body.String())
	}
}

// TestConnectStatusGuards covers connectStatus's method + operator guards.
func TestConnectStatusGuards(t *testing.T) {
	b, _ := brokerWithOwner(t)
	wm := httptest.NewRecorder()
	b.connectStatus(wm, httptest.NewRequest(http.MethodPost, "/connect/status", nil))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST connect/status = %d, want 405", wm.Code)
	}
	wf := httptest.NewRecorder()
	b.connectStatus(wf, sessionReq(b, http.MethodGet, "/connect/status", "stranger", 999))
	if wf.Code != http.StatusForbidden {
		t.Fatalf("non-operator connect/status = %d, want 403", wf.Code)
	}
}

// TestDashIdentityHandlers401 covers the invalid-signature 401 of the dash reads that go
// through dashIdentity (balance is tested elsewhere): usage, me, and billing.
func TestDashIdentityHandlers401(t *testing.T) {
	b, _ := brokerWithOwner(t)
	for name, h := range map[string]http.HandlerFunc{
		"usage":   b.usage,
		"me":      b.me,
		"billing": b.billing,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h(w, badSigReq(http.MethodGet, "/"+name))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s bad-sig = %d, want 401", name, w.Code)
			}
		})
	}
}

// TestOwnerStrikesNonOperator covers the logged-in-but-not-an-operator branch: a session
// whose login has an empty pubkey gets a well-formed empty strikes body (200), not a 403.
func TestOwnerStrikesNonOperator(t *testing.T) {
	b, _ := brokerWithOwner(t)
	_ = b.db.BindOwner(store.Owner{GitHubID: 8, Login: "nopub", Pubkey: ""})
	w := httptest.NewRecorder()
	b.ownerStrikes(w, sessionReq(b, http.MethodGet, "/owner/strikes", "nopub", 8))
	if w.Code != http.StatusOK {
		t.Fatalf("non-operator ownerStrikes = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"count":0`) {
		t.Errorf("body = %q, want count:0", w.Body.String())
	}
}
