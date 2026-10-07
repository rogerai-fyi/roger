package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/agent"
)

// In LAN mode SHARE serves the LAN Towers as a standalone station and registers with nothing
// (features/lan/share_in_lan_mode.feature): a priced row goes on air free with no login, and a
// private band is refused.
func TestLANShareServesTheLANTowersAndRegistersNothing(t *testing.T) {
	var polls atomic.Int32
	tower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/poll" {
			polls.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tower.Close()
	real := startAgent
	t.Cleanup(func() { startAgent = real })
	startAgent = func(agent.Config) (*agent.Session, error) {
		t.Error("a LAN share registered with the broker")
		return nil, nil
	}

	c := newCtrl(t, Config{})
	c.SetLAN(func() []string { return []string{tower.URL} })

	res := c.ToggleOnAir("paid")
	require.NoError(t, res.Err)
	require.False(t, res.LoginNeeded, "LAN sharing is free: no login")
	require.False(t, res.Priced, "the saved price does not apply on the LAN")
	require.Eventually(t, func() bool { return polls.Load() > 0 }, 3*time.Second, 20*time.Millisecond)

	priv := c.TogglePrivate("free-1")
	require.Error(t, priv.Err)
	require.True(t, strings.Contains(priv.Err.Error(), "LAN mode"), priv.Err.Error())

	off := c.ToggleOnAir("paid")
	require.True(t, off.WentOff)
	n := polls.Load()
	time.Sleep(700 * time.Millisecond)
	require.LessOrEqual(t, polls.Load(), n+1, "off air: polling stops")
}
