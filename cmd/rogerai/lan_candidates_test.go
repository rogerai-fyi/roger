package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge/wire"
)

// It passed on a developer machine only because that machine is on an Edge, read from the real
// config: CI is not, so the LAN browse gate (edgeMayBrowse) said no and nothing was heard. The test
// now has its own empty config, and holds the LAN dial's scan scope open as the TUI does; and with no
// scope open, on no Edge, nothing is browsed at all.
func TestLANCandidatesFilterAndSummarise(t *testing.T) {
	useTempConfig(t)
	prev := lanBrowseTowers
	t.Cleanup(func() { lanBrowseTowers = prev })
	browsed := false
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) { browsed = true; return wire.BrowseResult{}, nil }
	require.Nil(t, lanCandidates(context.Background(), nil))
	require.False(t, browsed, "no LAN view open and no Edge: no browse, no socket")
	end := edgeScans.Begin(scanLAN) // what the TUI's hook does while the LAN dial shows
	t.Cleanup(end)
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) {
		return wire.BrowseResult{Seen: 7, Adverts: []wire.Advert{
			{Kind: wire.TowerKind, IP: net.ParseIP("192.168.1.50"), Port: 8787, Name: "gar\x1bage" + strings.Repeat("x", 60)},
			{Kind: wire.TowerKind, IP: net.ParseIP("192.168.1.20"), Port: 8787, Name: "known"},
			{Kind: "edge", IP: net.ParseIP("192.168.1.30"), Port: 8787},
			{Kind: wire.TowerKind, IP: nil, Port: 8787},
			{Kind: wire.TowerKind, IP: net.ParseIP("192.168.1.31"), Port: 0},
		}}, nil
	}
	got := lanCandidates(context.Background(), []string{"192.168.1.20:8787"})
	require.Len(t, got, 2)
	require.Equal(t, "192.168.1.50:8787", got[0].Addr)
	require.NotContains(t, got[0].Detail, "\x1b", "a LAN device's label never carries a control character")
	require.LessOrEqual(t, len(got[0].Detail), 120)
	require.Equal(t, "", got[1].Addr)
	require.Contains(t, got[1].Detail, "2 more Tower advertisement(s) heard")
	require.Equal(t, got[1].Detail, lanStatusText(got[1]), "a summary line prints as its detail alone")

	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) { return wire.BrowseResult{}, errors.New("no LAN") }
	require.Nil(t, lanCandidates(context.Background(), nil))
	require.Contains(t, lanEmptyText([]client.LANTowerStatus{{State: client.LANTowerCandidate, Addr: "192.168.1.50:8787"}}),
		"and 0 Tower(s)", "a candidate was heard, not checked")
}

// TestDefaultLANBrowseTowersReturns: the real browse runs and returns (a cancelled context ends it
// at once), whatever this machine's network is.
func TestDefaultLANBrowseTowersReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, _ := defaultLANBrowseTowers(ctx)
	require.LessOrEqual(t, len(res.Adverts), lanCandidateCap)
}

// TestTUIHooksAddTowerLikeLanAdd: in LAN mode the TUI's add-from-the-dial hook is `roger lan add`,
// with its lines returned instead of printed (a public address is refused the same way).
func TestTUIHooksAddTowerLikeLanAdd(t *testing.T) {
	useTempConfig(t)
	cfg := loadConfig()
	cfg.lan.LAN = true
	h := tuiHooks(cfg)
	require.NotNil(t, h.LANAddTower)
	_, err := h.LANAddTower("203.0.113.7:8787")
	require.ErrorContains(t, err, "not added")
	require.Empty(t, loadConfig().LanTowers)
}
