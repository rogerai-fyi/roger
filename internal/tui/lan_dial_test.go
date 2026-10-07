package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
)

// A standalone Tower's /discover refuses an unsigned read; the dial retries once signed, as
// client.discover does, instead of drawing the Tower as "0 on air" (features/lan/regressions.feature).
func TestFetchOffersRetriesSignedOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(protocol.HeaderSig) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": []offer{{Model: "llama-3.3-70b", Online: true}}})
	}))
	defer srv.Close()
	msg := fetchOffers(srv.URL)()
	got, ok := msg.(offersMsg)
	require.True(t, ok, "got %#v", msg)
	require.Len(t, got, 1)
	require.Equal(t, "llama-3.3-70b", got[0].Model)
}

// In LAN mode the dial is the shared LAN dial: one row per band, free, naming its source, and
// the broker is never asked.
func TestFetchOffersInLANModeReadsTheLANDial(t *testing.T) {
	asked := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked = true }))
	defer broker.Close()
	SetLANDial(func() client.LANDial {
		return client.LANDial{Bands: []client.LANBand{
			{Model: "qwen3-8b", Sources: []client.LANSource{{Kind: client.LANSelf, Verified: true}}},
			{Model: "llama-3.3-70b", Sources: []client.LANSource{{Kind: client.LANTower, ID: "192.168.1.20:8787"}}},
		}}
	})
	defer SetLANDial(nil)
	got, ok := fetchOffers(broker.URL)().(offersMsg)
	require.True(t, ok)
	require.False(t, asked, "the broker was asked in LAN mode")
	require.Len(t, got, 2)
	for _, o := range got {
		require.True(t, o.Online)
		require.True(t, o.FreeNow)
		require.Zero(t, o.PriceIn)
		require.Zero(t, o.PriceOut)
		require.NotEmpty(t, o.LANSource)
	}
	require.Equal(t, "tower: 192.168.1.20", got[0].LANSource)
	require.True(t, LANMode())
}

// NewController hands the LAN Towers to the controller: SHARE is then in LAN mode.
func TestNewControllerPutsShareInLANMode(t *testing.T) {
	ctrl := NewController("http://127.0.0.1:1", Hooks{LANTowers: func() []string { return nil }})
	ctrl.SetRows([]node.ShareRow{{Model: "m", Upstream: "http://127.0.0.1:1/v1"}})
	res := ctrl.TogglePrivate("m")
	if res.Err == nil || !strings.Contains(res.Err.Error(), "LAN mode") {
		t.Fatalf("a private band in LAN mode: %+v", res)
	}
}
