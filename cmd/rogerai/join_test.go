package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
)

const joinTestFP = "9f3c0000000000000000000000000000000000000000000000000000000000e1"

// features/join/invite.feature: `roger join` refuses a malformed invite before anything is sent.
func TestJoinRefusesAMalformedInvite(t *testing.T) {
	for _, args := range [][]string{nil, {"https://example.com"}, {"roger://join?kind=toaster"}} {
		err := cmdJoin(config{}, args)
		require.Error(t, err, "%v", args)
		var ue *usageError
		require.ErrorAs(t, err, &ue, "%v", args)
	}
}

// An invite whose addresses are not local is refused before any connection.
func TestJoinRefusesAnInviteWithNoLocalAddress(t *testing.T) {
	inv := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{"203.0.113.9:8787"}, FP: joinTestFP, Code: joincode.Tower.NewCode()}
	err := cmdJoin(config{}, []string{inv.String()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "203.0.113.9:8787")
	require.Contains(t, err.Error(), "not a local address")
}

// The Edge allow output and the edge invite parser use the one scheme, and an old Edge invite
// still reads.
func TestEdgeInvitesUseTheOneScheme(t *testing.T) {
	code := joincode.Edge.NewCode()
	s := edgeInvite("192.168.1.69:39009", "sha256:"+joinTestFP, code)
	require.True(t, strings.HasPrefix(s, "roger://join?kind=edge"), s)
	ep, pin, got, err := edgeParseInvite(s)
	require.NoError(t, err)
	require.Equal(t, "http://192.168.1.69:39009", ep)
	require.Equal(t, joinTestFP, pin)
	require.Equal(t, code, got)
	_, _, _, err = edgeParseInvite("rogerai-edge://join?at=192.168.1.69%3A39009&root=sha256%3A" + joinTestFP + "&code=" + code)
	require.NoError(t, err, "an Edge invite from before this change still reads")
	tower := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: joinTestFP, Code: joincode.Tower.NewCode()}
	_, _, _, err = edgeParseInvite(tower.String())
	require.ErrorContains(t, err, "roger join")
}

// `roger lan join <invite>` is an alias of `roger join <invite>`.
func TestLANJoinIsAnAlias(t *testing.T) {
	err := cmdLAN(config{}, []string{"join", "roger://join?kind=toaster"})
	require.ErrorContains(t, err, "unknown kind")
}
