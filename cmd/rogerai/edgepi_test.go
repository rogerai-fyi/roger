package main

// Found rehearsing the Pi join (2026-10-02): the steps a person follows must print what they need.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// Every help line keeps its usage and its purpose apart: a long usage puts the purpose on its own
// line rather than running into it ("[--health]open the live fleet screen").
func TestEdgeHelpNeverRunsAUsageIntoItsPurpose(t *testing.T) {
	u := edgeUsage()
	for _, c := range edgeSubcommands {
		use := strings.TrimSpace("roger edge " + c.name + " " + c.args)
		require.NotContains(t, u, use+c.purpose, "%s runs into its purpose", c.name)
		require.Contains(t, u, c.purpose)
	}
	require.Contains(t, u, "roger edge fleet [--select <node>] [--health]\n")
}

// A machine nobody named is listed by its hardware word, never its id (node_identity.feature).
func TestTheFleetListNamesAnUnnamedMachine(t *testing.T) {
	offline(t)
	edgeSeed(t, store.EdgeNode{ID: "n_ebab8e24", Name: "n_ebab8e24", Kind: "host"})
	out, code := edgeRun(t, "edge", "list")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "n_ebab", "never its id")
	require.Contains(t, out, "machine")
}

// Allowing a machine prints the address to type on it - this machine's LAN address and the port the
// authority answers on - not a placeholder.
func TestAllowingAMachineSaysTheAddressToTypeOnIt(t *testing.T) {
	offline(t)
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	require.NoError(t, os.WriteFile(filepath.Join(edgeAuthDir(), "authority-port"), []byte("39009\n"), 0o600))
	old := edgeLANIPs
	t.Cleanup(func() { edgeLANIPs = old })
	edgeLANIPs = func() []string { return []string{"192.168.1.20", "10.0.0.5"} }
	out, code := edgeRun(t, "edge", "authority", "allow", strings.Repeat("aa", 32))
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "roger edge enroll <name> --authority http://192.168.1.20:39009")
	require.Contains(t, out, "http://10.0.0.5:39009")
	require.NotContains(t, out, "<this machine's LAN address>")

	// Before the authority has ever answered, there is no port yet, and it says how to get one.
	require.NoError(t, os.Remove(filepath.Join(edgeAuthDir(), "authority-port")))
	out, _ = edgeRun(t, "edge", "authority", "allow", strings.Repeat("bb", 32))
	require.Contains(t, out, "start roger on this machine")
}

// An enroll the authority refuses because it does not know this machine says how to fix it, as the
// setup wizard does: the exact allow command, with this machine's key.
func TestAnEnrollThatIsNotAllowedSaysHowToAllowIt(t *testing.T) {
	err := edgeEnrollErr(fmt.Errorf("enroll: %w", edgeauth.ErrUnknownMachine), "pi", "http://192.168.1.20:39009")
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	require.Contains(t, err.Error(), "roger edge authority allow "+client.UserPubHex())
	require.Contains(t, err.Error(), "roger edge enroll pi --authority http://192.168.1.20:39009")
	other := fmt.Errorf("connection refused")
	require.Equal(t, other, edgeEnrollErr(other, "pi", ""), "any other error is left alone")
}
