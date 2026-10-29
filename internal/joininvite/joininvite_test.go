package joininvite

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/joincode"
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

// An invite names its kind exactly once, first, so it reads as what it is and never carries two
// kinds a parser could disagree on.
func TestAnInviteNamesItsKindOnce(t *testing.T) {
	for _, k := range []Kind{TowerClient, TowerStation, Edge} {
		code := joincode.Tower.NewCode()
		if k == Edge {
			code = joincode.Edge.NewCode()
		}
		s := Invite{Kind: k, Addrs: []string{"192.168.1.20:8787"}, FP: fp, Code: code, For: "laptop-x"}.String()
		require.Equal(t, 1, strings.Count(s, "kind="), s)
		require.True(t, strings.HasPrefix(s, "roger://join?kind="+string(k)+"&"), s)
	}
}

// The printed join line pastes into a shell as is: the whole invite reaches roger as ONE argument
// in sh, bash and zsh (an unquoted ? and & would glob in zsh and background in sh).
func TestTheJoinLinePastesIntoAShell(t *testing.T) {
	inv := Invite{Kind: TowerClient, Addrs: []string{"192.168.1.20:8787", "[fe80::1%en0]:8787"}, FP: fp, Code: joincode.Tower.NewCode(), For: "laptop-x"}.String()
	line := JoinLine(inv)
	require.True(t, strings.HasPrefix(line, "roger join "), line)
	for _, sh := range []string{"sh", "bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		// roger is a shell function here that prints its arguments one per line.
		out, err := exec.Command(path, "-c", `roger() { for a in "$@"; do printf '%s\n' "$a"; done; }; `+line).CombinedOutput()
		require.NoError(t, err, "%s: %s", sh, out)
		require.Equal(t, "join\n"+inv+"\n", string(out), sh)
	}
	require.Equal(t, inv, FromLine(line))
	require.Equal(t, inv, FromLine("roger join "+inv), "an unquoted line still reads back")
	got, err := Parse("'" + inv + "'")
	require.NoError(t, err, "a pasted invite that kept its quotes still parses")
	want, _ := Parse(inv)
	require.Equal(t, want, got)
}
