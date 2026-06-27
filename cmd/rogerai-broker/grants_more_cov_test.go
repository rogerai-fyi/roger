package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// createGrantViaSession POSTs a grant as the logged-in octocat/7 owner and returns its id.
func createGrantViaSession(t *testing.T, b *broker, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	b.grants(w, sessionPost(b, http.MethodPost, "/grants", "octocat", 7, `{"name":"`+name+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("create grant = %d, want 200: %s", w.Code, w.Body.String())
	}
	var created struct {
		Grant map[string]any `json:"grant"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id, _ := created.Grant["id"].(string)
	if id == "" {
		t.Fatal("created grant has no id")
	}
	return id
}

// TestGrantCreateNameRequired locks the create validation: a body with no name is 400.
func TestGrantCreateNameRequired(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionPost(b, http.MethodPost, "/grants", "octocat", 7, `{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no-name create = %d, want 400 (%s)", w.Code, w.Body.String())
	}
}

// TestGrantsForbiddenNoOperator locks the GitHub-link gate: a valid session whose login is
// NOT a bound operator (GitHubID 0) cannot manage grants -> 403.
func TestGrantsForbiddenNoOperator(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodGet, "/grants", "stranger", 99))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlinked grants = %d, want 403 (%s)", w.Code, w.Body.String())
	}
}

// TestGrantsMethodNotAllowed locks the collection method guard: a PUT is 405.
func TestGrantsMethodNotAllowed(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodPut, "/grants", "octocat", 7))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /grants = %d, want 405", w.Code)
	}
}

// TestGrantByIDNotFound locks the show miss: GET /grants/{unknown} is 404.
func TestGrantByIDNotFound(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodGet, "/grants/grant_nope", "octocat", 7))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET unknown grant = %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

// TestGrantByIDPatchUpdates locks the PATCH path: a valid patch updates the grant and
// returns the new view; a malformed patch body is 400; an unknown id is 404.
func TestGrantByIDPatchUpdates(t *testing.T) {
	b, _ := brokerWithOwner(t)
	id := createGrantViaSession(t, b, "patchable")

	// Bad JSON patch -> 400.
	w := httptest.NewRecorder()
	b.grants(w, sessionPost(b, http.MethodPatch, "/grants/"+id, "octocat", 7, "{bad"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad patch = %d, want 400", w.Code)
	}

	// Valid patch (rename) -> 200 with the updated name.
	revoked := false
	patch := store.GrantPatch{Revoked: &revoked}
	pb, _ := json.Marshal(patch)
	w = httptest.NewRecorder()
	b.grants(w, sessionPost(b, http.MethodPatch, "/grants/"+id, "octocat", 7, string(pb)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// Patch an unknown id -> 404.
	w = httptest.NewRecorder()
	b.grants(w, sessionPost(b, http.MethodPatch, "/grants/grant_nope", "octocat", 7, string(pb)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("patch unknown = %d, want 404", w.Code)
	}
}

// TestGrantByIDDeleteNotFound locks the revoke miss: DELETE an unknown id is 404.
func TestGrantByIDDeleteNotFound(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodDelete, "/grants/grant_nope", "octocat", 7))
	if w.Code != http.StatusNotFound {
		t.Fatalf("DELETE unknown = %d, want 404", w.Code)
	}
}

// TestGrantByIDMethodNotAllowed locks the item method guard: a PUT on /grants/{id} is 405.
func TestGrantByIDMethodNotAllowed(t *testing.T) {
	b, _ := brokerWithOwner(t)
	id := createGrantViaSession(t, b, "methodguard")
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodPut, "/grants/"+id, "octocat", 7))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /grants/{id} = %d, want 405", w.Code)
	}
}

// TestGrantByIDForbiddenNoOperator locks the per-item GitHub-link gate: a valid session
// whose login is NOT a bound operator cannot GET/DELETE a single grant -> 403.
func TestGrantByIDForbiddenNoOperator(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.grants(w, sessionReq(b, http.MethodGet, "/grants/grant_x", "stranger", 99))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlinked grantByID = %d, want 403 (%s)", w.Code, w.Body.String())
	}
}
