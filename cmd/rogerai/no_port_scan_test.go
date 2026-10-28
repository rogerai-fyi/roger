package main

// CI 2026-10-28 (#164): the gate runs every package's tests at once, and a roger test that ran the
// real detection scanned every listening port on the runner, landing unsigned probes on another
// package's fake Core (internal/towercli). No test here may probe a port it did not name.

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/detect"
)

func TestNoTestHereProbesAPortItDidNotName(t *testing.T) {
	var hits atomic.Int32
	bystander := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bystander.Close()
	_, _ = detect.DetectFull()
	require.Zero(t, hits.Load(), "a detection run in a test probed a port nobody named")
}
