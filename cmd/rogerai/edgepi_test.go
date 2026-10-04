package main

// Found rehearsing the Pi join (2026-10-02): the steps a person follows must print what they need.

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
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

// Found with tools/edgesim (2026-10-04): a machine enrolled as "pi-1" was listed as "Pi 5 39f2" -
// discovery rightly never trusts the name a device announces, but the name in an enrollment this
// authority accepted is the owner's own choice (enrollment.feature). It is the name the member is
// first seen under, unless another member already holds it.
func TestAMemberIsFirstSeenUnderTheNameItEnrolledAs(t *testing.T) {
	newFacedAuthority(t, "desk")
	local, isAuth, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, isAuth)
	enroll := func(name string) string {
		userPub, userPriv, _ := ed25519.GenerateKey(nil)
		nodePub, _, _ := ed25519.GenerateKey(nil)
		require.NoError(t, local.Allow(hex.EncodeToString(userPub)))
		r := edgeauth.NewRequest(edgeauth.LocalAccount, name, "host", nodePub, time.Now())
		r.Sign(userPriv)
		resp, err := local.Issue(r)
		require.NoError(t, err)
		return resp.NodeID
	}
	pi := enroll("pi")
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, err := st.fleet.Observe(pi, edge.Observation{Name: pi, Kind: "host", Addr: "192.168.1.50:44100", Fingerprint: "ab"})
	require.NoError(t, err)
	require.Equal(t, "pi", n.Name, "seen first under the name it enrolled as")

	// A second machine enrolled as "desk" (this authority's own name) keeps its id-name: one name, one node.
	dup := enroll("desk")
	n, err = st.fleet.Observe(dup, edge.Observation{Name: dup, Kind: "host", Addr: "192.168.1.51:44100", Fingerprint: "cd"})
	require.NoError(t, err)
	require.NotEqual(t, "desk", n.Name)
}
