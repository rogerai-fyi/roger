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
