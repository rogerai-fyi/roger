package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v5/internal/store"
)

// PATCH /bands/{id} is the MOVE: it repoints a band at a different node so an owner can
// put their band on a different model WITHOUT rotating the secret code and cutting off
// everyone tuned in. Spec: features/sharing/band_management.feature.

// ownerPatch builds an owner-authenticated PATCH with a JSON body.
func ownerPatch(path, pubkey, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	r.Header.Set("X-Roger-Pubkey", pubkey)
	r.Header.Set("Content-Type", "application/json")
	signAsTestOwner(r, pubkey, []byte(body))
	return r
}

func TestBandMoveKeepsTheCode(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{
		ID: "band_x", Owner: o.Pubkey, CodeHash: "hash_x",
		CodeDisplay: "147.520 MHz · ••••-••••", NodeID: "amber-fox-model-a",
	})

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerPatch("/bands/band_x", o.Pubkey, `{"node_id":"amber-fox-model-b"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /bands/band_x = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// The response echoes the band so a client can render the move without a second call.
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ok"] != true {
		t.Errorf("response should report ok, got %+v", resp)
	}
	if resp["node_id"] != "amber-fox-model-b" {
		t.Errorf("response node_id = %v, want the destination", resp["node_id"])
	}
	// The secret must never appear in a move response, like every other band surface.
	body := w.Body.String()
	for _, leak := range []string{"hash_x", "code_hash", "band_code"} {
		if strings.Contains(body, leak) {
			t.Errorf("move response leaked %q: %s", leak, body)
		}
	}

	// The band really moved, and the code still resolves it.
	got, ok, _ := b.db.BandByCodeHash("hash_x")
	if !ok || got.NodeID != "amber-fox-model-b" {
		t.Fatalf("band did not move (ok=%v %+v)", ok, got)
	}
	if _, ok, _ := b.db.BandByNode("amber-fox-model-a"); ok {
		t.Error("the source node must no longer carry the band")
	}
}

func TestBandMoveRejectsOccupiedDestination(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: o.Pubkey, CodeHash: "h1", NodeID: "n-a"})
	_ = b.db.CreateBand(store.Band{ID: "band_y", Owner: o.Pubkey, CodeHash: "h2", NodeID: "n-b"})

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerPatch("/bands/band_x", o.Pubkey, `{"node_id":"n-b"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("move onto an occupied node = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	// The message must name the remedy, not just refuse.
	if !strings.Contains(strings.ToLower(w.Body.String()), "already carries") {
		t.Errorf("409 should explain the destination already has a band, got %s", w.Body.String())
	}
	// Both bands untouched.
	if got, _, _ := b.db.BandByNode("n-a"); got.ID != "band_x" {
		t.Error("a refused move must leave the source band in place")
	}
	if got, _, _ := b.db.BandByNode("n-b"); got.ID != "band_y" {
		t.Error("a refused move must leave the destination band in place")
	}
}

func TestBandMoveIsOwnerScopedAndOpaque(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: "someone_else", CodeHash: "h1", NodeID: "n-a"})

	// A band belonging to another owner must answer exactly like one that does not exist,
	// so PATCH cannot be used to enumerate other people's band ids.
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerPatch("/bands/band_x", o.Pubkey, `{"node_id":"n-b"}`))

	wUnknown := httptest.NewRecorder()
	b.bandsByID(wUnknown, ownerPatch("/bands/band_nope", o.Pubkey, `{"node_id":"n-b"}`))

	if w.Code != http.StatusNotFound || wUnknown.Code != http.StatusNotFound {
		t.Fatalf("foreign=%d unknown=%d, want both 404", w.Code, wUnknown.Code)
	}
	if w.Body.String() != wUnknown.Body.String() {
		t.Errorf("a foreign band must be indistinguishable from a missing one:\n foreign: %s\n unknown: %s",
			w.Body.String(), wUnknown.Body.String())
	}
	if got, _, _ := b.db.BandByNode("n-a"); got.ID != "band_x" {
		t.Error("a foreign move must not disturb the band")
	}
}

func TestBandMoveRequiresOwnerAndRejectsBadInput(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: o.Pubkey, CodeHash: "h1", NodeID: "n-a"})

	// Anonymous -> 403, same gate as list/revoke.
	wa := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/bands/band_x", strings.NewReader(`{"node_id":"n-b"}`))
	b.bandsByID(wa, r)
	if wa.Code != http.StatusForbidden {
		t.Fatalf("anon PATCH = %d, want 403", wa.Code)
	}

	// An empty node_id would unbind the band from every node - refuse it.
	we := httptest.NewRecorder()
	b.bandsByID(we, ownerPatch("/bands/band_x", o.Pubkey, `{"node_id":""}`))
	if we.Code != http.StatusBadRequest {
		t.Errorf("empty node_id = %d, want 400", we.Code)
	}

	// Malformed JSON must not be silently treated as an empty move.
	wj := httptest.NewRecorder()
	b.bandsByID(wj, ownerPatch("/bands/band_x", o.Pubkey, `{not json`))
	if wj.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", wj.Code)
	}

	// Untouched throughout.
	if got, _, _ := b.db.BandByNode("n-a"); got.ID != "band_x" {
		t.Error("no rejected request may move the band")
	}
}

func TestBandMoveRefusesRevokedBand(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: o.Pubkey, CodeHash: "h1", NodeID: "n-a", Revoked: true})

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerPatch("/bands/band_x", o.Pubkey, `{"node_id":"n-b"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("moving a revoked band = %d, want 404 (a burnt code must not be resurrected)", w.Code)
	}
}

// The method guard must now allow PATCH alongside DELETE, and still refuse the rest.
func TestBandsByIDMethodGuardAllowsPatch(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: o.Pubkey, CodeHash: "h1", NodeID: "n-a"})

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodGet, "/bands/band_x", o.Pubkey))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /bands/{id} = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "PATCH") || !strings.Contains(allow, "DELETE") {
		t.Errorf("Allow = %q, want both DELETE and PATCH", allow)
	}
}

// /bands/resolve stays reserved: it must never be treated as a band id by PATCH.
func TestBandMoveNeverTreatsResolveAsAnID(t *testing.T) {
	b, o := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerPatch("/bands/resolve", o.Pubkey, `{"node_id":"n-b"}`))
	if w.Code != http.StatusNotFound {
		t.Errorf("PATCH /bands/resolve = %d, want 404 (reserved path)", w.Code)
	}
}
