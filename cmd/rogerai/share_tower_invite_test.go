package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// features/join/invite.feature: `roger share --tower <invite>` is the same join for a station,
// then the share. A bare --tower keeps its explanation; a client invite is refused.
func TestShareTowerInviteJoinsThenShares(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lanmode.SetPins(nil)
	st, err := tower.Init(t.TempDir()+"/lab", tower.ModeStandalone)
	require.NoError(t, err)
	key, _ := st.TLSKey()
	cfg, _ := towerpin.ServerTLS(key)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	fp, _ := st.JoinFingerprint()
	mint := func(kind tower.JoinKind, jk joininvite.Kind) string {
		_, code, err := st.CreateJoinInvitation(kind, "", nil, time.Minute)
		require.NoError(t, err)
		return joininvite.Invite{Kind: jk, Addrs: []string{ln.Addr().String()}, FP: fp, Code: code}.String()
	}
	prev := detectFull
	detectFull = fakeDetect("qwen3-8b")
	t.Cleanup(func() { detectFull = prev })
	fixtureIsRogerServed(t) // the fixture's model is one roger serves, which a station offers

	// A bare --tower is left for the explanation.
	c := config{}
	rest, err := shareTowerInvite(&c, []string{"--tower"})
	require.NoError(t, err)
	require.Equal(t, []string{"--tower"}, rest)
	require.False(t, c.lan.LAN)

	// A client invite is not a station invite.
	_, err = shareTowerInvite(&c, []string{"--tower", mint(tower.JoinClient, joininvite.TowerClient)})
	require.ErrorContains(t, err, "station invite")

	// A station invite joins (exactly as roger join), then the share runs on the LAN.
	out := captureStdout(t, func() {
		rest, err = shareTowerInvite(&c, []string{"qwen3-8b", "--tower=" + mint(tower.JoinStation, joininvite.TowerStation)})
	})
	require.NoError(t, err, out)
	require.Equal(t, []string{"qwen3-8b"}, rest)
	require.True(t, c.lan.LAN, "the share serves the LAN")
	require.Contains(t, out, "as a station")
	require.Contains(t, loadConfig().LanTowers, ln.Addr().String())
	stations, _ := st.Stations()
	require.Len(t, stations, 1)
}
