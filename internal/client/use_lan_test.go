package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureUse runs Use with the serve seam stubbed, returning stdout, the assembled proxy
// options and every path the broker was asked for.
func captureUse(t *testing.T, model string, opt UseOptions) (string, *ProxyOptions, []string, error) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer broker.Close()
	var got *ProxyOptions
	oldServe, oldHandler := useServe, newProxyHandler
	useServe = func(string, http.Handler) error { return nil }
	newProxyHandler = func(o ProxyOptions) http.Handler { got = &o; return http.NotFoundHandler() }
	defer func() { useServe, newProxyHandler = oldServe, oldHandler }()
	out := captureOut(t, func() error { return Use(broker.URL, "u", model, opt) })
	mu.Lock()
	defer mu.Unlock()
	return out.out, got, append([]string(nil), paths...), out.err
}

type useOut struct {
	out string
	err error
}

func captureOut(t *testing.T, fn func() error) useOut {
	var err error
	out := captureStdoutT(t, func() { err = fn() })
	return useOut{out, err}
}

func TestUseInLANModeOpensOnALocalSourceAndNeverAsksTheBroker(t *testing.T) {
	out, opts, paths, err := captureUse(t, "llama-3.3-70b", UseOptions{Port: 4141, LANMode: true,
		LANSources: func(string) []LANSource { return []LANSource{{Kind: LANTower, ID: "192.168.1.20:8787"}} },
		LANTowers:  func(string) []string { return []string{"192.168.1.20:8787"} }})
	require.NoError(t, err)
	require.Empty(t, paths, "the broker was asked for %v", paths)
	require.NotNil(t, opts)
	require.True(t, opts.LANMode)
	require.Equal(t, "local-only", opts.Prefer)
	require.Contains(t, out, "tower: 192.168.1.20")
	require.Contains(t, out, "free")
	require.Contains(t, out, "local")
	for _, money := range []string{"est.", "balance", "hold"} {
		require.NotContains(t, out, money)
	}
}

func TestUseInLANModeRefusesABandNothingLocalServes(t *testing.T) {
	_, opts, paths, err := captureUse(t, "gpt-oss-120b", UseOptions{Port: 4141, LANMode: true,
		LANSources: func(string) []LANSource { return nil }})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gpt-oss-120b")
	require.Contains(t, err.Error(), "this machine, your Edge and your LAN Towers")
	require.Nil(t, opts, "no proxy was opened")
	require.Empty(t, paths)
}

func TestUsePinnedNodeNeverConsultsTheMarket(t *testing.T) {
	peer := EdgePeer{Node: "jetson", Addr: "192.168.1.77:1", Pin: strings.Repeat("ab", 32)}
	out, opts, paths, err := captureUse(t, "gpt-oss-20b", UseOptions{Port: 4141, Pinned: "edge: jetson",
		EdgePeers: func(string) []EdgePeer { return []EdgePeer{peer} }, Prefer: "local-only"})
	require.NoError(t, err)
	require.Empty(t, paths, "a pinned node consulted the broker: %v", paths)
	require.NotContains(t, out, "no station on air")
	require.Contains(t, out, "edge: jetson")
	require.Equal(t, "local-only", opts.Prefer)
	require.False(t, opts.LANMode)
}

func captureStdoutT(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
