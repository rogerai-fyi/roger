package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoadRecount covers the recount config loader: env overrides for tolerance +
// strike-tolerance, and the strike>=billing clamp.
func TestLoadRecount(t *testing.T) {
	t.Setenv("ROGERAI_RECOUNT_TOLERANCE", "0.05")
	t.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", "0.01") // tighter than billing -> clamped up
	c := loadRecount()
	if c.tolerance != 0.05 {
		t.Errorf("recount tolerance = %v, want 0.05", c.tolerance)
	}
	if c.strikeTolerance < c.tolerance {
		t.Errorf("strike tolerance %v must clamp up to >= billing tolerance %v", c.strikeTolerance, c.tolerance)
	}
	if c.client == nil {
		t.Error("loadRecount should set an http client")
	}
}

// TestParseMeasurements covers the TEE measurement allow-list parser: valid hex entries
// decode, while blanks / comments / invalid hex are skipped.
func TestParseMeasurements(t *testing.T) {
	got := parseMeasurements("deadbeef, # a comment\n , 00ff , zznothex")
	if len(got) != 2 {
		t.Fatalf("parseMeasurements = %d entries, want 2 (deadbeef + 00ff)", len(got))
	}
	if got[0][0] != 0xde || got[1][1] != 0xff {
		t.Errorf("parseMeasurements decoded wrong bytes: %x", got)
	}
	if len(parseMeasurements("")) != 0 {
		t.Error("empty input should yield no measurements")
	}
}

// TestEnvIntAndTCBFloor covers the int env helper + the TEE TCB-floor builder.
func TestEnvIntAndTCBFloor(t *testing.T) {
	t.Setenv("ROGERAI_TEST_INT", "42")
	if envInt("ROGERAI_TEST_INT", 7) != 42 {
		t.Error("envInt override failed")
	}
	t.Setenv("ROGERAI_TEST_INT", "nope")
	if envInt("ROGERAI_TEST_INT", 7) != 7 {
		t.Error("envInt garbage should fall back to default")
	}
	t.Setenv("ROGERAI_TEE_MIN_BL_SPL", "3")
	if tcbFloorFromEnv().BlSpl != 3 {
		t.Error("tcbFloorFromEnv should read the BL_SPL floor")
	}
}

// TestPayoutsSubtree404 covers the /payouts/{id}/... router's unknown-subpath 404.
func TestPayoutsSubtree404(t *testing.T) {
	b, _ := brokerWithOwner(t)
	w := httptest.NewRecorder()
	b.payoutsSubtree(w, httptest.NewRequest(http.MethodGet, "/payouts/123/bogus", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("payoutsSubtree(unknown subpath) = %d, want 404", w.Code)
	}
}
