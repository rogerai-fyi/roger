package main

// A member states the name it enrolled under in its describe answer, so other members can call it
// what its authority does instead of its hardware word (found 2026-10-10).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

func TestTheFaceStatesTheNameThisMachineEnrolledUnder(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)

	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, edge.DescribePath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var d edge.Describe
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&d))
	require.Equal(t, "workshop", d.Name)
}
