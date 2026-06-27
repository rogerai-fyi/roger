package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestValidateOfferInput covers the owner price/schedule validator across every reject
// branch + the valid pass.
func TestValidateOfferInput(t *testing.T) {
	if validateOfferInput(-1, 0, nil) == "" {
		t.Error("negative price-in should be rejected")
	}
	if validateOfferInput(0, -1, nil) == "" {
		t.Error("negative price-out should be rejected")
	}
	bad := []protocol.PriceWindow{{Start: "25:00", End: "06:00"}}
	if validateOfferInput(0, 0, bad) == "" {
		t.Error("bad HH:MM should be rejected")
	}
	badDay := []protocol.PriceWindow{{Start: "01:00", End: "02:00", Days: []int{9}}}
	if validateOfferInput(0, 0, badDay) == "" {
		t.Error("bad weekday should be rejected")
	}
	negWin := []protocol.PriceWindow{{Start: "01:00", End: "02:00", In: -1}}
	if validateOfferInput(0, 0, negWin) == "" {
		t.Error("negative window price should be rejected")
	}
	ok := []protocol.PriceWindow{{Start: "22:00", End: "06:00", In: 1, Out: 2, Days: []int{0, 6}}}
	if validateOfferInput(0.1, 0.2, ok) != "" {
		t.Error("a valid offer should pass")
	}
	// validHHMM directly.
	if !validHHMM("09:30") || validHHMM("9:99") || validHHMM("noon") {
		t.Error("validHHMM wrong")
	}
}

// TestAccountPatch covers the email PATCH: invalid email -> 400, a valid update on a
// bound owner -> 200, and an unknown login -> 404.
func TestAccountPatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	_ = b.db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pk7"})

	// Invalid email -> 400.
	wbad := httptest.NewRecorder()
	b.accountPatch(wbad, httptest.NewRequest(http.MethodPatch, "/account", strings.NewReader(`{"email":"notanemail"}`)), "octocat", 7, "u_gh_7")
	if wbad.Code != http.StatusBadRequest {
		t.Fatalf("accountPatch(bad email) = %d, want 400", wbad.Code)
	}

	// Valid update -> 200.
	wok := httptest.NewRecorder()
	b.accountPatch(wok, httptest.NewRequest(http.MethodPatch, "/account", strings.NewReader(`{"email":"new@x.com"}`)), "octocat", 7, "u_gh_7")
	if wok.Code != http.StatusOK {
		t.Fatalf("accountPatch(valid) = %d, want 200: %s", wok.Code, wok.Body.String())
	}
	if o, _, _ := b.db.OwnerByLogin("octocat"); o.Email != "new@x.com" {
		t.Errorf("email not persisted: %q", o.Email)
	}

	// Unknown login -> 404.
	w404 := httptest.NewRecorder()
	b.accountPatch(w404, httptest.NewRequest(http.MethodPatch, "/account", strings.NewReader(`{"email":"x@y.com"}`)), "ghost", 9, "u_gh_9")
	if w404.Code != http.StatusNotFound {
		t.Errorf("accountPatch(unknown) = %d, want 404", w404.Code)
	}
}

// TestAdminUnholdAuth covers adminUnhold's admin gate (403 without the key) and its
// method guard.
func TestAdminUnholdAuth(t *testing.T) {
	b := &broker{adminKey: "secret", db: store.NewMem()}
	// No admin key -> 403.
	w := httptest.NewRecorder()
	b.adminUnhold(w, httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden {
		t.Errorf("adminUnhold without key = %d, want 403", w.Code)
	}
	// Wrong method -> 405.
	wm := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin/unhold", nil)
	r.Header.Set("X-Roger-Admin", "secret")
	b.adminUnhold(wm, r)
	if wm.Code != http.StatusMethodNotAllowed {
		t.Errorf("adminUnhold GET = %d, want 405", wm.Code)
	}
}
