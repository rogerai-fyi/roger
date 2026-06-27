package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sessionPost builds an owner-session POST/PATCH with a JSON body.
func sessionPost(b *broker, method, path, login string, gid int64, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession(login, gid, time.Now().Add(time.Hour).Unix())})
	return r
}

// TestProviderModels covers the owner pricing manager: method guard (405), auth (401),
// the GET list, an owned-node price set (200 + persisted override), a not-owned node
// (403), and a missing-field body (400).
func TestProviderModels(t *testing.T) {
	b, o := brokerWithOwner(t) // owner octocat/7, pubkey o.Pubkey
	_ = b.db.BindNode("n1", o.Pubkey)

	// Method guard -> 405.
	w405 := httptest.NewRecorder()
	b.providerModels(w405, httptest.NewRequest(http.MethodDelete, "/provider/models", nil))
	if w405.Code != http.StatusMethodNotAllowed {
		t.Fatalf("providerModels DELETE = %d, want 405", w405.Code)
	}
	// No auth -> 401.
	w401 := httptest.NewRecorder()
	b.providerModels(w401, httptest.NewRequest(http.MethodGet, "/provider/models", nil))
	if w401.Code != http.StatusUnauthorized {
		t.Fatalf("providerModels GET anon = %d, want 401", w401.Code)
	}
	// Owner GET -> 200 list.
	wg := httptest.NewRecorder()
	b.providerModels(wg, sessionReq(b, http.MethodGet, "/provider/models", "octocat", 7))
	if wg.Code != http.StatusOK {
		t.Fatalf("providerModels GET = %d, want 200: %s", wg.Code, wg.Body.String())
	}
	// Owner sets a price on an OWNED node -> 200 + override persisted.
	wok := httptest.NewRecorder()
	b.providerModels(wok, sessionPost(b, http.MethodPost, "/provider/models", "octocat", 7,
		`{"node":"n1","model":"m1","price_in":0.1,"price_out":0.2}`))
	if wok.Code != http.StatusOK {
		t.Fatalf("providerModels POST = %d, want 200: %s", wok.Code, wok.Body.String())
	}
	if _, ok, _ := b.db.OfferOverride("n1", "m1"); !ok {
		t.Error("the price override should be persisted")
	}
	// A node the owner does NOT own -> 403.
	wno := httptest.NewRecorder()
	b.providerModels(wno, sessionPost(b, http.MethodPost, "/provider/models", "octocat", 7,
		`{"node":"someone-elses","model":"m1","price_out":0.2}`))
	if wno.Code != http.StatusForbidden {
		t.Errorf("providerModels POST not-owned = %d, want 403", wno.Code)
	}
	// Missing node/model -> 400.
	wbad := httptest.NewRecorder()
	b.providerModels(wbad, sessionPost(b, http.MethodPost, "/provider/models", "octocat", 7, `{}`))
	if wbad.Code != http.StatusBadRequest {
		t.Errorf("providerModels POST no-node = %d, want 400", wbad.Code)
	}
}
