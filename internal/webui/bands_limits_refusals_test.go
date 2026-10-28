package webui

// The console's band and spend-limit writes when something is not right: the broker refuses, the
// broker is absent, or the operator typed something a cap cannot mean. Each refusal is SPECIFIC and
// nothing is half-applied. Real Server over httptest with its token; the broker is a real HTTP
// server that answers every request with an error, the way a broker that is down or angry would.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func angryBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"broker on fire"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func webPostBody(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	resp := webDo(t, s, http.MethodPost, path, body)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func TestBandActionsSurfaceABrokerRefusal(t *testing.T) {
	s := New(testCtrl(), Options{Broker: angryBroker(t)})
	for _, act := range []string{"label", "rotate", "revoke", "forget"} {
		code, body := webPostBody(t, s, "/api/bands/"+act, `{"id":"band_1","label":"x","model":"amber-fox-m1"}`)
		require.Equal(t, http.StatusBadGateway, code, act)
		require.NotContains(t, body, `"ok":true`, "%s must not claim success over a broker failure", act)
		require.NotContains(t, body, `"code"`, "a failed rotate never shows a code")
	}
}

func TestBandListSurfacesABrokerRefusal(t *testing.T) {
	s := New(testCtrl(), Options{Broker: angryBroker(t)})
	resp := webDo(t, s, http.MethodGet, "/api/bands", "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode, "a broker failure is not an empty band list")
}

func TestBandActionWithoutABroker(t *testing.T) {
	s := New(testCtrl(), Options{})
	code, body := webPostBody(t, s, "/api/bands/label", `{"id":"band_1"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body, "broker is not configured")
}

func TestModelForNodeOnlyMatchesThisMachine(t *testing.T) {
	s := New(testCtrl(), Options{})
	require.Equal(t, "", s.modelForNode(""))
	require.Equal(t, "", s.modelForNode("some-other-station-m1"))
}

func TestSetLimitRefusals(t *testing.T) {
	var wrote []string
	read := func() map[string]SpendLimit { return map[string]SpendLimit{} }
	write := func(model string, l SpendLimit) { wrote = append(wrote, model) }

	t.Run("not editable", func(t *testing.T) {
		s := New(testCtrl(), Options{ReadLimits: read})
		code, body := webPostBody(t, s, "/api/limits", `{"model":"m1","max_out":5}`)
		require.Equal(t, http.StatusServiceUnavailable, code)
		require.Contains(t, body, "not editable on this node")
	})
	s := New(testCtrl(), Options{ReadLimits: read, WriteLimit: write})
	for _, tc := range []struct{ body, want string }{
		{`not json`, "invalid JSON body"},
		{`{"model":"   ","max_out":5}`, "which band?"},
		{`{"model":"m1","max_out":-1}`, "cannot be negative"},
		{`{"model":"m1","min_tps":-0.5}`, "cannot be negative"},
	} {
		code, body := webPostBody(t, s, "/api/limits", tc.body)
		require.Equal(t, http.StatusBadRequest, code, tc.body)
		require.True(t, strings.Contains(body, tc.want), "%s -> %q", tc.body, body)
	}
	require.Empty(t, wrote, "no refused cap is ever written")

	code, _ := webPostBody(t, s, "/api/limits", `{"model":" m1 ","max_out":0}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"m1"}, wrote, "the model is trimmed and a 0 cap (no cap) is accepted")
}
