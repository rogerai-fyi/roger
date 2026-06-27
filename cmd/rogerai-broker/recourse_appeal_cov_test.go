package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestOwnerAppealMethodNotAllowed locks the appeal handler's method guard: a PUT is
// rejected with 405 + an Allow header before any auth/body work.
func TestOwnerAppealMethodNotAllowed(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	w := httptest.NewRecorder()
	b.ownerAppeal(w, httptest.NewRequest(http.MethodPut, "/owner/appeal", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /owner/appeal = %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != "GET, POST" {
		t.Errorf("Allow header = %q, want 'GET, POST'", got)
	}
}

// TestOwnerAppealUnauthenticated locks the auth gate: an anonymous request is 401.
func TestOwnerAppealUnauthenticated(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	w := httptest.NewRecorder()
	b.ownerAppeal(w, httptest.NewRequest(http.MethodGet, "/owner/appeal", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon /owner/appeal = %d, want 401", w.Code)
	}
}

// TestOwnerAppealNoOperatorAccount locks the "logged-in but no operator wallet" branch: a
// valid session whose login is bound with an EMPTY pubkey gets 403, not 200.
func TestOwnerAppealNoOperatorAccount(t *testing.T) {
	b, db, _ := newRecourseBroker(t)
	_ = db.BindOwner(store.Owner{GitHubID: 8, Login: "nopub", Pubkey: ""})
	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionReq(b, http.MethodGet, "/owner/appeal", "nopub", 8))
	if w.Code != http.StatusForbidden {
		t.Fatalf("no-pubkey owner /owner/appeal = %d, want 403 (%s)", w.Code, w.Body.String())
	}
}

// TestOwnerAppealGetHistory locks the GET status surface: it returns the caller's own
// appeals (scoped to the authenticated pubkey).
func TestOwnerAppealGetHistory(t *testing.T) {
	b, db, priv := newRecourseBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if _, err := db.AddAppeal(store.Appeal{AccountID: pubHex, NodeID: "n", Reason: "first"}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionReq(b, http.MethodGet, "/owner/appeal", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /owner/appeal = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Appeals []store.Appeal `json:"appeals"`
		Count   int            `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Appeals) != 1 || resp.Appeals[0].Reason != "first" {
		t.Errorf("GET appeals = %+v, want 1 appeal 'first'", resp)
	}
}

// TestOwnerAppealInvalidJSON locks the POST body parse guard: a malformed JSON body is 400.
func TestOwnerAppealInvalidJSON(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, "{not-json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad-JSON appeal = %d, want 400 (%s)", w.Code, w.Body.String())
	}
}

// TestOwnerAppealNodeNotOwned locks the owner-scoping gate: filing an appeal that names a
// node NOT bound to the caller is forbidden (403), structurally barring cross-account
// filing.
func TestOwnerAppealNodeNotOwned(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, `{"node_id":"someone-elses","reason":"unfair"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("appeal for unowned node = %d, want 403 (%s)", w.Code, w.Body.String())
	}
}

// TestOwnerAppealAutoExonerate locks the clear-false-positive path: filing an appeal for
// the caller's OWN node that holds a report-origin ban, with auto-eject disabled
// (reportEjectAt<=0, so no live corroboration threshold exists), lifts the ban
// immediately and reports auto_exonerated, restoring routing pending review.
func TestOwnerAppealAutoExonerate(t *testing.T) {
	b, db, priv := newRecourseBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	b.banned = map[string]bool{}
	b.reportEjectAt = 0 // auto-eject disabled -> any leftover report ban is unsustainable
	// "n" is already bound to the operator in newRecourseBroker; ban it as report-origin.
	b.banNode("n", "report bad-output")
	if !b.isBanned("n") {
		t.Fatal("precondition: node n must be banned")
	}

	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, `{"node_id":"n","reason":"false positive"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("self-node appeal = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["auto_exonerated"] != true || out["node_unbanned"] != "n" {
		t.Errorf("appeal out = %+v, want auto_exonerated + node_unbanned:n", out)
	}
	if b.isBanned("n") {
		t.Error("auto-exoneration must lift the in-memory ban")
	}
	// The appeal row is persisted for the caller.
	appeals, _ := db.AppealsByOwner(pubHex, 10)
	if len(appeals) != 1 {
		t.Errorf("persisted appeals = %d, want 1", len(appeals))
	}
}

// TestOwnerAppealCorroboratedNotExonerated locks the still-corroborated branch: with
// auto-eject ENABLED (reportEjectAt>0) and the distinct-reporter count AT/above the
// threshold, the report-origin ban is NOT auto-lifted (a human must review).
func TestOwnerAppealCorroboratedNotExonerated(t *testing.T) {
	b, db, _ := newRecourseBroker(t)
	b.banned = map[string]bool{}
	b.reportEjectAt = 2
	b.reportDecayDays = 0 // since=0 -> count all reporters
	b.banNode("n", "report still-corroborated")
	// Two distinct reporter IPs keep the corroboration at/above the threshold.
	_, _ = db.AddReport(store.Report{NodeID: "n", IP: "1.1.1.1", Detail: "x"})
	_, _ = db.AddReport(store.Report{NodeID: "n", IP: "2.2.2.2", Detail: "x"})

	w := httptest.NewRecorder()
	b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, `{"node_id":"n","reason":"please review"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("appeal = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if _, ok := out["auto_exonerated"]; ok {
		t.Errorf("still-corroborated ban must NOT auto-exonerate, out=%+v", out)
	}
	if !b.isBanned("n") {
		t.Error("a corroborated report ban must remain in place")
	}
}

// TestAdminUnbanNodeLiftsBan locks the admin node-unban escape hatch: an admin-keyed POST
// clears a node ban (durable row + in-memory set); a bad key is 403 and an empty node 400.
func TestAdminUnbanNodeLiftsBan(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	b.banned = map[string]bool{}
	b.banNode("n", "report x")

	// Bad key -> 403.
	r := httptest.NewRequest(http.MethodPost, "/admin/unban-node", strings.NewReader(`{"node":"n"}`))
	r.Header.Set("X-Roger-Admin", "wrong")
	w := httptest.NewRecorder()
	b.adminUnbanNode(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unban bad key = %d, want 403", w.Code)
	}

	// Empty node -> 400.
	r = httptest.NewRequest(http.MethodPost, "/admin/unban-node", strings.NewReader(`{"node":"  "}`))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w = httptest.NewRecorder()
	b.adminUnbanNode(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unban empty node = %d, want 400", w.Code)
	}

	// Correct key -> lifts the ban.
	r = httptest.NewRequest(http.MethodPost, "/admin/unban-node", strings.NewReader(`{"node":"n"}`))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w = httptest.NewRecorder()
	b.adminUnbanNode(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unban = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if b.isBanned("n") {
		t.Error("admin unban must clear the in-memory ban")
	}
}

// TestAdminUnholdNodeOnly locks the node-only unhold branch (no account_id): it clears the
// node recount hold and reports node_unheld without touching any account.
func TestAdminUnholdNodeOnly(t *testing.T) {
	b, db, _ := newRecourseBroker(t)
	_ = db.SetNodeRecountHold("n", true)
	r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{"node":"n"}`))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("node-only unhold = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["node_unheld"] != "n" {
		t.Errorf("out = %+v, want node_unheld:n", out)
	}
}

// TestAdminUnholdMissingTarget locks the validation branch: neither account_id nor node
// supplied is a 400.
func TestAdminUnholdMissingTarget(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{}`))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w := httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty unhold = %d, want 400", w.Code)
	}
}

// TestAdminAppealsQueue locks the admin review queue: an admin-keyed GET returns the OPEN
// appeals; a missing key is 403.
func TestAdminAppealsQueue(t *testing.T) {
	b, db, _ := newRecourseBroker(t)
	_, _ = db.AddAppeal(store.Appeal{AccountID: "pk1", NodeID: "n", Reason: "open one"})

	// No key -> 403.
	w := httptest.NewRecorder()
	b.adminAppeals(w, httptest.NewRequest(http.MethodGet, "/admin/appeals", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("no-key /admin/appeals = %d, want 403", w.Code)
	}

	// Admin key -> the open queue.
	r := httptest.NewRequest(http.MethodGet, "/admin/appeals", nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w = httptest.NewRecorder()
	b.adminAppeals(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/admin/appeals = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Appeals []store.Appeal `json:"appeals"`
		Count   int            `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 || len(resp.Appeals) != 1 {
		t.Errorf("admin appeals = %d, want 1 open", resp.Count)
	}
}
