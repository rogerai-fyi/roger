package client

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTargetsLANTowerEveryLocalAddress: the replay nonce goes to every address the LAN check
// calls local, and to Towers added by hand; never to loopback, https or a public host
// (features/lan/regressions.feature, 2026-10-07).
func TestTargetsLANTowerEveryLocalAddress(t *testing.T) {
	SetLANTowerHosts(func() []string { return []string{"100.64.10.20:8787", "tower.lan:8787"} })
	defer SetLANTowerHosts(nil)
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"http://192.168.1.20:8787/v1/chat/completions", true},
		{"http://10.0.0.5:8787/discover", true},
		{"http://[fd00::20]:8787/discover", true},
		{"http://169.254.10.20:8787/discover", true},
		{"http://[fe80::20%25eth0]:8787/discover", true},
		{"http://[::ffff:192.168.1.20]:8787/discover", true},
		{"http://100.64.10.20:8787/discover", true}, // CGNAT, added by hand
		{"http://tower.lan:8787/discover", true},    // a name, added by hand
		{"http://100.64.99.1:8787/discover", false}, // CGNAT, not added
		{"http://other.lan:8787/discover", false},   // a name, not added
		{"http://127.0.0.1:8787/discover", false},   // loopback never leaves the host
		{"http://[::1]:8787/discover", false},
		{"https://192.168.1.20:8787/discover", false}, // only plaintext http
		{"https://broker.rogerai.fm/discover", false},
		{"http://203.0.113.7:8787/discover", false}, // public
	} {
		u, err := url.Parse(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, targetsLANTower(u), tc.raw)
	}
	SetLANTowerHosts(nil)
	u, _ := url.Parse("http://100.64.10.20:8787/discover")
	require.False(t, targetsLANTower(u), "with no hand-added Towers a CGNAT address gets no nonce")
	require.False(t, targetsLANTower(nil))
}
