package joininvite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
)

const fp = "9f3c0000000000000000000000000000000000000000000000000000000000e1"

// features/join/invite.feature: every invite kind uses one scheme and parses back unchanged.
func TestEveryKindRoundTripsThroughOneScheme(t *testing.T) {
	for _, k := range []Kind{TowerClient, TowerStation, Edge} {
		code := joincode.Tower.NewCode()
		if k == Edge {
			code = joincode.Edge.NewCode()
		}
		in := Invite{Kind: k, Addrs: []string{"192.168.1.20:8787", "[fe80::1%en0]:8787"}, FP: fp, Code: code}
		s := in.String()
		require.True(t, strings.HasPrefix(s, "roger://join?kind="+string(k)), s)
		got, err := Parse(s)
		require.NoError(t, err)
		require.Equal(t, in, got)
	}
}

func TestANamedInviteCarriesItsName(t *testing.T) {
	in := Invite{Kind: TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: fp, Code: joincode.Tower.NewCode(), For: "laptop-x"}
	got, err := Parse(in.String())
	require.NoError(t, err)
	require.Equal(t, "laptop-x", got.For)
}

// An Edge invite from before this change is still accepted, as kind edge.
func TestTheOldEdgeSchemeIsStillRead(t *testing.T) {
	old := "rogerai-edge://join?at=192.168.1.69%3A39009&code=ABCDEFGHJK&root=sha256%3A" + fp
	got, err := Parse(old)
	require.NoError(t, err)
	require.Equal(t, Invite{Kind: Edge, Addrs: []string{"192.168.1.69:39009"}, FP: fp, Code: "ABCDEFGHJK"}, got)
}

func TestTheFingerprintIsAlwaysTheFullOne(t *testing.T) {
	in := Invite{Kind: TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: fp, Code: joincode.Tower.NewCode()}
	require.Contains(t, in.String(), "fp=sha256%3A"+fp)
	short := strings.Replace(in.String(), fp, fp[:16], 1)
	_, err := Parse(short)
	require.ErrorIs(t, err, ErrBadInvite)
}

func TestMalformedInvitesAreRefusedWithAReason(t *testing.T) {
	good := Invite{Kind: TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: fp, Code: joincode.Tower.NewCode()}.String()
	for name, s := range map[string]string{
		"not an invite":      "https://example.com",
		"unknown kind":       strings.Replace(good, "kind=tower-client", "kind=toaster", 1),
		"no address":         "roger://join?kind=tower-client&fp=sha256%3A" + fp + "&code=" + joincode.Tower.NewCode(),
		"address no port":    strings.Replace(good, "192.168.1.20%3A8787", "192.168.1.20", 1),
		"no fingerprint":     "roger://join?kind=tower-client&at=192.168.1.20%3A8787&code=" + joincode.Tower.NewCode(),
		"edge-length code":   strings.Replace(good, good[strings.Index(good, "code=")+5:strings.Index(good, "code=")+31], "ABCDEFGHJK", 1),
		"no code on tower":   "roger://join?kind=tower-client&at=192.168.1.20%3A8787&fp=sha256%3A" + fp,
		"tower code on edge": strings.Replace(good, "kind=tower-client", "kind=edge", 1),
	} {
		_, err := Parse(s)
		require.ErrorIs(t, err, ErrBadInvite, name)
	}
}

// A code in an invite is canonicalized, however it was pasted.
func TestTheCodeIsCanonicalized(t *testing.T) {
	c := joincode.Edge.NewCode()
	s := "roger://join?kind=edge&at=192.168.1.69%3A39009&fp=sha256%3A" + fp + "&code=" + strings.ToLower(joincode.Edge.Format(c))
	got, err := Parse(s)
	require.NoError(t, err)
	require.Equal(t, c, got.Code)
	require.Equal(t, joincode.Edge, got.Kind.Domain())
	require.Equal(t, joincode.Tower, TowerStation.Domain())
}
