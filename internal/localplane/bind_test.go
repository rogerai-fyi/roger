package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The exposure posture: the consumer plane defaults to loopback and refuses to masquerade on
// a public or all-interfaces address without an explicit acknowledged override. An unbound
// standalone Tower on a routable address is a broker lookalike for phishing ROGER_BROKER, and
// admission does not stop that - a bind refusal does.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBindPosture(t *testing.T) {
	cases := []struct {
		name        string
		addr        string
		allowPublic bool
		wantErr     string // substring; "" means allowed
		noteHas     string
	}{
		{"empty defaults to loopback", "", false, "", "loopback"},
		{"explicit loopback", "127.0.0.1:9000", false, "", "loopback"},
		{"ipv6 loopback", "[::1]:9000", false, "", "loopback"},
		{"private LAN", "192.168.1.10:9000", false, "", "private-LAN"},
		{"private 10.x", "10.2.3.4:8787", false, "", "private-LAN"},
		{"all interfaces refused", "0.0.0.0:8787", false, "ALL interfaces", ""},
		{"all interfaces with override", "0.0.0.0:8787", true, "", "ALL interfaces"},
		{"public refused", "203.0.113.5:8787", false, "PUBLIC", ""},
		{"public with override", "203.0.113.5:8787", true, "", "PUBLIC"},
		{"hostname refused", "roggentoo:8787", false, "literal IP", ""},
		{"garbage refused", "not-an-addr", false, "host:port", ""},
		{"bad port refused", "127.0.0.1:not-a-port", false, "invalid port", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bind, note, err := ResolveBind(tc.addr, tc.allowPublic)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.Empty(t, bind)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, bind)
			require.Contains(t, note, tc.noteHas)
		})
	}
}

func TestDefaultBindIsLoopback(t *testing.T) {
	require.True(t, strings.HasPrefix(DefaultBind, "127.0.0.1:"), "the default must be loopback")
}
