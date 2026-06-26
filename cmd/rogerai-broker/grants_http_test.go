package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	// An unsigned (unauthenticated) caller cannot create: no cookie and no valid
	// signature -> 401 from the dual-path resolver.
	uw := httptest.NewRecorder()
	b.grants(uw, httptest.NewRequest(http.MethodPost, "/grants", bytes.NewReader(createBody)))
	if uw.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned create = %d, want 401", uw.Code)
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

// TestGrantsWebSessionAuth locks the keys-page fix: the /grants endpoints now
// resolve the owner via the dual-path payoutOwner, so a logged-in BROWSER session
// cookie (the web keys page) can GET + POST /grants - which used to 403 because the
// handlers only honored a signed CLI request. Owner-scoping still holds (one owner
// cannot read/edit another's grant), an unauthenticated caller is rejected, and the
// signed CLI path keeps working.
func TestGrantsWebSessionAuth(t *testing.T) {
	mem := store.NewMem()
	_, bpriv, _ := ed25519.GenerateKey(nil)
	b := &broker{db: mem, priv: bpriv, grantRL: loadRateLimiter(), rl: loadRateLimiter(), pubOfUser: map[string]string{}}

	// Two GitHub-linked operators (each with its own signing pubkey).
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubAHex := hex.EncodeToString(pubA)
	pubB, _, _ := ed25519.GenerateKey(nil)
	pubBHex := hex.EncodeToString(pubB)
	_ = mem.BindOwner(store.Owner{GitHubID: 1, Login: "ownerA", Pubkey: pubAHex})
	_ = mem.BindOwner(store.Owner{GitHubID: 2, Login: "ownerB", Pubkey: pubBHex})

	// session mints a request carrying a valid web session cookie for login/gid.
	session := func(method, path, login string, gid int64, body []byte) *http.Request {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rdr)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession(login, gid, time.Now().Add(time.Hour).Unix())})
		return r
	}

	// (a) A web SESSION cookie can POST /grants (this used to 403).
	createBody, _ := json.Marshal(map[string]any{"name": "from-web"})
	cw := httptest.NewRecorder()
	b.grants(cw, session(http.MethodPost, "/grants", "ownerA", 1, createBody))
	if cw.Code != http.StatusOK {
		t.Fatalf("web-session create = %d, want 200: %s", cw.Code, cw.Body.String())
	}
	var created struct {
		Grant  map[string]any `json:"grant"`
		Secret string         `json:"secret"`
	}
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	if created.Secret == "" || created.Grant["name"] != "from-web" {
		t.Fatalf("web-session create missing secret/name: %s", cw.Body.String())
	}
	idA, _ := created.Grant["id"].(string)

	// (a) A web SESSION cookie can GET /grants and sees its own grant.
	lw := httptest.NewRecorder()
	b.grants(lw, session(http.MethodGet, "/grants", "ownerA", 1, nil))
	if lw.Code != http.StatusOK {
		t.Fatalf("web-session list = %d, want 200: %s", lw.Code, lw.Body.String())
	}
	var listed struct {
		Grants []map[string]any `json:"grants"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &listed)
	if len(listed.Grants) != 1 {
		t.Fatalf("web-session list = %d grants, want 1", len(listed.Grants))
	}

	// (b) Owner-scoping: owner B (web session) cannot read or edit owner A's grant.
	ow := httptest.NewRecorder()
	b.grants(ow, session(http.MethodGet, "/grants/"+idA, "ownerB", 2, nil))
	if ow.Code != http.StatusNotFound {
		t.Fatalf("cross-owner GET = %d, want 404", ow.Code)
	}
	patchBody, _ := json.Marshal(map[string]any{"daily_cap": 5})
	pw := httptest.NewRecorder()
	b.grants(pw, session(http.MethodPatch, "/grants/"+idA, "ownerB", 2, patchBody))
	if pw.Code != http.StatusNotFound {
		t.Fatalf("cross-owner PATCH = %d, want 404", pw.Code)
	}
	// Owner B's session also lists ZERO grants (it sees only its own).
	bw := httptest.NewRecorder()
	b.grants(bw, session(http.MethodGet, "/grants", "ownerB", 2, nil))
	var bListed struct {
		Grants []map[string]any `json:"grants"`
	}
	_ = json.Unmarshal(bw.Body.Bytes(), &bListed)
	if len(bListed.Grants) != 0 {
		t.Fatalf("owner B should see 0 grants, saw %d", len(bListed.Grants))
	}

	// (c) An unauthenticated request (no cookie, no signature) is rejected with 401.
	uw := httptest.NewRecorder()
	b.grants(uw, httptest.NewRequest(http.MethodGet, "/grants", nil))
	if uw.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", uw.Code)
	}

	// (d) The signed CLI path still works: owner A signs a GET and sees its grant.
	sr := httptest.NewRequest(http.MethodGet, "/grants", nil)
	signReq(sr, privA, nil)
	sw := httptest.NewRecorder()
	b.grants(sw, sr)
	if sw.Code != http.StatusOK {
		t.Fatalf("signed-CLI list = %d, want 200: %s", sw.Code, sw.Body.String())
	}
	var sListed struct {
		Grants []map[string]any `json:"grants"`
	}
	_ = json.Unmarshal(sw.Body.Bytes(), &sListed)
	if len(sListed.Grants) != 1 {
		t.Fatalf("signed-CLI list = %d grants, want 1", len(sListed.Grants))
	}
}
