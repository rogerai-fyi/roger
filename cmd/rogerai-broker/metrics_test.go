package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// signedMetricsGET builds an Ed25519-signed GET whose signature covers the BARE path
// (no query) - matching how the broker verifies (identityOf signs over r.URL.Path), so
// a `?days=` query string does not break the signature.
func signedMetricsGET(fullPath, barePath string, priv ed25519.PrivateKey) *http.Request {
	r := httptest.NewRequest(http.MethodGet, fullPath, nil)
	pub, ts, sig := protocol.SignRequest(priv, http.MethodGet, barePath, nil)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
	return r
}

// newMetricsBroker wires a broker with a bound operator (pubkey = hex of priv's public
// key, github id 7 -> wallet "u_gh_7"), one owned node, and a handful of settled
// receipts so the per-model rollups have data. It returns the broker, the consumer
// wallet id, and the operator's signing key (for the signed-CLI auth path).
func newMetricsBroker(t *testing.T) (*broker, string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	node := "node-1"
	_ = db.BindNode(node, pubHex)
	wallet := "u_gh_7" // the github-scoped wallet the operator's web session + signed reads use

	now := time.Now()
	settle := func(model string, in, out int, cost, share float64, ts int64) {
		if cost > 0 {
			_, _ = db.AddCredits(wallet, cost)
		}
		_, _ = db.Settle(wallet, node, cost, share, protocol.UsageReceipt{
			RequestID: "r-" + model + "-" + time.Unix(ts, 0).Format("150405.000000000"),
			Model:     model, PromptTokens: in, CompletionTokens: out, TS: ts,
		})
	}
	settle("modelA", 10, 20, 0.50, 0.35, now.Unix())                 // paid
	settle("modelA", 5, 5, 0, 0, now.Unix())                         // free
	settle("modelB", 100, 200, 1.00, 0.70, now.Unix())               // paid
	settle("old", 1, 1, 9.0, 6.0, now.Add(-400*24*time.Hour).Unix()) // outside any sane window

	b := &broker{priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{}}
	b.bill.creditUSD = 1
	return b, wallet, priv
}

// TestMetricsProviderShape locks the provider JSON shape, the free-vs-paid split, the
// earnings total, and that BOTH auth paths (web session + signed Ed25519) are served.
func TestMetricsProviderShape(t *testing.T) {
	b, _, priv := newMetricsBroker(t)

	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"session", sessionReq(b, http.MethodGet, "/metrics/provider?days=30", "octocat", 7)},
		{"signed", signedMetricsGET("/metrics/provider?days=30", "/metrics/provider", priv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.metricsProvider(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("provider %s = %d, want 200 (body=%s)", tc.name, w.Code, w.Body.String())
			}
			var resp struct {
				Models     []store.ProviderModelMetric `json:"models"`
				Totals     map[string]any              `json:"totals"`
				PeriodDays int                         `json:"period_days"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.PeriodDays != 30 {
				t.Errorf("period_days = %d, want 30", resp.PeriodDays)
			}
			// modelA + modelB inside the window; "old" excluded.
			if len(resp.Models) != 2 {
				t.Fatalf("models = %d, want 2 (%+v)", len(resp.Models), resp.Models)
			}
			var a store.ProviderModelMetric
			for _, m := range resp.Models {
				if m.Model == "modelA" {
					a = m
				}
			}
			if a.Requests != 2 || a.PaidRequests != 1 || a.FreeRequests != 1 {
				t.Errorf("modelA split = {%d %d %d}, want {2 1 1}", a.Requests, a.PaidRequests, a.FreeRequests)
			}
			if a.NodeID != "node-1" {
				t.Errorf("modelA node_id = %q, want node-1", a.NodeID)
			}
			// Totals: earnings 0.35 + 0.70 = 1.05; the excluded "old" 6.0 must NOT count.
			if got := resp.Totals["earnings_usd"].(float64); got < 1.0499 || got > 1.0501 {
				t.Errorf("totals.earnings_usd = %v, want 1.05", got)
			}
			if got := resp.Totals["requests"].(float64); got != 3 {
				t.Errorf("totals.requests = %v, want 3", got)
			}
		})
	}
}

// TestMetricsUsageShape locks the usage JSON shape, the free-vs-paid split, the spend
// total, and both auth paths.
func TestMetricsUsageShape(t *testing.T) {
	b, _, priv := newMetricsBroker(t)

	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"session", sessionReq(b, http.MethodGet, "/metrics/usage?days=30", "octocat", 7)},
		{"signed", signedMetricsGET("/metrics/usage?days=30", "/metrics/usage", priv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.metricsUsage(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("usage %s = %d, want 200 (body=%s)", tc.name, w.Code, w.Body.String())
			}
			var resp struct {
				LoggedIn   bool                     `json:"logged_in"`
				Models     []store.UsageModelMetric `json:"models"`
				Totals     map[string]any           `json:"totals"`
				PeriodDays int                      `json:"period_days"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !resp.LoggedIn {
				t.Errorf("logged_in = false, want true")
			}
			if resp.PeriodDays != 30 {
				t.Errorf("period_days = %d, want 30", resp.PeriodDays)
			}
			if len(resp.Models) != 2 { // modelA + modelB in window
				t.Fatalf("models = %d, want 2 (%+v)", len(resp.Models), resp.Models)
			}
			// spend total: 0.50 + 1.00 = 1.50 (the free modelA serve adds 0; "old" excluded).
			if got := resp.Totals["spend_usd"].(float64); got < 1.4999 || got > 1.5001 {
				t.Errorf("totals.spend_usd = %v, want 1.50", got)
			}
			if got := resp.Totals["free_requests"].(float64); got != 1 {
				t.Errorf("totals.free_requests = %v, want 1", got)
			}
			if got := resp.Totals["paid_requests"].(float64); got != 2 {
				t.Errorf("totals.paid_requests = %v, want 2", got)
			}
		})
	}
}

// TestMetricsAuth covers the auth contract: an anonymous (unsigned, no cookie) request
// is 401 on both endpoints, and a signed-but-UNBOUND keypair (no account, no wallet) is
// 401 on both - own-data-only, mirroring the payout endpoints.
func TestMetricsAuth(t *testing.T) {
	b, _, _ := newMetricsBroker(t)

	// Anonymous -> 401 on both.
	for _, p := range []string{"/metrics/provider", "/metrics/usage"} {
		w := httptest.NewRecorder()
		if p == "/metrics/provider" {
			b.metricsProvider(w, httptest.NewRequest(http.MethodGet, p, nil))
		} else {
			b.metricsUsage(w, httptest.NewRequest(http.MethodGet, p, nil))
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("anon %s = %d, want 401", p, w.Code)
		}
	}

	// Signed-but-unbound keypair: 401 on both (no operator account, no github wallet).
	_, unbound, _ := ed25519.GenerateKey(nil)
	wp := httptest.NewRecorder()
	b.metricsProvider(wp, signedReq(http.MethodGet, "/metrics/provider", nil, unbound))
	if wp.Code != http.StatusUnauthorized {
		t.Errorf("unbound provider = %d, want 401", wp.Code)
	}
	wu := httptest.NewRecorder()
	b.metricsUsage(wu, signedReq(http.MethodGet, "/metrics/usage", nil, unbound))
	if wu.Code != http.StatusUnauthorized {
		t.Errorf("unbound usage = %d, want 401", wu.Code)
	}
}

// TestMetricsDaysClamp covers the window param echoed back as period_days: a default
// (absent / zero / garbage all fall back to metricsDefaultDays), a custom value passed
// through, and an over-cap value clamped to metricsMaxDays.
func TestMetricsDaysClamp(t *testing.T) {
	b, _, priv := newMetricsBroker(t)
	cases := []struct {
		q    string
		want int
	}{
		{"", metricsDefaultDays},
		{"?days=7", 7},
		{"?days=99999", metricsMaxDays},
		{"?days=0", metricsDefaultDays},
		{"?days=abc", metricsDefaultDays},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		b.metricsUsage(w, signedMetricsGET("/metrics/usage"+c.q, "/metrics/usage", priv))
		if w.Code != http.StatusOK {
			t.Fatalf("days=%q = %d, want 200", c.q, w.Code)
		}
		var resp struct {
			PeriodDays int `json:"period_days"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.PeriodDays != c.want {
			t.Errorf("days=%q -> period_days %d, want %d", c.q, resp.PeriodDays, c.want)
		}
	}
}
