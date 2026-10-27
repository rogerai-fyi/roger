package client

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/lanmode"
)

// TestTargetsLANTowerEveryLocalAddress: the replay nonce goes to every address the LAN check
// calls local, and to every known standalone Tower by host and port, loopback included; never
// to an unknown loopback service, https or a public host
// (features/lan/regressions.feature, 2026-10-07).
func TestTargetsLANTowerEveryLocalAddress(t *testing.T) {
	SetLANTowerHosts(func() []string {
		return []string{"100.64.10.20:8787", "tower.lan:8787", "127.0.0.1:8787", "[::1]:8787", "http://127.0.0.2:9000"}
	})
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
		{"http://127.0.0.1:8787/discover", true},    // a known loopback Tower (ruled 2026-10-07)
		{"http://[::1]:8787/discover", true},
		{"http://127.0.0.2:9000/discover", true},      // known by its broker URL
		{"http://127.0.0.1:9999/discover", false},     // loopback that is not a known Tower
		{"http://100.64.10.20:9999/discover", false},  // a known host on another port
		{"http://127.0.0.1/discover", false},          // port 80 is not the known :8787
		{"https://192.168.1.20:8787/discover", false}, // only plaintext http
		{"https://broker.rogerai.fm/discover", false},
		{"http://203.0.113.7:8787/discover", false}, // public
	} {
		u, err := url.Parse(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, lanmode.WantsNonce(u, lanTowerHostList()), tc.raw)
	}
	SetLANTowerHosts(nil)
	u, _ := url.Parse("http://100.64.10.20:8787/discover")
	require.False(t, lanmode.WantsNonce(u, lanTowerHostList()), "with no hand-added Towers a CGNAT address gets no nonce")
	require.False(t, lanmode.WantsNonce(nil, lanTowerHostList()))
}
