package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// POST /bands/{id}/rotate is the ROTATE: a fresh secret for an EXISTING band, keeping its
// id, node binding, label, quota slot and cosmetic dial. It exists because the only way to
// change a code used to be revoke + go private again, which mints a DIFFERENT band and, if
// the second step fails, leaves the operator with none.
//
// POST /bands/{id}/forget deletes a REVOKED row, the only way to clear the dead history
// that used to accumulate around a live band forever.

func rotatableBand(t *testing.T) (*broker, store.Owner, string) {
	t.Helper()
	b, o := brokerWithOwner(t)
	code, display, tail := protocol.NewBandCode()
	_ = code
	if err := b.db.CreateBand(store.Band{
		ID: "band_x", Owner: o.Pubkey, CodeHash: protocol.BandCodeHash(tail),
		CodeDisplay: display, NodeID: "amber-fox-model-a", Label: "family",
	}); err != nil {
		t.Fatalf("CreateBand: %v", err)
	}
	return b, o, tail
}

// THE HEADLINE: the old code stops resolving and the returned one works, on the SAME band.
func TestRotateBurnsTheOldCodeAndReturnsAWorkingOne(t *testing.T) {
	b, o, oldTail := rotatableBand(t)

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/rotate", o.Pubkey))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /bands/band_x/rotate = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	code, _ := resp["code"].(string)
	if strings.TrimSpace(code) == "" {
		t.Fatalf("rotate returned no code: %s", w.Body.String())
	}

	// The OLD code must be dead.
	if _, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(oldTail)); found {
		t.Error("the OLD code still resolves after a rotation")
	}
	// The NEW code must resolve THE SAME band.
	got, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(code))
	if !found {
		t.Fatal("the code the rotation returned does not resolve")
	}
	if got.ID != "band_x" || got.NodeID != "amber-fox-model-a" || got.Label != "family" {
		t.Errorf("rotation did not keep the band's identity: %+v", got)
	}
}

// The cosmetic dial survives, because the frequency is documented as never folded into the
// key. Keeping it is what makes this a rotation rather than a rename.
func TestRotateKeepsTheDial(t *testing.T) {
	b, o, _ := rotatableBand(t)
	before, _, _ := b.db.BandByNode("amber-fox-model-a")
	freqBefore, _, _ := strings.Cut(before.CodeDisplay, " · ")

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/rotate", o.Pubkey))
	if w.Code != http.StatusOK {
		t.Fatalf("rotate = %d", w.Code)
	}
	after, _, _ := b.db.BandByNode("amber-fox-model-a")
	freqAfter, _, _ := strings.Cut(after.CodeDisplay, " · ")
	if freqBefore != freqAfter {
		t.Errorf("the dial changed on rotation: %q -> %q", freqBefore, freqAfter)
	}
	// And the PERSISTED display must still be non-recoverable - a rotation must never
	// write the new tail into storage.
	if protocol.CanonicalBandTail(after.CodeDisplay) != "" {
		t.Errorf("the rotated band persisted a recoverable tail: %q", after.CodeDisplay)
	}
}

// Owner scoping: another owner's band answers exactly like one that does not exist, so
// rotate can never be used to burn someone else's code or enumerate band ids.
func TestRotateIsOwnerScopedAndOpaque(t *testing.T) {
	b, o := brokerWithOwner(t)
	// A band owned by SOMEONE ELSE. The requesting owner must be unable to tell it apart
	// from one that does not exist, or rotate becomes a band-id oracle.
	_ = b.db.CreateBand(store.Band{
		ID: "band_x", Owner: "someone_else", CodeHash: "h_foreign",
		CodeDisplay: "147.520 MHz · ••••-••••", NodeID: "n-a",
	})

	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/rotate", o.Pubkey))
	wUnknown := httptest.NewRecorder()
	b.bandsByID(wUnknown, ownerReq(http.MethodPost, "/bands/band_nope/rotate", o.Pubkey))

	if w.Code != http.StatusNotFound || wUnknown.Code != http.StatusNotFound {
		t.Fatalf("foreign=%d unknown=%d, want both 404", w.Code, wUnknown.Code)
	}
	if w.Body.String() != wUnknown.Body.String() {
		t.Errorf("a foreign band must be indistinguishable from a missing one:\n foreign: %s\n unknown: %s",
			w.Body.String(), wUnknown.Body.String())
	}
	if _, found, _ := b.db.BandByCodeHash("h_foreign"); !found {
		t.Error("a refused rotation still burnt the real owner's code")
	}
}

// Revoke is final and gave the quota slot back, so a rotation must not resurrect it.
func TestRotateRefusesARevokedBand(t *testing.T) {
	b, o, _ := rotatableBand(t)
	if _, err := b.db.SetBandRevoked("band_x", o.Pubkey, true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/rotate", o.Pubkey))
	if w.Code != http.StatusConflict {
		t.Fatalf("rotate of a revoked band = %d, want 409 (%s)", w.Code, w.Body.String())
	}
}

// A GET must not rotate. A rotation cuts off everyone tuned in, so it can never be a safe
// method - a prefetcher or a link would burn the operator's code.
func TestRotateRefusesNonPost(t *testing.T) {
	b, o, oldTail := rotatableBand(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		b.bandsByID(w, ownerReq(method, "/bands/band_x/rotate", o.Pubkey))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s rotate = %d, want 405", method, w.Code)
		}
	}
	if _, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(oldTail)); !found {
		t.Error("a refused method still rotated the band")
	}
}

// The response carries the one-time code and nothing else secret - no hash, no tail in the
// persisted display.
func TestRotateResponseLeaksNoStoredSecret(t *testing.T) {
	b, o, _ := rotatableBand(t)
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/rotate", o.Pubkey))
	body := w.Body.String()
	for _, leak := range []string{"code_hash", "codehash", "hash"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("rotate response leaked %q: %s", leak, body)
		}
	}
	after, _, _ := b.db.BandByNode("amber-fox-model-a")
	if strings.Contains(body, after.CodeHash) {
		t.Errorf("rotate response echoed the stored hash: %s", body)
	}
}

// FORGET clears a revoked row. Bands revoked BEFORE revoke started cleaning up after itself
// are still rows in the wild, so this is how an operator removes them.
func TestForgetRemovesARevokedRow(t *testing.T) {
	b, o, _ := rotatableBand(t)
	if _, err := b.db.SetBandRevoked("band_x", o.Pubkey, true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/forget", o.Pubkey))
	if w.Code != http.StatusOK {
		t.Fatalf("forget = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	bands, _ := b.db.BandsByOwner(o.Pubkey)
	if len(bands) != 0 {
		t.Errorf("the forgotten row is still listed: %+v", bands)
	}
}

// THE NEGATIVE HALF THAT MATTERS: a LIVE band must never be deletable this way. Dropping a
// live row removes its code from the resolve index while every consumer holding that code
// carries on believing it works, and frees a quota slot with no confirm anywhere.
func TestForgetRefusesALiveBand(t *testing.T) {
	b, o, oldTail := rotatableBand(t)
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodPost, "/bands/band_x/forget", o.Pubkey))
	if w.Code != http.StatusConflict {
		t.Fatalf("forget of a LIVE band = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	if _, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(oldTail)); !found {
		t.Error("a refused forget still dropped the live code from the resolve index")
	}
}

// An unknown action must 404 rather than falling through to the revoke path - otherwise
// POST /bands/{id}/anything would be a silent revoke.
func TestUnknownBandActionIsNotARevoke(t *testing.T) {
	b, o, oldTail := rotatableBand(t)
	w := httptest.NewRecorder()
	b.bandsByID(w, ownerReq(http.MethodDelete, "/bands/band_x/wat", o.Pubkey))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d, want 404", w.Code)
	}
	if _, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(oldTail)); !found {
		t.Error("an unknown action silently revoked the band")
	}
}
