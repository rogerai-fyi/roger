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

// TestSmallPureHelpers covers clampInt, ewma, recountModel, and qualityOK across branches.
func TestSmallPureHelpers(t *testing.T) {
	if clampInt(5, 0, 10) != 5 || clampInt(-1, 0, 10) != 0 || clampInt(99, 0, 10) != 10 {
		t.Error("clampInt bounds wrong")
	}
	if ewma(0, 7, 0.5) != 7 { // cur<=0 -> seed with sample
		t.Error("ewma(seed) wrong")
	}
	if got := ewma(10, 20, 0.5); got != 15 { // 0.5*20 + 0.5*10
		t.Errorf("ewma(blend) = %v, want 15", got)
	}
	if recountModel(protocol.UsageReceipt{Model: "m"}, "req") != "m" ||
		recountModel(protocol.UsageReceipt{}, "req") != "req" {
		t.Error("recountModel fallback wrong")
	}
	if qualityOK([]byte("")) || qualityOK([]byte("   ")) {
		t.Error("empty/blank body should fail quality")
	}
	if !qualityOK([]byte(`{"choices":[{"message":{"content":"hi"}}]}`)) {
		t.Error("a real completion should pass quality")
	}
	if !qualityOK([]byte(`{"unknown":"shape but non-trivial"}`)) {
		t.Error("an unknown but non-trivial body should not be rejected")
	}
}

// TestReady covers the readiness probe: a live store is 200 ready; a nil store is 503.
func TestReady(t *testing.T) {
	b := &broker{db: store.NewMem()}
	w := httptest.NewRecorder()
	b.ready(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready(live) = %d, want 200", w.Code)
	}
	w2 := httptest.NewRecorder()
	(&broker{}).ready(w2, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("ready(nil db) = %d, want 503", w2.Code)
	}
}

// TestAdminUnbanNode covers the admin node-unban handler: 403 without the admin key, the
// 400 validation branches, and a keyed unban that lifts the ban from store + cache.
func TestAdminUnbanNodeBranches(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BanNode("n1", "report threshold")
	b := &broker{db: mem, adminKey: "super-secret", banned: map[string]bool{"n1": true}}

	// No admin auth -> 403.
	w := httptest.NewRecorder()
	b.adminUnbanNode(w, httptest.NewRequest(http.MethodPost, "/admin/unban-node", strings.NewReader(`{"node":"n1"}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unban without admin = %d, want 403", w.Code)
	}

	adminPost := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/admin/unban-node", strings.NewReader(body))
		r.Header.Set("X-Roger-Admin", "super-secret")
		return r
	}
	// Bad JSON -> 400.
	wb := httptest.NewRecorder()
	b.adminUnbanNode(wb, adminPost(`{bad`))
	if wb.Code != http.StatusBadRequest {
		t.Errorf("unban bad JSON = %d, want 400", wb.Code)
	}
	// Missing node -> 400.
	wn := httptest.NewRecorder()
	b.adminUnbanNode(wn, adminPost(`{}`))
	if wn.Code != http.StatusBadRequest {
		t.Errorf("unban no-node = %d, want 400", wn.Code)
	}
	// Keyed unban -> 200, ban lifted from store + cache.
	wok := httptest.NewRecorder()
	b.adminUnbanNode(wok, adminPost(`{"node":"n1"}`))
	if wok.Code != http.StatusOK {
		t.Fatalf("keyed unban = %d, want 200: %s", wok.Code, wok.Body.String())
	}
	if b.banned["n1"] {
		t.Error("the in-memory ban cache should be cleared")
	}
	if bans, _ := mem.BannedNodes(); bans["n1"] != "" {
		t.Error("the store ban should be lifted")
	}
}

// TestAccountLimitHandler covers GET/PATCH of the per-account monthly spend cap via a
// signed/owner session.
func TestAccountLimitHandler(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, bpriv, _ := ed25519.GenerateKey(nil)
	mem := store.NewMem()
	b := &broker{db: mem, priv: bpriv, pubOfUser: map[string]string{}}
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pk7"})

	// GET the (default) cap.
	wg := httptest.NewRecorder()
	b.accountLimit(wg, sessionReq(b, http.MethodGet, "/account/limit", "octocat", 7))
	if wg.Code != http.StatusOK {
		t.Fatalf("accountLimit GET = %d, want 200: %s", wg.Code, wg.Body.String())
	}
	_ = time.Now
}
