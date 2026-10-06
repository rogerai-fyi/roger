package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/lanmode"
)

func TestCmdLANUsageAndRemove(t *testing.T) {
	useTempConfig(t)
	require.ErrorContains(t, cmdLAN(config{}, nil), "usage: roger lan")
	require.ErrorContains(t, cmdLAN(config{}, []string{"frob"}), `unknown lan command "frob"`)
	require.ErrorContains(t, cmdLAN(config{}, []string{"rm"}), "usage")

	c := loadConfig()
	c.LanTowers = []string{"192.168.1.20:8787", "192.168.1.40:8787"}
	require.NoError(t, saveConfig(c))

	out, err := captureEdgeStdout(func() error { return cmdLAN(config{}, []string{"rm", "192.168.1.99"}) })
	require.NoError(t, err)
	require.Contains(t, out, "not in the list")

	out, err = captureEdgeStdout(func() error { return cmdLAN(config{}, []string{"remove", "192.168.1.40"}) })
	require.NoError(t, err, "a bare host is the default port")
	require.Contains(t, out, "removed 192.168.1.40:8787")
	require.Equal(t, []string{"192.168.1.20:8787"}, loadConfig().LanTowers)
}

func TestLanListLines(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error { return cmdLAN(config{}, []string{"list"}) })
	require.NoError(t, err)
	require.Contains(t, out, "no LAN Towers")

	dial := &lanDialMap{}
	dial.set("192.168.1.20:8787", newRealTower(t).addr())
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer other.Close()
	dial.set("192.168.1.30:8787", other.Listener.Addr().String())
	lanProbeHTTP = dial.client()
	lanResolve = func(context.Context, string) ([]net.IP, error) { return nil, fmt.Errorf("nxdomain") }
	defer func() { lanProbeHTTP = &http.Client{}; lanResolve = lanmode.DefaultResolver }()

	c := loadConfig()
	c.LanTowers = []string{"192.168.1.20:8787", "192.168.1.30:8787", "192.168.1.40:8787", "203.0.113.7:8787", "gone.lan:8787", "[fe80::1]:8787"}
	require.NoError(t, saveConfig(c))
	out, err = captureEdgeStdout(func() error { return cmdLAN(config{}, []string{"ls"}) })
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 6)
	require.Contains(t, lines[0], "answering")
	require.Contains(t, lines[1], "bad answer")
	require.Contains(t, lines[2], "not answering")
	require.Contains(t, lines[3], "refused: a public address")
	require.Contains(t, lines[4], "refused: gone.lan does not resolve")
	require.Contains(t, lines[5], "refused: a link-local IPv6 address needs its interface")
}

func TestLanAddRefusesBadInput(t *testing.T) {
	useTempConfig(t)
	require.Error(t, lanAdd(nil))
	require.ErrorContains(t, lanAdd([]string{"0.0.0.0:8787"}), "unspecified")
	lanProbeHTTP = (&lanDialMap{}).client()
	defer func() { lanProbeHTTP = &http.Client{} }()
	require.ErrorContains(t, lanAdd([]string{"192.168.1.77:8787"}), "--offline")
}

func TestStatusDefaultsAndHelpers(t *testing.T) {
	out, err := captureEdgeStdout(func() error { return cmdStatus(config{Broker: "https://b", EdgePrefer: "market"}, nil) })
	require.NoError(t, err)
	require.Contains(t, out, "mode: default (from default)")
	require.Contains(t, out, "edge preference: market")
	out, err = captureEdgeStdout(func() error { return cmdStatus(config{}, []string{"--json"}) })
	require.NoError(t, err)
	require.Contains(t, out, `"lan_towers": []`)
	require.Contains(t, out, `"mode": "default"`)
}

func TestIsLocalTowerBrokerUsesTheSharedProbe(t *testing.T) {
	tw := newRealTower(t)
	require.True(t, isLocalTowerBroker("http://"+tw.addr()))
	_, port, _ := net.SplitHostPort(tw.addr())
	require.True(t, isLocalTowerBroker("http://localhost:"+port))
	require.False(t, isLocalTowerBroker("https://"+tw.addr()), "only plain http is a Tower's shape")
	require.False(t, isLocalTowerBroker("http://203.0.113.7:8787"), "a public address is never probed")
	require.False(t, isLocalTowerBroker("://bad"))
}
