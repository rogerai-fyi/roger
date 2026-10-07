package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

// TestLANDialFitsEveryWidth: the source column never pushes a LAN dial row past the terminal,
// at narrow, wide and windowshade widths, and each row still names its source where it fits.
func TestLANDialFitsEveryWidth(t *testing.T) {
	s := &lanUIBDD{t: t}
	s.reset()
	s.bands = append(s.bands, client.LANBand{Model: "deepseek-v4-flash-long-name",
		Sources: []client.LANSource{{Kind: client.LANTower, ID: "192.168.100.200:8787", Addr: "192.168.100.200:8787"}}})
	defer SetLANDial(nil)
	for _, w := range []int{52, 60, 63, 64, 80, 100} {
		for _, compact := range []bool{false, true} {
			s.seed(w, "http://broker.invalid")
			s.m.compact = compact
			out := stripANSI(s.m.browseView(w))
			for _, ln := range strings.Split(out, "\n") {
				require.LessOrEqual(t, lipgloss.Width(ln), w, "w=%d compact=%v overflows: %q", w, compact, ln)
			}
			if !compact && w >= 64 {
				require.Contains(t, lanRow(out, "qwen3-8b"), "this machine", "w=%d", w)
			}
		}
	}
}

// TestLANDialNotesAnUnreadEdgeUnderItsBands: a dial with bands still says the Edge could not be
// read, so a missing member band never looks like a member serving nothing.
func TestLANDialNotesAnUnreadEdgeUnderItsBands(t *testing.T) {
	s := &lanUIBDD{t: t}
	s.reset()
	s.edgeUnread = "open edge.json: permission denied"
	defer SetLANDial(nil)
	s.seed(100, "http://broker.invalid")
	out := stripANSI(s.m.browseView(100))
	require.Contains(t, out, "the Edge could not be read: open edge.json: permission denied")
	require.NotEmpty(t, lanRow(out, "qwen3-8b"))
}
