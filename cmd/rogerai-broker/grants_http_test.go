package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestGrantsHTTPCRUD exercises the owner-auth /grants endpoints end to end: an
// owner creates a grant (secret shown once), lists it, and revokes it; an
// un-owned (unsigned, unbound) caller is forbidden.
func TestGrantsHTTPCRUD(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, grantRL: loadRateLimiter(), rl: loadRateLimiter()}
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_ = mem.BindOwner(store.Owner{GitHubID: 1, Login: "owner", Pubkey: pubHex})

	// Create (signed owner).
	createBody, _ := json.Marshal(map[string]any{"name": "petlings"})
	cr := httptest.NewRequest(http.MethodPost, "/grants", bytes.NewReader(createBody))
	signReq(cr, priv, createBody)
	cw := httptest.NewRecorder()
	b.grants(cw, cr)
	if cw.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200: %s", cw.Code, cw.Body.String())
	}
	var created struct {
		Grant  map[string]any `json:"grant"`
		Secret string         `json:"secret"`
	}
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	if created.Secret == "" || created.Grant["name"] != "petlings" {
		t.Fatalf("create response missing secret/name: %s", cw.Body.String())
	}
	if created.Grant["free"] != true {
		t.Fatalf("a grant with no price should default free: %v", created.Grant["free"])
	}

	// List (signed owner) returns the one grant.
	lr := httptest.NewRequest(http.MethodGet, "/grants", nil)
	signReq(lr, priv, nil)
	lw := httptest.NewRecorder()
	b.grants(lw, lr)
	var listed struct {
		Grants []map[string]any `json:"grants"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &listed)
	if len(listed.Grants) != 1 {
		t.Fatalf("list = %d grants, want 1", len(listed.Grants))
	}
	id, _ := listed.Grants[0]["id"].(string)

	// An unsigned (un-owned) caller cannot create.
	uw := httptest.NewRecorder()
	b.grants(uw, httptest.NewRequest(http.MethodPost, "/grants", bytes.NewReader(createBody)))
	if uw.Code != http.StatusForbidden {
		t.Fatalf("unsigned create = %d, want 403", uw.Code)
	}

	// Revoke (DELETE) by id.
	dr := httptest.NewRequest(http.MethodDelete, "/grants/"+id, nil)
	signReq(dr, priv, nil)
	dw := httptest.NewRecorder()
	b.grants(dw, dr)
	if dw.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200: %s", dw.Code, dw.Body.String())
	}
	gs, _ := mem.GrantsByOwner(pubHex)
	if len(gs) != 1 || !gs[0].Revoked {
		t.Fatalf("grant should be revoked after DELETE")
	}
}
