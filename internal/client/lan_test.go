package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

// fakeTowerDiscover is a /discover that, like the plane, requires a signature and lists offers.
func fakeTowerDiscover(t *testing.T, offers []Offer, admitted bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(protocol.HeaderSig) == "" || !admitted {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": offers})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type dialMap map[string]string

func (m dialMap) client() *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			real, ok := m[addr]
			if !ok {
				return nil, fmt.Errorf("dial %s: connection refused", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, real)
		}}}
}

func lanKey(t *testing.T) ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

func TestLANDiscoverMergesTheThreeSources(t *testing.T) {
	tw := fakeTowerDiscover(t, []Offer{{Model: "llama-3.3-70b", Online: true}, {Model: "qwen3-8b", Online: true}}, true)
	dm := dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}
	bands, status := LANDiscover(context.Background(), LANInputs{
		Self:   []string{"qwen3-8b"},
		Edge:   []LANEdgeBand{{Node: "jetson", Band: "gpt-oss-20b", Verified: true, LANDirect: true}},
		Towers: []string{"192.168.1.20:8787"},
		Key:    lanKey(t), HTTP: dm.client(),
	})
	require.Len(t, status, 1)
	require.Equal(t, LANTowerAnswering, status[0].State)
	require.Equal(t, 2, status[0].Bands)
	got := map[string]string{}
	for _, b := range bands {
		got[b.Model] = b.SourceLabel()
	}
	require.Equal(t, map[string]string{
		"gpt-oss-20b":   "edge: jetson",
		"llama-3.3-70b": "tower: 192.168.1.20",
		"qwen3-8b":      "this machine, tower: 192.168.1.20",
	}, got)
	// sorted by model, self first within a row
	require.Equal(t, "gpt-oss-20b", bands[0].Model)
}

func TestLANDiscoverEdgeRules(t *testing.T) {
	bands, _ := LANDiscover(context.Background(), LANInputs{Edge: []LANEdgeBand{
		{Node: "bench", Band: "claimed", Verified: false, LANDirect: true},
		{Node: "dark", Band: "darkband", Verified: true, LANDirect: true, Dark: true},
		{Node: "shed", Band: "relayed", Verified: true, LANDirect: false},
		{Node: "jetson", Band: "ok", Verified: true, LANDirect: true},
	}})
	require.Len(t, bands, 1)
	require.Equal(t, "ok", bands[0].Model)
	require.True(t, bands[0].Sources[0].Verified)
}

func TestLANDiscoverTowerStates(t *testing.T) {
	refusing := fakeTowerDiscover(t, nil, false)
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer html.Close()
	var many []Offer
	for i := 0; i < 600; i++ {
		many = append(many, Offer{Model: fmt.Sprintf("m%03d", i), Online: true})
	}
	big := fakeTowerDiscover(t, many, true)
	dm := dialMap{
		"192.168.1.21:8787": refusing.Listener.Addr().String(),
		"192.168.1.22:8787": html.Listener.Addr().String(),
		"192.168.1.23:8787": big.Listener.Addr().String(),
	}
	resolve := func(_ context.Context, host string) ([]net.IP, error) {
		if host == "flip.lan" {
			return []net.IP{net.ParseIP("203.0.113.7")}, nil
		}
		return nil, fmt.Errorf("nxdomain")
	}
	bands, status := LANDiscover(context.Background(), LANInputs{
		Towers: []string{"192.168.1.21:8787", "192.168.1.22:8787", "192.168.1.23:8787", "192.168.1.24:8787", "flip.lan:8787", "203.0.113.7:8787"},
		Key:    lanKey(t), HTTP: dm.client(), Resolve: resolve, MaxBands: 500,
	})
	require.Len(t, status, 6)
	require.Equal(t, LANTowerNotAdmitted, status[0].State)
	require.Contains(t, status[0].Detail, "roger-tower admit --client")
	require.Equal(t, LANTowerBadAnswer, status[1].State)
	require.Equal(t, LANTowerAnswering, status[2].State)
	require.True(t, status[2].More)
	require.Equal(t, 500, status[2].Bands)
	require.Equal(t, LANTowerNotAnswering, status[3].State)
	require.Equal(t, LANTowerRefused, status[4].State)
	require.Contains(t, status[4].Detail, "resolves to a public address")
	require.Equal(t, LANTowerRefused, status[5].State)
	require.Len(t, bands, 500)
}

func TestLANDiscoverDedupesTwoSpellingsOfOneTower(t *testing.T) {
	var hits atomic.Int32
	tw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": []Offer{{Model: "x", Online: true}}})
	}))
	defer tw.Close()
	dm := dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}
	resolve := func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.20")}, nil }
	bands, status := LANDiscover(context.Background(), LANInputs{
		Towers: []string{"tower.lan:8787", "192.168.1.20:8787"}, Key: lanKey(t), HTTP: dm.client(), Resolve: resolve,
	})
	require.Len(t, bands, 1)
	require.Len(t, bands[0].Sources, 1)
	require.Equal(t, int32(1), hits.Load())
	require.Equal(t, LANTowerSameAs, status[1].State)
	require.Contains(t, status[1].Detail, "tower.lan:8787")
}

func TestLANDiscoverSanitizesHostileNames(t *testing.T) {
	long := strings.Repeat("a", 100000) + "\x1b[31m"
	tw := fakeTowerDiscover(t, []Offer{{Model: long, Online: true}, {Model: "ok", Online: true}}, true)
	dm := dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}
	bands, _ := LANDiscover(context.Background(), LANInputs{Towers: []string{"192.168.1.20:8787"}, Key: lanKey(t), HTTP: dm.client()})
	require.Len(t, bands, 2)
	for _, b := range bands {
		require.LessOrEqual(t, len(b.Model), lanMaxModelName)
		require.NotContains(t, b.Model, "\x1b")
	}
}

func TestLANDiscoverDoesNotWaitForASlowTower(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) }))
	defer slow.Close()
	dm := dialMap{"192.168.1.25:8787": slow.Listener.Addr().String()}
	start := time.Now()
	bands, status := LANDiscover(context.Background(), LANInputs{
		Self: []string{"qwen3-8b"}, Towers: []string{"192.168.1.25:8787"}, Key: lanKey(t),
		HTTP: dm.client(), Timeout: 200 * time.Millisecond,
	})
	require.Less(t, time.Since(start), 2*time.Second)
	require.Len(t, bands, 1)
	require.Equal(t, LANTowerWaiting, status[0].State)
}
