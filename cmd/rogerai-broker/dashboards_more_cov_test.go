package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestBalanceHandler covers GET /balance across its three resolutions: a logged-in session
// sees its real balance (logged_in:true), an unsigned (anon) caller sees logged_in:false
// with no balance, and an offered-but-invalid signature is 401.
func TestBalanceHandler(t *testing.T) {
	b, _ := brokerWithOwner(t)
	_, _ = b.db.AddCredits("u_gh_7", 17)

	// Logged-in session -> real balance.
	w := httptest.NewRecorder()
	b.balance(w, sessionReq(b, http.MethodGet, "/balance", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("balance = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Balance  float64 `json:"balance"`
		LoggedIn bool    `json:"logged_in"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.LoggedIn || resp.Balance != 17 {
		t.Errorf("balance resp = %+v, want logged_in:true balance:17", resp)
	}

	// Unsigned (anon) -> logged_in:false, no balance, still 200.
	wa := httptest.NewRecorder()
	b.balance(wa, httptest.NewRequest(http.MethodGet, "/balance", nil))
	if wa.Code != http.StatusOK {
		t.Fatalf("anon balance = %d, want 200", wa.Code)
	}
	var anon struct {
		LoggedIn bool `json:"logged_in"`
	}
	_ = json.Unmarshal(wa.Body.Bytes(), &anon)
	if anon.LoggedIn {
		t.Error("an unsigned balance read must be logged_in:false")
	}

	// Offered-but-invalid signature -> 401.
	_, userPriv, _ := ed25519.GenerateKey(nil)
	rbad := httptest.NewRequest(http.MethodGet, "/balance", nil)
	signReq(rbad, userPriv, nil)
	rbad.Header.Set(protocol.HeaderSig, "deadbeef")
	wbad := httptest.NewRecorder()
	b.balance(wbad, rbad)
	if wbad.Code != http.StatusUnauthorized {
		t.Fatalf("bad-sig balance = %d, want 401", wbad.Code)
	}
}

// TestAccountLimitGetSetRead covers GET/PATCH /account/limit: a method guard (405), the
// anon login gate (401), a PATCH missing the field (400), a valid PATCH that persists the
// cap, and the GET that reads it back.
func TestAccountLimitGetSetRead(t *testing.T) {
	b, _ := brokerWithOwner(t)

	// Method guard: POST is 405.
	wm := httptest.NewRecorder()
	b.accountLimit(wm, sessionReq(b, http.MethodPost, "/account/limit", "octocat", 7))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /account/limit = %d, want 405", wm.Code)
	}

	// Anon (unsigned) -> not-logged-in 401.
	wa := httptest.NewRecorder()
	b.accountLimit(wa, httptest.NewRequest(http.MethodGet, "/account/limit", nil))
	if wa.Code != http.StatusUnauthorized {
		t.Fatalf("anon /account/limit = %d, want 401", wa.Code)
	}

	// PATCH with no monthly_cap field -> 400.
	wmiss := httptest.NewRecorder()
	b.accountLimit(wmiss, sessionPost(b, http.MethodPatch, "/account/limit", "octocat", 7, `{}`))
	if wmiss.Code != http.StatusBadRequest {
		t.Fatalf("PATCH no-field = %d, want 400 (%s)", wmiss.Code, wmiss.Body.String())
	}

	// Valid PATCH sets the cap.
	wset := httptest.NewRecorder()
	b.accountLimit(wset, sessionPost(b, http.MethodPatch, "/account/limit", "octocat", 7, `{"monthly_cap":42.5}`))
	if wset.Code != http.StatusOK {
		t.Fatalf("PATCH set = %d, want 200 (%s)", wset.Code, wset.Body.String())
	}

	// GET reads the persisted cap back.
	wget := httptest.NewRecorder()
	b.accountLimit(wget, sessionReq(b, http.MethodGet, "/account/limit", "octocat", 7))
	if wget.Code != http.StatusOK {
		t.Fatalf("GET /account/limit = %d, want 200", wget.Code)
	}
	var got struct {
		MonthlyCap float64 `json:"monthly_cap"`
	}
	_ = json.Unmarshal(wget.Body.Bytes(), &got)
	if got.MonthlyCap != 42.5 {
		t.Errorf("monthly_cap = %v, want 42.5", got.MonthlyCap)
	}
}
