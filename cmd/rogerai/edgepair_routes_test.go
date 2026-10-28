package main

// The Edge authority's pairing routes are exactly the three the wire comment at
// internal/localplane/pair.go names (the iOS handoff codes against them): each answers an unsigned
// request with the uniform 401, and nothing a Tower serves besides them is reachable here.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

func TestEdgeAuthorityPairRoutes(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/local/pair/", edgePairing(local).Handler()) // as edgehost mounts it
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/local/pair/start", "/local/pair/reveal", "/local/pair/wait"} {
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewBufferString(`{}`))
		require.NoError(t, err)
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, path)
		require.JSONEq(t, `{"error":"unauthorized"}`, b.String(), path)
	}
	for _, path := range []string{"/local/poll", "/local/join", "/local/pair/other", "/v1/models"} {
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewBufferString(`{}`))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}
