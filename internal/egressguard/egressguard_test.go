package egressguard

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func listen(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestDialContextLiterals(t *testing.T) {
	var log bytes.Buffer
	uninstall := Install(&log)
	defer uninstall()
	ctx := context.Background()

	c, err := DialContext(ctx, "tcp", listen(t))
	require.NoError(t, err, "loopback is local")
	c.Close()

	for _, addr := range []string{"8.8.8.8:53", "100.64.0.9:80", "[2001:db8::1]:443", "[::ffff:8.8.8.8]:53"} {
		_, err := DialContext(ctx, "tcp", addr)
		require.ErrorIs(t, err, ErrRefused, addr)
		require.Contains(t, log.String(), addr)
	}
	_, err = DialContext(ctx, "tcp", "no-port")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrRefused))
}

func TestDialContextNames(t *testing.T) {
	uninstall := Install(nil)
	defer uninstall()
	defer SetResolver(nil)
	ctx := context.Background()
	local := listen(t)
	_, port, _ := net.SplitHostPort(local)

	SetResolver(func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil })
	c, err := DialContext(ctx, "tcp", net.JoinHostPort("tower.lan", port))
	require.NoError(t, err, "a name resolving locally is dialed at the resolved address")
	c.Close()

	SetResolver(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("93.184.216.34")}, nil
	})
	_, err = DialContext(ctx, "tcp", "mixed.example:443")
	require.ErrorIs(t, err, ErrRefused, "one public answer refuses the name")
	require.Contains(t, err.Error(), "93.184.216.34")

	SetResolver(func(context.Context, string) ([]net.IP, error) { return nil, errors.New("nxdomain") })
	_, err = DialContext(ctx, "tcp", "gone.example:443")
	require.ErrorContains(t, err, "nxdomain")

	SetResolver(func(context.Context, string) ([]net.IP, error) { return nil, nil })
	_, err = DialContext(ctx, "tcp", "empty.example:443")
	require.ErrorContains(t, err, "resolves to no address")
}

func TestInstallReplacesAndRestoresTheDefaults(t *testing.T) {
	prevT, prevR := http.DefaultTransport, net.DefaultResolver
	var log bytes.Buffer
	uninstall := Install(&log)
	require.NotSame(t, prevT, http.DefaultTransport)
	require.NotSame(t, prevR, net.DefaultResolver)
	_, err := http.Get("http://8.8.8.8/")
	require.ErrorIs(t, err, ErrRefused, "http.DefaultClient goes through the guard")
	uninstall()
	require.Same(t, prevT, http.DefaultTransport)
	require.Same(t, prevR, net.DefaultResolver)
}

func TestDefaultResolverIsRestorable(t *testing.T) {
	SetResolver(func(context.Context, string) ([]net.IP, error) { return nil, nil })
	SetResolver(nil)
	ips, err := resolve(context.Background(), "localhost")
	require.NoError(t, err)
	require.NotEmpty(t, ips)
}
