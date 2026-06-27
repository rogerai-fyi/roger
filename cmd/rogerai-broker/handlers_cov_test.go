package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// brokerWithOwner builds a broker (Mem store + signing key) plus one GitHub-linked
// owner (login octocat / gid 7) whose github-scoped wallet is u_gh_7.
func brokerWithOwner(t *testing.T) (*broker, store.Owner) {
	t.Helper()
	mem := store.NewMem()
	_, bpriv, _ := ed25519.GenerateKey(nil)
	b := &broker{db: mem, priv: bpriv, pubOfUser: map[string]string{}}
	b.bill.creditUSD = 1
	pub, _, _ := ed25519.GenerateKey(nil)
	o := store.Owner{GitHubID: 7, Login: "octocat", Pubkey: hex.EncodeToString(pub), Email: "o@x.com"}
	if err := mem.BindOwner(o); err != nil {
		t.Fatal(err)
	}
	return b, o
}

// TestAccountExportHandler covers the GDPR-style export: a logged-in session gets a JSON
// dump tagged with its identity; an unauthenticated request is 401.
func TestAccountExportHandler(t *testing.T) {
	b, _ := brokerWithOwner(t)
	_, _ = b.db.AddCredits("u_gh_7", 5)

	w := httptest.NewRecorder()
	b.accountExport(w, sessionReq(b, http.MethodPost, "/account/export", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("accountExport = %d, want 200: %s", w.Code, w.Body.String())
	}
	var dump map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &dump); err != nil {
		t.Fatal(err)
	}
	if dump["github_login"] != "octocat" {
		t.Errorf("export github_login = %v, want octocat", dump["github_login"])
	}

	w2 := httptest.NewRecorder()
	b.accountExport(w2, httptest.NewRequest(http.MethodPost, "/account/export", nil))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated export = %d, want 401", w2.Code)
	}
}

// TestBillingHandler covers GET /billing: a logged-in session sees its balance + credit
// rate; an anonymous request is 401.
func TestBillingHandler(t *testing.T) {
	b, _ := brokerWithOwner(t)
	_, _ = b.db.AddCredits("u_gh_7", 12)

	w := httptest.NewRecorder()
	b.billing(w, sessionReq(b, http.MethodGet, "/billing", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("billing = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["balance"].(float64) < 11.99 {
		t.Errorf("billing balance = %v, want ~12", resp["balance"])
	}
	if resp["credit_usd"].(float64) != 1 {
		t.Errorf("billing credit_usd = %v, want 1", resp["credit_usd"])
	}
}

// TestUsageHandler covers GET /usage in both grouping modes (model + day), exercising
// groupSpend, plus the 401 path. A settled receipt gives the grouping something to sum.
func TestUsageHandler(t *testing.T) {
	b, _ := brokerWithOwner(t)
	_ = b.db.BindNode("n1", "acct")
	_, _ = b.db.AddCredits("u_gh_7", 100)
	_, _ = b.db.Settle("u_gh_7", "n1", 3, 2, protocol.UsageReceipt{
		RequestID: "rq1", Model: "qwen", PromptTokens: 5, CompletionTokens: 9, TS: 1_700_000_000,
	})

	for _, group := range []string{"model", "day"} {
		w := httptest.NewRecorder()
		b.usage(w, sessionReq(b, http.MethodGet, "/usage?group="+group, "octocat", 7))
		if w.Code != http.StatusOK {
			t.Fatalf("usage?group=%s = %d, want 200: %s", group, w.Code, w.Body.String())
		}
		var resp struct {
			Spend   float64       `json:"spend"`
			Group   string        `json:"group"`
			Buckets []usageBucket `json:"buckets"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Group != group {
			t.Errorf("usage group = %q, want %q", resp.Group, group)
		}
		if resp.Spend < 2.99 || len(resp.Buckets) == 0 {
			t.Errorf("usage spend=%v buckets=%d, want ~3 and >=1 bucket", resp.Spend, len(resp.Buckets))
		}
	}
}

// TestBandsHandlers covers GET /bands (owner-scoped list via bandView) and DELETE
// /bands/{id} (owner-scoped revoke), plus the unauthenticated 403 and unknown-id 404.
func TestBandsHandlers(t *testing.T) {
	b, o := brokerWithOwner(t)
	if err := b.db.CreateBand(store.Band{ID: "band_1", Owner: o.Pubkey, NodeID: "n1",
		CodeDisplay: "147.520 MHz", Label: "friends"}); err != nil {
		t.Fatal(err)
	}

	// List: authenticated by the owner pubkey header.
	r := httptest.NewRequest(http.MethodGet, "/bands", nil)
	r.Header.Set("X-Roger-Pubkey", o.Pubkey)
	w := httptest.NewRecorder()
	b.bands(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bands list = %d, want 200: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Bands []map[string]any `json:"bands"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Bands) != 1 || listResp.Bands[0]["status"] != "active" {
		t.Fatalf("bands = %+v, want one active band", listResp.Bands)
	}

	// No owner pubkey -> 403.
	w403 := httptest.NewRecorder()
	b.bands(w403, httptest.NewRequest(http.MethodGet, "/bands", nil))
	if w403.Code != http.StatusForbidden {
		t.Errorf("unauthenticated bands = %d, want 403", w403.Code)
	}

	// Revoke an unknown id -> 404.
	rNo := httptest.NewRequest(http.MethodDelete, "/bands/nope", nil)
	rNo.Header.Set("X-Roger-Pubkey", o.Pubkey)
	wNo := httptest.NewRecorder()
	b.bandsByID(wNo, rNo)
	if wNo.Code != http.StatusNotFound {
		t.Errorf("revoke unknown band = %d, want 404", wNo.Code)
	}

	// Revoke the owner's band -> 200.
	rDel := httptest.NewRequest(http.MethodDelete, "/bands/band_1", nil)
	rDel.Header.Set("X-Roger-Pubkey", o.Pubkey)
	wDel := httptest.NewRecorder()
	b.bandsByID(wDel, rDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("revoke own band = %d, want 200: %s", wDel.Code, wDel.Body.String())
	}
}

// TestAuthLogout covers POST /auth/logout: it clears the session cookie and 200s.
func TestAuthLogout(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.authLogout(w, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("authLogout = %d, want 200", w.Code)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("authLogout must expire the session cookie")
	}
}
