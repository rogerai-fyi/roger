package detect

// Live E2E 2026-10-27: a station joining a Tower with --models took a RogerAI broker on this machine
// as its upstream, because the broker's /v1/models lists the market's models. A RogerAI broker, or a
// `roger use` endpoint in front of one, is never a local model server to share through: a turn sent
// there would be a market request, signed by nobody, and not this machine's model.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestARogerAIBrokerIsNeverALocalUpstream(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"a broker, by its header": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Rogerai-Cost", "0")
			_, _ = io.WriteString(w, `{"data":[{"id":"smollm2-135m-instruct","object":"model","owned_by":"llamacpp"}]}`)
		},
		"a broker, by its listing": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"id":"smollm2-135m-instruct","object":"model","owned_by":"rogerai"}]}`)
		},
	} {
		srv := httptest.NewServer(h)
		models, _, _, res := probeModels(srv.URL+"/v1", nil)
		srv.Close()
		require.Empty(t, models, name)
		require.Equal(t, probeMiss, res, name)
	}
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"smollm2-135m-instruct","object":"model","owned_by":"llamacpp"}]}`)
	}))
	defer real.Close()
	models, _, _, res := probeModels(real.URL+"/v1", nil)
	require.Equal(t, probeOK, res)
	require.Equal(t, []string{"smollm2-135m-instruct"}, models)
}
