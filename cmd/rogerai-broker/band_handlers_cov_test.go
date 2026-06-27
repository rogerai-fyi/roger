package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// ownerReq builds a request authenticated as the bound owner via the X-Roger-Pubkey
// header (the requireOwner path the band handlers use).
func ownerReq(method, path, pubkey string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Roger-Pubkey", pubkey)
	return r
}

// TestBandsListHandler covers GET /bands: no owner -> 403, a non-GET -> 405, and an
// owner GET returns their bands (secret-free view).
func TestBandsListHandler(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_1", Owner: o.Pubkey, CodeDisplay: "147.5 MHz", Label: "friends", NodeID: "n1"})

	// No owner header -> 403.
	w := httptest.NewRecorder()
	b.bands(w, httptest.NewRequest(http.MethodGet, "/bands", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("anon /bands = %d, want 403", w.Code)
	}

	// Non-GET with owner -> 405.
	wm := httptest.NewRecorder()
	b.bands(wm, ownerReq(http.MethodPost, "/bands", o.Pubkey))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /bands = %d, want 405", wm.Code)
	}

	// Owner GET -> the one band.
	wg := httptest.NewRecorder()
	b.bands(wg, ownerReq(http.MethodGet, "/bands", o.Pubkey))
	if wg.Code != http.StatusOK {
		t.Fatalf("GET /bands = %d, want 200 (%s)", wg.Code, wg.Body.String())
	}
	var resp struct {
		Bands []map[string]any `json:"bands"`
	}
	_ = json.Unmarshal(wg.Body.Bytes(), &resp)
	if len(resp.Bands) != 1 || resp.Bands[0]["label"] != "friends" {
		t.Errorf("bands = %+v, want 1 band 'friends'", resp.Bands)
	}
}

// TestBandsByIDHandler covers DELETE /bands/{id}: the empty/resolve guard (404), the
// no-owner gate (403), the method guard (405), an unknown id (404), and a real revoke.
func TestBandsByIDHandler(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_x", Owner: o.Pubkey, CodeDisplay: "150 MHz", NodeID: "n1"})

	// /bands/resolve is reserved (routed elsewhere) -> 404 here.
	wr := httptest.NewRecorder()
	b.bandsByID(wr, ownerReq(http.MethodDelete, "/bands/resolve", o.Pubkey))
	if wr.Code != http.StatusNotFound {
		t.Fatalf("DELETE /bands/resolve = %d, want 404", wr.Code)
	}

	// No owner -> 403.
	wa := httptest.NewRecorder()
	b.bandsByID(wa, httptest.NewRequest(http.MethodDelete, "/bands/band_x", nil))
	if wa.Code != http.StatusForbidden {
		t.Fatalf("anon DELETE = %d, want 403", wa.Code)
	}

	// Non-DELETE -> 405.
	wm := httptest.NewRecorder()
	b.bandsByID(wm, ownerReq(http.MethodGet, "/bands/band_x", o.Pubkey))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /bands/{id} = %d, want 405", wm.Code)
	}

	// Unknown id -> 404.
	wn := httptest.NewRecorder()
	b.bandsByID(wn, ownerReq(http.MethodDelete, "/bands/band_nope", o.Pubkey))
	if wn.Code != http.StatusNotFound {
		t.Fatalf("DELETE unknown = %d, want 404", wn.Code)
	}

	// Real revoke -> 200, and the band is now revoked in the store.
	wd := httptest.NewRecorder()
	b.bandsByID(wd, ownerReq(http.MethodDelete, "/bands/band_x", o.Pubkey))
	if wd.Code != http.StatusOK {
		t.Fatalf("DELETE owned band = %d, want 200 (%s)", wd.Code, wd.Body.String())
	}
	bands, _ := b.db.BandsByOwner(o.Pubkey)
	if len(bands) != 1 || !bands[0].Revoked {
		t.Errorf("band should be revoked after DELETE, got %+v", bands)
	}
}
