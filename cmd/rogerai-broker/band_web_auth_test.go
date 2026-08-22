package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// THE WEB BAND LIST WAS SILENTLY EMPTY FOR EVERYONE.
//
// web/src/js/private.js lists bands with fetch(credentials:"include") - a session cookie,
// which is all a browser has (it holds no Ed25519 signing key). requireOwner read the
// X-Roger-Pubkey header EXCLUSIVELY, so every browser got 403, and the page's
// `.catch(function () { renderBands([]); })` turned that into a confident "No private
// bands yet." Verified live against production on 2026-08-07.
//
// Reads may authenticate with the cookie; MUTATIONS still require the signed key, so a
// cookie alone can never revoke or move a band.
// Spec: features/sharing/band_management.feature - "An owner's bands actually appear on
// the website" / "an authentication failure is shown as a failure, never as an empty list".

// webSessionReq builds a request carrying a valid browser session cookie for login.
func webSessionReq(t *testing.T, b *broker, method, path, login string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSessionWallet(login, 1, "u_gh_1", time.Now().Add(time.Hour).Unix())})
	return r
}

func TestWebSessionCanListItsOwnBands(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{
		ID: "band_1", Owner: o.Pubkey, CodeDisplay: "145.225 MHz · ••••-••••",
		NodeID: "roggentoo-gemma-4-31b",
	})

	w := httptest.NewRecorder()
	b.bands(w, webSessionReq(t, b, http.MethodGet, "/bands", o.Login))
	if w.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated GET /bands = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Bands []map[string]any `json:"bands"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Bands) != 1 || resp.Bands[0]["id"] != "band_1" {
		t.Fatalf("bands = %+v, want the owner's one band", resp.Bands)
	}
	// Still secret-free, exactly like the signed path.
	if strings.Contains(w.Body.String(), "code_hash") {
		t.Error("the web list leaked a code hash")
	}
}

// A cookie is enough to READ. It must never be enough to revoke or move: those burn or
// repoint a code, and they stay on the signed-key path.
func TestWebSessionCannotMutateBands(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateBand(store.Band{ID: "band_1", Owner: o.Pubkey, CodeHash: "h1", NodeID: "n-a"})

	wd := httptest.NewRecorder()
	b.bandsByID(wd, webSessionReq(t, b, http.MethodDelete, "/bands/band_1", o.Login))
	if wd.Code != http.StatusForbidden {
		t.Errorf("cookie DELETE = %d, want 403 (mutations need the signed key)", wd.Code)
	}

	wp := httptest.NewRecorder()
	rp := webSessionReq(t, b, http.MethodPatch, "/bands/band_1", o.Login)
	rp.Body = http.NoBody
	b.bandsByID(wp, rp)
	if wp.Code != http.StatusForbidden {
		t.Errorf("cookie PATCH = %d, want 403 (mutations need the signed key)", wp.Code)
	}

	// Nothing changed.
	if got, _, _ := b.db.BandByNode("n-a"); got.ID != "band_1" || got.Revoked {
		t.Error("a cookie-only request must not have altered the band")
	}
}

// No session and no key is still a refusal - and an explicit one, so a client can tell
// "not signed in" apart from "you have no bands".
func TestAnonymousBandListStillRefuses(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.bands(w, httptest.NewRequest(http.MethodGet, "/bands", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("anon GET /bands = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "roger login") {
		t.Errorf("the refusal should name the remedy, got %s", w.Body.String())
	}
}

// A session cookie for a login with no bound owner record must not resolve to somebody.
func TestWebSessionForUnknownOwnerRefuses(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.bands(w, webSessionReq(t, b, http.MethodGet, "/bands", "not-an-owner"))
	if w.Code != http.StatusForbidden {
		t.Errorf("unknown-owner session = %d, want 403", w.Code)
	}
}
