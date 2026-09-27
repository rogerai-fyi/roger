package edge

// Executable pins for features/edge/serving_probe.feature (the face path and the client): a real
// mTLS face, a real OpenAI-compatible fake behind it, real certificates from a real authority.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

type probeRig struct {
	face     *httptest.Server
	srv      *Server
	upstream *httptest.Server
	models   atomic.Value // []string the fake lists
	hits     atomic.Int32
	member   tls.Certificate
	stranger tls.Certificate
	outsider tls.Certificate
	pin      string
}

func newProbeRig(t *testing.T) *probeRig {
	t.Helper()
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	issue := func(a *cert.Authority, name string) tls.Certificate {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		leaf, err := a.Issue(name, pub)
		require.NoError(t, err)
		return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}
	}
	rig := &probeRig{}
	rig.models.Store([]string{"qwen"})
	rig.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.hits.Add(1)
		if r.URL.Path != "/v1/models" {
			http.Error(w, "not the models list", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-x" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var data []map[string]string
		for _, m := range rig.models.Load().([]string) {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(rig.upstream.Close)

	serveCert := issue(authority, "n_desk")
	_, servePriv, _ := ed25519.GenerateKey(rand.Reader)
	rig.member = issue(authority, "n_ibex")
	rig.stranger = issue(other, "n_ibex")
	rig.outsider = issue(authority, "n_elsewhere")
	rig.srv = NewServer(Describe{NodeID: "n_desk", Account: "acct-1", Kind: "host"}, serveCert)
	rig.srv.SetHousehold(func() ([]Instance, bool) {
		return []Instance{{Name: "serve", Bands: []string{"qwen"}, Caps: []store.EdgeCap{{Name: string(Serve), State: string(Claimed)}}}}, false
	})
	rig.srv.SetServing(Serving{
		Trust:  mustVerifier(t, authority, nil),
		Member: func(id string) bool { return id == "n_ibex" || id == "n_desk" },
		Upstream: func(band string) (string, string, string, bool) {
			if band != "qwen" {
				return "", "", "", false
			}
			return rig.upstream.URL + "/v1/chat/completions", "sk-x", "serve", true
		},
		NodeID: "n_desk", Key: servePriv,
	})
	ts := httptest.NewUnstartedServer(rig.srv.Handler())
	ts.TLS = rig.srv.ListenerTLS()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	rig.face = ts
	rig.pin = rig.srv.Fingerprint()
	return rig
}

func (r *probeRig) get(t *testing.T, c *tls.Certificate, q string) (int, string) {
	t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	if c != nil {
		cfg.Certificates = []tls.Certificate{*c}
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	resp, err := cl.Get(r.face.URL + ProbePath + q)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestProbeAnswersAMemberForABandItServes(t *testing.T) {
	rig := newProbeRig(t)
	code, body := rig.get(t, &rig.member, "?band=qwen")
	require.Equal(t, http.StatusOK, code, body)
	var res ProbeResult
	require.NoError(t, json.Unmarshal([]byte(body), &res))
	require.Equal(t, ProbeResult{Instance: "serve", Band: "qwen", Served: true}, res)
	require.Equal(t, int32(1), rig.hits.Load(), "the probe reached the server behind the face, once")
}

func TestProbeIsAMemberOnlyRead(t *testing.T) {
	rig := newProbeRig(t)
	code, _ := rig.get(t, nil, "?band=qwen")
	require.Equal(t, http.StatusUnauthorized, code, "no certificate")
	code, _ = rig.get(t, &rig.stranger, "?band=qwen")
	require.Equal(t, http.StatusUnauthorized, code, "a certificate not under this Edge's root")
	code, _ = rig.get(t, &rig.outsider, "?band=qwen")
	require.Equal(t, http.StatusForbidden, code, "under the root, but not a member of this Edge")
	require.Equal(t, int32(0), rig.hits.Load(), "a refused caller never reaches the server behind the face")
}

func TestProbeRefusals(t *testing.T) {
	rig := newProbeRig(t)
	code, body := rig.get(t, &rig.member, "")
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body, "names the band it checks")

	code, body = rig.get(t, &rig.member, "?band=gpt-oss-120b")
	require.Equal(t, http.StatusNotFound, code)
	require.Contains(t, body, "does not serve gpt-oss-120b")

	rig.models.Store([]string{"llama-3"})
	code, body = rig.get(t, &rig.member, "?band=qwen")
	require.Equal(t, http.StatusBadGateway, code)
	require.Contains(t, body, "does not list")

	rig.upstream.Close()
	code, _ = rig.get(t, &rig.member, "?band=qwen")
	require.Equal(t, http.StatusBadGateway, code, "a server that is down fails the probe")

	// POST is not a probe.
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{rig.member}}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	resp, err := cl.Post(rig.face.URL+ProbePath+"?band=qwen", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestProbeServeClientPinsAndCarriesTheReason(t *testing.T) {
	rig := newProbeRig(t)
	addr := rig.face.Listener.Addr().String()

	inst, err := ProbeServe(context.Background(), addr, rig.pin, &rig.member, "qwen")
	require.NoError(t, err)
	require.Equal(t, "serve", inst)

	before := rig.hits.Load()
	_, err = ProbeServe(context.Background(), addr, "0000", &rig.member, "qwen")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pin")
	require.Equal(t, before, rig.hits.Load(), "a face that does not match its pin is never asked")

	_, err = ProbeServe(context.Background(), addr, rig.pin, &rig.member, "gpt-oss-120b")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not serve gpt-oss-120b", "the face's own reason, not just a status")

	_, err = ProbeServe(context.Background(), addr, rig.pin, &rig.stranger, "qwen")
	require.Error(t, err)
}

func TestProbeFailsOnAServerThatAnswersBadly(t *testing.T) {
	mode := "500"
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "500":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			_, _ = io.WriteString(w, "<html>not json</html>")
		}
	}))
	t.Cleanup(bad.Close)
	srv := NewServer(Describe{NodeID: "n_desk", Account: "acct-1", Kind: "host"}, tls.Certificate{})
	srv.SetServing(Serving{Upstream: func(string) (string, string, string, bool) {
		return bad.URL + "/v1/chat/completions", "", "serve", true
	}})
	_, status, msg := srv.ProbeBand(context.Background(), "qwen")
	require.Equal(t, http.StatusBadGateway, status)
	require.Contains(t, msg, "answered 500")
	mode = "garbage"
	_, status, msg = srv.ProbeBand(context.Background(), "qwen")
	require.Equal(t, http.StatusBadGateway, status)
	require.Contains(t, msg, "not answer with a model list")

	// A face with no serving at all serves nothing, and says so.
	bare := NewServer(Describe{NodeID: "n_x"}, tls.Certificate{})
	_, status, msg = bare.ProbeBand(context.Background(), "qwen")
	require.Equal(t, http.StatusNotFound, status)
	require.Contains(t, msg, "does not serve qwen")
	require.Equal(t, "http://h/v1/models", modelsURL("http://h/v1/chat/completions/"))
}

func TestProbeServeClientRefusesNonsenseAndNeedsACertificate(t *testing.T) {
	_, err := ProbeServe(context.Background(), "127.0.0.1:1", "00", nil, "qwen")
	require.EqualError(t, err, "no certificate to present")

	face := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"served":false}`)
	}))
	t.Cleanup(face.Close)
	self := face.TLS.Certificates[0]
	pin := Fingerprint(face.Certificate().Raw)
	_, err = ProbeServe(context.Background(), face.Listener.Addr().String(), pin, &self, "qwen")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a probe result")
}
