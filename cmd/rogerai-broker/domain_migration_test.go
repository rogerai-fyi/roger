package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredentialedCORSAcceptsBothMigrationOrigins(t *testing.T) {
	t.Setenv("ROGERAI_WEB_ORIGIN", "https://rogerai.fm")
	t.Setenv("ROGERAI_WEB_ORIGINS", "https://rogerai.fm, https://rogerai.fyi")

	for _, origin := range []string{"https://rogerai.fm", "https://rogerai.fyi"} {
		t.Run(origin, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/account", nil)
			r.Header.Set("Origin", origin)
			corsCreds(w, r)
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("allow origin = %q, want exact %q", got, origin)
			}
			if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Fatalf("allow credentials = %q, want true", got)
			}
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.Header.Set("Origin", "https://attacker.example")
	corsCreds(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin reflected as %q", got)
	}
}

// The migration spec requires BOTH origins during the .fyi -> .fm overlap
// (features/domain/domain_migration.feature, "Browser authentication accepts both
// migration origins"). Production supplies ROGERAI_WEB_ORIGINS, but the built-in
// default must satisfy the spec on its own: if that env var is ever dropped or
// mistyped, a .fyi visitor silently loses every credentialed surface - chat, voice,
// and the wallet - with only an opaque CORS failure in the console.
func TestCredentialedCORSDefaultCoversBothMigrationOrigins(t *testing.T) {
	t.Setenv("ROGERAI_WEB_ORIGIN", "")
	t.Setenv("ROGERAI_WEB_ORIGINS", "")

	for _, origin := range []string{"https://rogerai.fm", "https://rogerai.fyi"} {
		t.Run(origin, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/account", nil)
			r.Header.Set("Origin", origin)
			corsCreds(w, r)
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("with no env configured, allow origin = %q, want exact %q", got, origin)
			}
		})
	}

	// and the default must not become a blanket allow
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.Header.Set("Origin", "https://attacker.example")
	corsCreds(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("a foreign origin was granted %q", got)
	}
}
