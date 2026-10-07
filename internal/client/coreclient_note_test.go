package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/coreclient"
)

// TestBrokerClientsMarkThemselves pins features/packaging/one_binary.feature section 4: every
// client that talks to Roger Core records itself, so a standalone Tower's serve refuses to start
// beside one. Roger's broker reads, its band discovery and its relay proxy are Core clients too.
func TestBrokerClientsMarkThemselves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"broker read", func() { _ = getJSON(srv.URL, "/balance", "", &map[string]any{}) }},
		{"band discovery", func() { _, _ = discover(srv.URL) }},
		{"relay proxy", func() { _ = ProxyHandlerLive(NewProxyOptionsHolder(ProxyOptions{Broker: srv.URL})) }},
	} {
		coreclient.Reset()
		tc.call()
		got := strings.Join(coreclient.Noted(), ",")
		if got == "" {
			t.Errorf("%s: no Core client recorded", tc.name)
		}
	}
	coreclient.Reset()
}
