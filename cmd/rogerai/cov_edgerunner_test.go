package main

// Coverage for the receiver side's live pieces: where a delegated turn runs on this node, the
// maintenance and model closures behind the face, one real headless turn against a real
// OpenAI-compatible server, its sandbox, and the pid checks a stop relies on.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
)

func TestEdgeLiveTurnDepsResolveWhatThisProcessHasOnAir(t *testing.T) {
	useTempConfig(t)
	prevUp, prevAir := edgeUpstreamOf, edgeOnAir
	t.Cleanup(func() { edgeUpstreamOf, edgeOnAir = prevUp, prevAir })
	edgeUpstreamOf = nil
	d := edgeLiveTurnDeps(nil)

	_, _, _, ok := d.band("")
	require.False(t, ok, "a roger that shares nothing runs no delegated turn")

	edgeAttachUpstream(func(band string) (string, string, bool) {
		return "http://127.0.0.1:9/v1/chat/completions", "k-" + band, band == "alpha"
	})
	edgeAttachOnAir(func() []string { return nil })
	_, _, _, ok = d.band("")
	require.False(t, ok, "nothing on air, nothing to default to")

	edgeAttachOnAir(func() []string { return []string{"zeta", "alpha"} })
	u, k, m, ok := d.band("")
	require.True(t, ok)
	require.Equal(t, "alpha", m, "no band asked for: the first on air, in a stable order")
	require.Equal(t, "k-alpha", k)
	require.Equal(t, "http://127.0.0.1:9/v1/chat/completions", u)
	_, _, _, ok = d.band("zeta")
	require.False(t, ok, "a band with no upstream here is not served here")

	// The maintenance and model closures go through the owner's switches on this machine.
	_, refused := d.maintain("n_caller", json.RawMessage(`{"action":"restart","target":"roger"}`))
	require.Equal(t, edge.MaintainOffReason, refused)
	_, refused = d.models("n_caller", json.RawMessage(`{"action":"status"}`))
	require.Equal(t, edge.ServeControlOffReason, refused)

	d = edgeLiveTurnDeps(func() string { return "bench" })
	_, refused = d.maintain("n_caller", json.RawMessage(`{"action":"restart","target":"roger"}`))
	require.Equal(t, edge.MaintainOffReason, refused)
}

func TestEdgeRunDelegatedTurnAgainstARealServer(t *testing.T) {
	useTempConfig(t)
	var gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, gotAuth = body.Model, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"the logs are quiet"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	out, err := edgeRunDelegatedTurn(context.Background(), srv.URL+"/v1/chat/completions", "sk-local", "tiny", "summarize the logs")
	require.NoError(t, err)
	require.Contains(t, out, "the logs are quiet")
	require.Equal(t, "tiny", gotModel)
	require.Equal(t, "Bearer sk-local", gotAuth)
	fi, err := os.Stat(filepath.Join(filepath.Dir(configPath()), "edge-delegate"))
	require.NoError(t, err)
	require.True(t, fi.IsDir(), "the turn ran in its own sandbox directory")
}

func TestEdgeDelegateRootRefusesRatherThanFallBackToTheCwd(t *testing.T) {
	useTempConfig(t)
	dir := filepath.Join(filepath.Dir(configPath()), "edge-delegate")
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o700))
	require.NoError(t, os.WriteFile(dir, []byte("not a dir"), 0o600))
	_, err := edgeDelegateRoot()
	require.ErrorContains(t, err, "could not prepare the delegate sandbox")
	_, err = edgeRunDelegatedTurn(context.Background(), "http://127.0.0.1:9", "", "tiny", "hi")
	require.ErrorContains(t, err, "could not prepare the delegate sandbox", "the turn is refused, never run elsewhere")
}

func TestEdgeStopPidChecksAreReal(t *testing.T) {
	require.NoError(t, edgeSignal(os.Getpid(), syscall.Signal(0)), "signal 0 to a live process is a liveness check")
	require.Error(t, edgeSignal(1<<30, syscall.Signal(0)))

	isRoger, known := edgeProcIsRoger(os.Getpid())
	if _, err := os.Stat("/proc/self/cmdline"); err == nil {
		require.True(t, known)
		require.True(t, isRoger, "the test binary is a roger (rogerai.test)")
	}
	_, known = edgeProcIsRoger(1 << 30)
	require.False(t, known, "a pid that is gone cannot be vouched for")
}
