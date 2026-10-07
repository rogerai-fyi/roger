package client

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUseNeverPrintsAPortItDoesNotHold: the endpoint plate names a port the session already
// holds. A port someone else holds is refused before the plate, never printed and then lost
// at the bind (the race a busy machine hit between picking a port and binding it).
func TestUseNeverPrintsAPortItDoesNotHold(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	squat, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer squat.Close()
	port := squat.Addr().(*net.TCPAddr).Port
	b := fakeBroker(t)
	var useErr error
	out := captureOut(t, func() { useErr = Use(b, "u_gh_1", "m1", UseOptions{Yes: true, MaxOut: 5, Port: port}) })
	require.Error(t, useErr, "the port is taken")
	require.NotContains(t, out, fmt.Sprintf("127.0.0.1:%d", port), "the plate never names a port it does not hold")
	require.False(t, strings.Contains(out, "CHANNEL OPEN"), "no channel is announced without a listener")
}

// TestUseAutoPortServesOnThePortItPrinted: with no --port, Use binds the first free port from
// the default (here the default is taken), prints that port, and serves on that same listener.
func TestUseAutoPortServesOnThePortItPrinted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if squat, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", DefaultUsePort)); err == nil {
		defer squat.Close() // the default is taken (if it was free, take it now)
	}
	old := useServe
	var served string
	useServe = func(ln net.Listener, _ http.Handler) error {
		served = ln.Addr().String()
		return ln.Close()
	}
	t.Cleanup(func() { useServe = old })
	b := fakeBroker(t)
	var useErr error
	out := captureOut(t, func() { useErr = Use(b, "u_gh_1", "m1", UseOptions{Yes: true, MaxOut: 5}) })
	require.NoError(t, useErr)
	require.NotEmpty(t, served)
	require.NotEqual(t, fmt.Sprintf("127.0.0.1:%d", DefaultUsePort), served, "the taken default is skipped")
	require.Contains(t, out, "http://"+served+"/v1", "the plate names the port the relay serves on")
}
