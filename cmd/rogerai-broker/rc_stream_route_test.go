package main

// rc_stream_route_test.go pins the fix for the Base Station "couldn't open the live stream (500)"
// bug: the remote-control viewer SSE lives at a DYNAMIC /rc/{sid}/stream path, so an exact-match
// streamRoutes set never recognized it -> it fell through to http.TimeoutHandler, whose wrapper
// ResponseWriter is NOT an http.Flusher -> rcStream 500'd with "streaming unsupported". Streaming
// RC routes must bypass the response deadline (and keep the raw Flusher); short ones stay bounded.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRCStreamRouteBypassesTimeoutHandler(t *testing.T) {
	mux := http.NewServeMux()
	var sawFlusher bool
	mux.HandleFunc("/rc/", func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	})
	// Also register a couple of the static stream/non-stream routes to guard the general path.
	mux.HandleFunc("/discover", func(w http.ResponseWriter, r *http.Request) { _, sawFlusher = w.(http.Flusher); w.WriteHeader(200) })
	h := streamSafeHandler(mux)

	cases := []struct {
		name, method, path string
		wantFlusher        bool // true => must bypass TimeoutHandler and keep the raw Flusher (SSE)
	}{
		{"rc viewer SSE keeps Flusher", http.MethodGet, "/rc/rcs_abc123/stream", true},
		{"rc host long-poll bypasses deadline", http.MethodGet, "/rc/rcs_abc123/poll", true},
		{"rc send stays bounded (short request)", http.MethodPost, "/rc/rcs_abc123/send", false},
		{"rc disable stays bounded", http.MethodPost, "/rc/rcs_abc123/disable", false},
		{"non-stream /discover stays bounded", http.MethodGet, "/discover", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sawFlusher = false
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
			if sawFlusher != tc.wantFlusher {
				t.Fatalf("%s %s: inner handler saw Flusher=%v, want %v (bypass=%v)",
					tc.method, tc.path, sawFlusher, tc.wantFlusher, tc.wantFlusher)
			}
		})
	}
}
