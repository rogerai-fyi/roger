package main

// moderation_failsafe_test.go pins two fail-safe gaps in the URL-backend screen()
// (audit findings #8 and #13). Real httptest servers + the real screen() decode path, no mocks.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// #8: under ROGERAI_REQUIRE_MODERATION=1 a 200 whose body does not decode to a valid verdict
// (empty / HTML / an error-JSON with no flagged|results field) must FAIL CLOSED (503) - not be
// treated as an implicit ALLOW. The require=0 arm still fails open (served).
func TestModerationURL200UnparseableFailsClosed(t *testing.T) {
	for _, body := range []string{"", "<html>502 bad gateway</html>", `{"error":"upstream overloaded"}`, "{}"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body)) // implicit 200
		}))
		if st := (moderation{provider: "url", url: srv.URL, require: true, client: srv.Client()}).screen("bad").status; st != http.StatusServiceUnavailable {
			t.Errorf("200 unparseable %q + require=1 must fail CLOSED (503), got %d", body, st)
		}
		if st := (moderation{provider: "url", url: srv.URL, client: srv.Client()}).screen("bad").status; st != 0 {
			t.Errorf("200 unparseable %q + require=0 should fail open (allow), got %d", body, st)
		}
		srv.Close()
	}
	// A valid {"flagged":false} 200 must still ALLOW even under require=1 (not over-fail-closed).
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"flagged":false}`))
	}))
	defer ok.Close()
	if st := (moderation{provider: "url", url: ok.URL, require: true, client: ok.Client()}).screen("fine").status; st != 0 {
		t.Errorf("valid not-flagged verdict must ALLOW even under require=1, got %d", st)
	}
}

// #13: the documented flagged-only adapter shape {"flagged":true} carries no category, so the
// 18 USC 2258A preserve+CyberTipline path could never fire for it. MODERATION_DEFAULT_CATEGORY
// lets an operator name the category to assume for an uncategorized flag so preservation is not
// silently skipped. Unset (default) keeps a plain 451 with no preserve; no NCMEC over-report.
func TestModerationFlaggedOnlyAdapterDefaultCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"flagged":true}`)) // no categories map
	}))
	defer srv.Close()

	// Default (knob UNSET): an uncategorized flag is a plain 451, NO preserve - behavior
	// unchanged from before this fix (so no non-CSAM flag is ever over-reported).
	base := moderation{provider: "url", url: srv.URL, client: srv.Client()}
	base.csamCats = loadCSAMCategories("") // defaults (s4, sexual/minors)
	if r := base.screen("bad"); r.status != http.StatusUnavailableForLegalReasons || r.csam {
		t.Fatalf("knob unset: want 451 + csam=false, got status=%d csam=%v", r.status, r.csam)
	}

	// Knob SET to a CSAM code: an uncategorized flag now assumes that category and triggers
	// the preserve path (csam=true), closing the 2258A gap for a flagged-only backend.
	withKnob := moderation{provider: "url", url: srv.URL, client: srv.Client(), defaultCat: "s4"}
	withKnob.csamCats = loadCSAMCategories("")
	if r := withKnob.screen("bad"); r.status != http.StatusUnavailableForLegalReasons || !r.csam {
		t.Fatalf("knob=s4: want 451 + csam=true for the preserve path, got status=%d csam=%v", r.status, r.csam)
	}

	// Knob SET to a NON-CSAM code: the flag is assumed non-CSAM, so it stays reject-only.
	withBenign := moderation{provider: "url", url: srv.URL, client: srv.Client(), defaultCat: "violence"}
	withBenign.csamCats = loadCSAMCategories("")
	if r := withBenign.screen("bad"); r.csam {
		t.Errorf("knob=violence: a non-CSAM default must NOT set csam, got csam=true category=%q", r.category)
	}
}
