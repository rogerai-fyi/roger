package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The band management client: RevokeBand burns a code, MoveBand repoints a band at a
// different model WITHOUT rotating it. Spec: features/sharing/band_management.feature.

func TestRevokeBandSendsAnOwnerSignedDelete(t *testing.T) {
	var gotMethod, gotPath, gotPubkey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotPubkey = r.Method, r.URL.Path, r.Header.Get("X-Roger-Pubkey")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
	}))
	defer srv.Close()

	if err := RevokeBand(srv.URL, "band_x"); err != nil {
		t.Fatalf("RevokeBand: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/bands/band_x" {
		t.Errorf("sent %s %s, want DELETE /bands/band_x", gotMethod, gotPath)
	}
	// The band endpoints authenticate on the pubkey header; an unsigned call is a 403.
	if gotPubkey == "" {
		t.Error("the request must carry the owner's pubkey")
	}
}

func TestMoveBandSendsThePatchAndTargetNode(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"ok":true,"moved":true,"node_id":"station-model-b"}`))
	}))
	defer srv.Close()

	if err := MoveBand(srv.URL, "band_x", "station-model-b"); err != nil {
		t.Fatalf("MoveBand: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/bands/band_x" {
		t.Errorf("sent %s %s, want PATCH /bands/band_x", gotMethod, gotPath)
	}
	if body["node_id"] != "station-model-b" {
		t.Errorf("body node_id = %v, want the destination node", body["node_id"])
	}
}

// A broker refusal must reach the operator as the SENTENCE the broker wrote, not as a JSON
// envelope. The broker replies {"error":{"message":"..."}}; anything that renders the raw
// blob puts braces and quotes on a status line the operator is meant to act on.
func TestBandErrorsSurfaceTheHumanSentence(t *testing.T) {
	const human = "that model already carries its own private band - move or revoke that one first"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"message":"` + human + `"}}`))
	}))
	defer srv.Close()

	err := MoveBand(srv.URL, "band_x", "occupied-node")
	if err == nil {
		t.Fatal("a 409 must be an error")
	}
	if err.Error() != human {
		t.Errorf("error = %q,\nwant the bare sentence %q", err.Error(), human)
	}
	for _, junk := range []string{"{", "}", `"error"`, "message"} {
		if strings.Contains(err.Error(), junk) {
			t.Errorf("the error leaked JSON syntax %q: %s", junk, err.Error())
		}
	}
}

func TestRevokeBandSurfacesTheHumanSentence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such band"}}`))
	}))
	defer srv.Close()

	err := RevokeBand(srv.URL, "band_gone")
	if err == nil || err.Error() != "no such band" {
		t.Errorf("err = %v, want the bare sentence \"no such band\"", err)
	}
}

// An unreachable broker is a transport failure, not a band problem - it must be
// distinguishable so callers do not tell the operator their band is missing.
func TestBandCallsReportAnUnreachableBroker(t *testing.T) {
	// A closed server: the port is dead.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	if err := RevokeBand(dead, "band_x"); err == nil {
		t.Error("revoking against a dead broker must error")
	}
	if err := MoveBand(dead, "band_x", "n"); err == nil {
		t.Error("moving against a dead broker must error")
	}
}

// The list must carry the node id, or BASE STATION cannot tell the operator WHICH model a
// band is on - the single fact missing when the founder hit the quota wall.
func TestListBandsCarriesTheNodeAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"bands":[{"id":"band_x","display":"145.225 MHz · ••••-••••",
			"node_id":"roggentoo-gemma-4-31b","status":"active","revoked":false,"created_at":1000}]}`))
	}))
	defer srv.Close()

	bands, err := ListBands(srv.URL)
	if err != nil {
		t.Fatalf("ListBands: %v", err)
	}
	if len(bands) != 1 {
		t.Fatalf("got %d bands, want 1", len(bands))
	}
	b := bands[0]
	if b.NodeID != "roggentoo-gemma-4-31b" || b.Status != "active" || b.ID != "band_x" {
		t.Errorf("band = %+v, want the node id and status carried through", b)
	}
	if b.CreatedAt != 1000 {
		t.Errorf("CreatedAt = %d, want 1000 (needed to order the list)", b.CreatedAt)
	}
}
