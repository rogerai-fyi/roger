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
		require.Equal(t, probeRogerAI, res, name)
	}
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"smollm2-135m-instruct","object":"model","owned_by":"llamacpp"}]}`)
	}))
	defer real.Close()
	models, _, _, res := probeModels(real.URL+"/v1", nil)
	require.Equal(t, probeOK, res)
	require.Equal(t, []string{"smollm2-135m-instruct"}, models)
}

// Pre-push audit 2026-10-27: a broker turned away as a model server must not come back as a voice
// server. Its /v1/audio/speech answers an anonymous POST with 401, which the voice probe reads as a
// live route, so the whole pipeline, not only the model listing, must leave it out.
func TestABrokerIsNotFoundAsAVoiceServerEither(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rogerai-Cost", "0")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"smollm2-135m-instruct","object":"model","owned_by":"rogerai"}]}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer broker.Close()
	found, needKey := detectWith([]candidate{{name: "configured", base: broker.URL + "/v1"}})
	for _, f := range found {
		require.NotEqual(t, broker.URL+"/v1", f.BaseURL, "the broker was found as %v", f.Models)
	}
	require.NotContains(t, needKey, broker.URL+"/v1")
	_, st := ProbeKey(broker.URL, "")
	require.Equal(t, Unreachable, st)
}

// DisablePortScan leaves the named defaults, the environment and explicit URLs, and drops only the
// scan of this host's listening ports.
func TestDisablePortScanDropsOnlyTheScan(t *testing.T) {
	prev := enumPorts
	t.Cleanup(func() { enumPorts = prev })
	enumPorts = func() []int { return []int{1} }
	DisablePortScan()
	require.Empty(t, enumPorts())
}
