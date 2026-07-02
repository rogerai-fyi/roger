package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCorsCredsEchoesWebOriginNeverWildcard locks the credentialed-CORS contract
// offline (previously only caught by `make smoke-live`): corsCreds and its preflight
// variant echo the EXACT ROGERAI_WEB_ORIGIN and set Access-Control-Allow-Credentials:
// true, and NEVER emit "*" for the allow-origin (a credentialed "*" is forbidden by
// the Fetch spec and would be a security regression). A non-matching origin gets NO
// allow-origin header at all (it is not reflected).
func TestCorsCredsEchoesWebOriginNeverWildcard(t *testing.T) {
	const webOrigin = "https://app.rogerai.fyi"
	t.Setenv("ROGERAI_WEB_ORIGIN", webOrigin)

	cases := []struct {
		name         string
		origin       string
		wantAllow    string // expected Access-Control-Allow-Origin ("" = header absent)
		wantCreds    bool   // expect Access-Control-Allow-Credentials: true
		allowMatches bool
	}{
		{"matching web origin", webOrigin, webOrigin, true, true},
		{"foreign origin not reflected", "https://evil.example", "", false, false},
		{"the public site origin (non-configured) not reflected", "https://rogerai.fyi", "", false, false},
		{"empty origin", "", "", false, false},
	}

	for _, tc := range cases {
		t.Run("corsCreds/"+tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/me", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			corsCreds(w, r)
			assertCredsHeaders(t, w, tc.wantAllow, tc.wantCreds)
		})

		t.Run("corsCredsPreflight/"+tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodOptions, "/me", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if !corsCredsPreflight(w, r) {
				t.Fatal("corsCredsPreflight must handle an OPTIONS request (return true)")
			}
			if w.Code != http.StatusNoContent {
				t.Errorf("preflight status = %d, want 204", w.Code)
			}
			assertCredsHeaders(t, w, tc.wantAllow, tc.wantCreds)
		})
	}

	// A non-OPTIONS request must NOT be treated as a preflight.
	t.Run("preflight passes through non-OPTIONS", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/me", nil)
		if corsCredsPreflight(w, r) {
			t.Error("corsCredsPreflight must return false for a non-OPTIONS request")
		}
	})
}

// assertCredsHeaders checks the credentialed-CORS invariants: the allow-origin is
// exactly wantAllow (never "*"), and credentials is "true" iff wantCreds.
func assertCredsHeaders(t *testing.T, w *httptest.ResponseRecorder, wantAllow string, wantCreds bool) {
	t.Helper()
	gotAllow := w.Header().Get("Access-Control-Allow-Origin")
	if gotAllow == "*" {
		t.Fatalf("credentialed CORS must NEVER emit Access-Control-Allow-Origin: * (got %q)", gotAllow)
	}
	if gotAllow != wantAllow {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", gotAllow, wantAllow)
	}
	gotCreds := w.Header().Get("Access-Control-Allow-Credentials")
	if wantCreds {
		if gotCreds != "true" {
			t.Errorf("Access-Control-Allow-Credentials = %q, want true", gotCreds)
		}
	} else if gotCreds != "" {
		t.Errorf("non-matching origin must not set Access-Control-Allow-Credentials, got %q", gotCreds)
	}
}

// TestCorsAllowsAttachHeader locks the BASE STATION requirement: the web viewer streams and
// sends with an X-Roger-Attach header (EventSource can't set headers, so it uses fetch), which
// forces a CORS preflight. If the broker's Allow-Headers omits X-Roger-Attach the browser
// blocks the whole live-session view — this pins that header into the allow list.
func TestCorsAllowsAttachHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/rc/rcs_x/stream", nil)
	r.Header.Set("Origin", envOr("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi"))
	if !corsCredsPreflight(w, r) {
		t.Fatal("preflight must handle the OPTIONS")
	}
	allow := w.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allow, "X-Roger-Attach") {
		t.Fatalf("Allow-Headers must include X-Roger-Attach (the web viewer's attach bearer), got %q", allow)
	}
}

// TestPublicCorsIsWildcardNoCreds asserts the PUBLIC read-only cors helper is the
// opposite policy: it DOES emit "*" (any browser can read /discover,/market) and
// never sets credentials - so the wildcard and credentials policies stay disjoint.
func TestPublicCorsIsWildcardNoCreds(t *testing.T) {
	w := httptest.NewRecorder()
	cors(w)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("public cors Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("public cors must NOT set credentials (would be a credentialed-* regression), got %q", got)
	}

	// corsPreflight (public) likewise: 204 + wildcard, no credentials.
	wp := httptest.NewRecorder()
	rp := httptest.NewRequest(http.MethodOptions, "/market", nil)
	if !corsPreflight(wp, rp) {
		t.Fatal("public corsPreflight must handle OPTIONS")
	}
	if wp.Code != http.StatusNoContent {
		t.Errorf("public preflight status = %d, want 204", wp.Code)
	}
	if got := wp.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("public preflight allow-origin = %q, want *", got)
	}
	if got := wp.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("public preflight must not set credentials, got %q", got)
	}
}
