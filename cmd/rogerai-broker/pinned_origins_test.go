package main

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The exact values now pinned in .do/app.yaml, checked against the real originAllowed.
func TestPinnedProductionOriginsAreAcceptedAndOthersAreNot(t *testing.T) {
	t.Setenv("ROGERAI_WEB_ORIGIN", "https://rogerai.fm")
	t.Setenv("ROGERAI_WEB_ORIGINS",
		"https://rogerai.fm,https://www.rogerai.fm,https://rogerai.fyi,https://www.rogerai.fyi")

	for _, ok := range []string{
		"https://rogerai.fm", "https://www.rogerai.fm",
		"https://rogerai.fyi", "https://www.rogerai.fyi",
	} {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Header.Set("Origin", ok)
		require.True(t, originAllowed(r), "%s must be allowed - sign-in depends on it", ok)
	}

	for _, bad := range []string{
		"https://evil.example",
		"http://rogerai.fm",           // scheme downgrade
		"https://rogerai.fm.evil.com", // suffix trick
		"https://notrogerai.fm",
		"", // no Origin at all
	} {
		r := httptest.NewRequest("POST", "/x", nil)
		if bad != "" {
			r.Header.Set("Origin", bad)
		}
		require.False(t, originAllowed(r), "%q must be refused", bad)
	}

	// The hint cookie's Domain comes from the SINGULAR var and must be one host.
	require.Equal(t, "rogerai.fm", webOriginHost())
}
