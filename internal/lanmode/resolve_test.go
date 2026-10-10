package lanmode

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixed(ips ...string) Resolver {
	return func(context.Context, string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func TestResolveDecidesWithOneLookup(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		addr    string
		resolve Resolver
		dial    string
		local   bool
	}{
		{"192.168.1.20:8787", nil, "192.168.1.20:8787", true},
		{"100.64.1.2:8787", nil, "100.64.1.2:8787", false}, // CGNAT: dialable only if hand-added
		{"8.8.8.8:8787", nil, "", false},
		{"lab.home:8787", fixed("192.168.1.20", "192.168.1.21"), "192.168.1.20:8787", true},
		{"lab.home:8787", fixed("192.168.1.20", "203.0.113.9"), "", false},
		{"lab.home:8787", fixed(), "", false},
		{"lab.home:8787", func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") }, "", false},
	} {
		a, err := ParseAddr(tc.addr)
		require.NoError(t, err)
		dial, v := Resolve(ctx, a, tc.resolve)
		require.Equal(t, tc.dial, dial, tc.addr)
		require.Equal(t, tc.local, v.Local, tc.addr)
	}
}
