package main

// A band a SEPARATE `roger --lan share` process has on air is this machine's to its Edge: the
// process that holds the LAN face reads the share's local, owner-only record, advertises the band
// on its own instance, proves it through the face, and serves a member's turn to the recorded
// upstream over mutual TLS. Nothing new rides the wire: the advert carries the band exactly as it
// would for a share in the face's own process, and never the upstream or its key.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
)

// separateProcess starts a real, separate process and returns its pid; it is killed at cleanup.
func separateProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestASeparateLANShareIsServedToTheEdgeThroughThisFace(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	var turns atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}}})
		case "/v1/chat/completions":
			turns.Add(1)
			require.Equal(t, "Bearer upkey", r.Header.Get("Authorization"), "the share's own upstream key")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from the separate share"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	// THIS process shares nothing: no band on air, no upstream of its own.
	edgeOnAir, edgeUpstreamOf = nil, nil
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })
	require.NoError(t, writeLANShareRecord(lanShareRecord{PID: separateProcess(t), Band: "qwen",
		ChatURL: upstream.URL + "/v1/chat/completions", Key: "upkey"}))

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	h.observeSelf(time.Now())

	// Advertised and proven like an in-process share.
	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	require.Equal(t, []string{"qwen"}, after.Instances[0].Bands, "the separate share's band is advertised")
	state := ""
	for _, c := range after.Instances[0].Caps {
		if c.Name == string(edge.Serve) {
			state = c.State
		}
	}
	require.Equal(t, string(edge.Verified), state, "it answered through this face")

	// The advert never carries the upstream or its key.
	desc, _ := h.household()
	raw, _ := json.Marshal(desc)
	require.NotContains(t, string(raw), upstream.URL)
	require.NotContains(t, string(raw), "upkey")

	// A member's turn, over mutual TLS to this face, reaches the separate share's upstream.
	faceAddr := h.faceLn.Addr().(*net.TCPAddr)
	pin := fmt.Sprintf("%x", sha256.Sum256(id.Cert.Raw))
	opts := client.ProxyOptions{Model: "qwen", SessionKey: "k", Prefer: "local-only"}
	applyEdgeLadder(h.cfg, &opts)
	opts.EdgePeers = func(string) []client.EdgePeer {
		return []client.EdgePeer{{Addr: faceAddr.String(), Pin: pin, Node: "workshop", Instance: "serve"}}
	}
	srv := httptest.NewServer(client.ProxyHandler(opts))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	require.True(t, strings.Contains(string(b), "from the separate share"), string(b))
	require.Equal(t, int32(1), turns.Load(), "one turn reached the separate share's upstream")
}
