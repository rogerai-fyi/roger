package main

// features/tower/approval_visibility.feature, CLI half: the operator can always tell
// where they stand, and the advertised endpoint is honest.

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"context"
	"fmt"
	"github.com/stretchr/testify/require"
)

func TestAnnounceStateSpeaksOperator(t *testing.T) {
	var out bytes.Buffer
	last := ""

	announceState(&out, &last, "quarantine")
	first := out.String()
	require.Contains(t, first, "pending approval", "the waiting room says it is one")
	require.Contains(t, first, "automatic", "and that the flip needs no action here")
	require.Contains(t, first, "https://rogerai.fm/tower", "and where to learn more")
	require.Contains(t, first, "Nothing is broken", "it must not read as a fault")

	// The same state again is silence - a once-a-minute heartbeat must not wallpaper the
	// terminal with the banner.
	out.Reset()
	announceState(&out, &last, "quarantine")
	require.Empty(t, out.String())

	// The approval announces itself the moment it lands.
	announceState(&out, &last, "active")
	require.Contains(t, out.String(), "approved and ready to carry traffic")

	// A state this binary has never heard of is shown verbatim, never hidden: an old CLI
	// meeting a new Core should say something true.
	out.Reset()
	announceState(&out, &last, "some-future-state")
	require.Contains(t, out.String(), "some-future-state")

	// The empty state (an old Core that does not say) prints nothing at all.
	out.Reset()
	announceState(&out, &last, "")
	require.Empty(t, out.String())
}

func TestResolveAdvertisedIsHonest(t *testing.T) {
	// Names resolve through a seam, so these tests touch no DNS.
	restore := lookupIPFn
	defer func() { lookupIPFn = restore }()
	fakeDNS := map[string][]net.IP{
		"hub.example.net": {net.ParseIP("93.184.216.34")},
		"roggentoo":       {net.ParseIP("192.168.1.40")},
		"loopname":        {net.ParseIP("127.0.0.1")},
	}
	lookupIPFn = func(_ context.Context, _ string, host string) ([]net.IP, error) {
		if ips, ok := fakeDNS[host]; ok {
			return ips, nil
		}
		return nil, fmt.Errorf("no such host")
	}

	// A public name passes through, silently: nothing needs saying.
	addr, note, err := resolveAdvertised("hub.example.net:8444")
	require.NoError(t, err)
	require.Equal(t, "hub.example.net:8444", addr)
	require.Empty(t, note)

	// A LAN name - the home-lab tier - is named for what it is, with the one trap called
	// out: the name must resolve on every device that will dial it.
	addr, note, err = resolveAdvertised("roggentoo:8444")
	require.NoError(t, err)
	require.Equal(t, "roggentoo:8444", addr, "the NAME stays advertised; the note shows the IP")
	require.Contains(t, note, "LOCAL network")
	require.Contains(t, note, "192.168.1.40")
	require.Contains(t, note, "hosts file", "the hosts-file trap is the one that bites")

	// A name that points home is the loopback tier, whatever it calls itself.
	_, note, err = resolveAdvertised("loopname:8444")
	require.NoError(t, err)
	require.Contains(t, note, "only THIS machine")

	// A name nothing can resolve is refused with the repair, not advertised broken.
	_, _, err = resolveAdvertised("no-such-name:8444")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not resolve")
	require.Contains(t, err.Error(), "IP address")

	// 0.0.0.0 / :: are BIND wildcards, not reachable addresses - the --hub argument
	// mistaken for --relay-public. Both mean "this machine", so they resolve to the
	// outbound address exactly like the empty host, with a note that says why.
	for _, wildcard := range []string{"0.0.0.0:8444", "[::]:8444"} {
		addr, note, err := resolveAdvertised(wildcard)
		require.NoError(t, err, wildcard)
		host, port, serr := net.SplitHostPort(addr)
		require.NoError(t, serr)
		require.Equal(t, "8444", port)
		require.False(t, net.ParseIP(host).IsUnspecified(), "%s must not stay a wildcard", wildcard)
		require.False(t, net.ParseIP(host).IsLoopback())
		require.Contains(t, note, "bind wildcard", "the note must name the mistake")
		require.Contains(t, note, host, "and show the address it chose instead")
	}

	// Loopback is accepted and named for what it is.
	addr, note, err = resolveAdvertised("127.0.0.1:8444")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:8444", addr)
	require.Contains(t, note, "only THIS machine")

	// An empty host resolves to this machine's own address and SAYS which - the literal
	// ":8444" made every node dial itself.
	addr, note, err = resolveAdvertised(":8444")
	if err != nil {
		t.Skipf("no outbound address on this machine: %v", err)
	}
	host, port, serr := net.SplitHostPort(addr)
	require.NoError(t, serr)
	require.Equal(t, "8444", port)
	require.NotEmpty(t, host)
	require.False(t, net.ParseIP(host).IsLoopback(), "the resolved address must be reachable by others")
	require.Contains(t, note, host, "the choice is printed so a wrong guess is visible")

	// Garbage is a flag error, not a mystery later.
	_, _, err = resolveAdvertised("not an address")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "relay-public"))
}

func TestAnnounceStateNamesEveryLifecycleState(t *testing.T) {
	for state, must := range map[string]string{
		"draining":  "no new work",
		"suspended": "pending review",
		"revoked":   "permanently",
	} {
		var out bytes.Buffer
		last := ""
		announceState(&out, &last, state)
		require.Contains(t, out.String(), must, state)
	}
}

func TestOutboundIPAnswersOrSaysWhy(t *testing.T) {
	ip, err := outboundIP()
	if err != nil {
		t.Skipf("no outbound route here: %v", err)
	}
	parsed := net.ParseIP(ip)
	require.NotNil(t, parsed)
	require.False(t, parsed.IsUnspecified())
}

// cmdServe's flag screen: honest errors before the data directory is ever touched, and
// the loopback note printed on the way through.
func TestServeFlagScreenIsHonest(t *testing.T) {
	var out bytes.Buffer
	err := cmdServe([]string{"--dir", t.TempDir(), "--relay-public", "not an address"}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "relay-public")

	out.Reset()
	err = cmdServe([]string{"--dir", t.TempDir(), "--relay-public", "127.0.0.1:8444"}, &out)
	require.Error(t, err, "a plane advertised with no hub serving one is refused")
	require.Contains(t, err.Error(), "no --hub")
	require.Contains(t, out.String(), "only THIS machine",
		"the loopback note prints even when a later flag check refuses")
}
