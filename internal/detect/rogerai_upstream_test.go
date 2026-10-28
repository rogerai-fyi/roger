package detect

// Live E2E 2026-10-27: a station joining a Tower with --models took a RogerAI broker on this machine
// as its upstream, because the broker's /v1/models lists the market's models. A RogerAI broker, or a
// `roger use` endpoint in front of one, is never a local model server to share through: a turn sent
// there would be a market request, signed by nobody, and not this machine's model.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	defer quietSources(t)()
	prevProbes := probes
	defer func() { probes = prevProbes }()
	probes = nil // only the broker: no named default on this host is part of the question
	found, needKey := detectWith([]candidate{{name: "configured", base: broker.URL + "/v1"}})
	for _, f := range found {
		require.NotEqual(t, broker.URL+"/v1", f.BaseURL, "the broker was found as %v", f.Models)
	}
	require.NotContains(t, needKey, broker.URL+"/v1")
	_, st := ProbeKey(broker.URL, "")
	require.Equal(t, Unreachable, st)
}

// DisablePortScan drops the scan of this host's listening ports - the host is not even asked which
// are open - and leaves the named defaults and the environment probed as before.
func TestDisablePortScanDropsOnlyTheScan(t *testing.T) {
	var scanned, named, fromEnv, bystander atomic.Int32
	serve := func(n *atomic.Int32) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(s.Close)
		return s
	}
	namedSrv, envSrv, bySrv := serve(&named), serve(&fromEnv), serve(&bystander)
	byPort := bySrv.Listener.Addr().(*net.TCPAddr).Port

	prevPorts, prevEnv, prevProbes, prevOff := enumPorts, envCands, probes, portScanOff
	t.Cleanup(func() { enumPorts, envCands, probes, portScanOff = prevPorts, prevEnv, prevProbes, prevOff })
	probes = []struct{ name, base string }{{"named", namedSrv.URL + "/v1"}}
	envCands = func() []candidate { return []candidate{{name: "env", base: envSrv.URL + "/v1"}} }
	enumPorts = func() []int { scanned.Add(1); return []int{byPort} }

	portScanOff = false
	_, _ = DetectFull()
	require.NotZero(t, bystander.Load(), "with the scan on, a listening port is probed")

	scanned.Store(0)
	named.Store(0)
	fromEnv.Store(0)
	bystander.Store(0)
	DisablePortScan()
	require.True(t, PortScanDisabled())
	_, _ = DetectFull()
	require.Zero(t, scanned.Load(), "the host is not asked for its ports")
	require.Zero(t, bystander.Load(), "and no listening port is probed")
	require.NotZero(t, named.Load(), "a named default is still probed")
	require.NotZero(t, fromEnv.Load(), "and so is the environment")
}
