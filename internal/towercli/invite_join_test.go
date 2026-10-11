package towercli

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/tower"
)

var joinLine = regexp.MustCompile(`roger join '?(roger://join\?[^'\s]+)'?`)

// features/join/invite.feature: the owner mints a client invite with one command; it prints one
// "roger join <invite>" line, says how long it lasts, and names no client.
func TestInviteMintsAOnePasteJoinLine(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--at", "192.168.1.20:8787")
	require.NoError(t, err)
	m := joinLine.FindStringSubmatch(out)
	require.NotNil(t, m, out)
	in, err := joininvite.Parse(m[1])
	require.NoError(t, err)
	require.Equal(t, joininvite.TowerClient, in.Kind)
	require.Equal(t, []string{"192.168.1.20:8787"}, in.Addrs)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	fp, _ := st.JoinFingerprint()
	require.Equal(t, fp, in.FP, "it carries the full identity fingerprint")
	require.Contains(t, out, "valid for 15 minutes and works once")
	require.Equal(t, 1, strings.Count(out, in.Code), "the code is printed once")
}

func TestInviteStationListAndVoid(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--station", "--models", "qwen3-8b", "--at", "192.168.1.20:8787")
	require.NoError(t, err)
	in, err := joininvite.Parse(joinLine.FindStringSubmatch(out)[1])
	require.NoError(t, err)
	require.Equal(t, joininvite.TowerStation, in.Kind)
	_, err = runCLI(t, "invite", "--dir", dir, "--at", "192.168.1.20:8787")
	require.NoError(t, err)

	list, err := runCLI(t, "invite", "--dir", dir, "--list")
	require.NoError(t, err)
	require.Contains(t, list, "station")
	require.Contains(t, list, "client")
	require.NotContains(t, list, in.Code, "the list never shows a code")
	id := regexp.MustCompile(`(?m)^\s*([0-9a-f]{16})\s+station`).FindStringSubmatch(list)
	require.NotNil(t, id, list)
	_, err = runCLI(t, "invite", "--dir", dir, "--void", id[1])
	require.NoError(t, err)
	list, _ = runCLI(t, "invite", "--dir", dir, "--list")
	require.NotContains(t, list, "station")
	require.Contains(t, list, "client")
}

// With no address given, the invite carries the addresses the Tower's bind serves on.
func TestInviteDefaultsToTheServeBind(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir)
	require.NoError(t, err)
	in, err := joininvite.Parse(joinLine.FindStringSubmatch(out)[1])
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:8787"}, in.Addrs)
	require.Contains(t, out, "only this machine can use it", "a loopback-only Tower says so")
}

func inviteYAML(t *testing.T, bind string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tower.yaml")
	require.NoError(t, os.WriteFile(p, []byte("apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: standalone\nstandalone:\n  bind: "+bind+"\n"), 0o600))
	return p
}

// An all-interfaces bind expands to this machine's home addresses (never loopback, never a public
// one); a configured LAN bind is named as is; nothing home falls back to loopback, said so.
func TestInviteAddrsFollowTheServeBind(t *testing.T) {
	prev := inviteInterfaceAddrs
	t.Cleanup(func() { inviteInterfaceAddrs = prev })
	inviteInterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1")}, &net.IPNet{IP: net.ParseIP("192.168.1.20")},
			&net.IPNet{IP: net.ParseIP("203.0.113.9")}, &net.IPNet{IP: net.ParseIP("fd00::5")},
		}, nil
	}
	got, lo, err := inviteAddrs(inviteYAML(t, "0.0.0.0:9000"), "")
	require.NoError(t, err)
	require.False(t, lo)
	require.Equal(t, []string{"192.168.1.20:9000", "[fd00::5]:9000"}, got)

	got, lo, err = inviteAddrs(inviteYAML(t, "192.168.1.20:9000"), "")
	require.NoError(t, err)
	require.False(t, lo)
	require.Equal(t, []string{"192.168.1.20:9000"}, got)

	inviteInterfaceAddrs = func() ([]net.Addr, error) { return []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.9")}}, nil }
	got, lo, err = inviteAddrs(inviteYAML(t, ":9000"), "")
	require.NoError(t, err)
	require.True(t, lo)
	require.Equal(t, []string{"127.0.0.1:9000"}, got)

	_, _, err = inviteAddrs(inviteYAML(t, "nonsense"), "")
	require.Error(t, err)
}

func TestInviteForANameListsItAndTheOldFlowStillWorks(t *testing.T) {
	dir := initStandalone(t)
	list, err := runCLI(t, "invite", "--dir", dir, "--list")
	require.NoError(t, err)
	require.Contains(t, list, "no outstanding invites")
	out, err := runCLI(t, "invite", "--dir", dir, "--for", "laptop-x", "--at", "192.168.1.20:8787")
	require.NoError(t, err)
	in, err := joininvite.Parse(joinLine.FindStringSubmatch(out)[1])
	require.NoError(t, err)
	require.Equal(t, "laptop-x", in.For)
	list, _ = runCLI(t, "invite", "--dir", dir, "--list")
	require.Contains(t, list, "for laptop-x")
	_, err = runCLI(t, "invite", "--dir", dir, "--void", "nope")
	require.Error(t, err)
	old, err := runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.NoError(t, err)
	require.Contains(t, old, "older two-step flow")
}

// A Tower serving now is invited at the address it is actually bound to (its live serve record),
// not the config's or the default bind: `serve --bind` on another port must not mint an invite
// that dials the wrong place.
func TestInviteAddrsFollowTheLiveServe(t *testing.T) {
	dir := initStandalone(t)
	prev := servingTowers
	t.Cleanup(func() { servingTowers = prev })
	servingTowers = func() []onair.Tower {
		return []onair.Tower{
			{PID: 1, Dir: "/elsewhere", Addr: "127.0.0.1:9999"},
			{PID: 2, Dir: dir, Addr: "127.0.0.1:18787"},
		}
	}
	out, err := runCLI(t, "invite", "--dir", dir)
	require.NoError(t, err)
	in, err := joininvite.Parse(joinLine.FindStringSubmatch(out)[1])
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:18787"}, in.Addrs, "the live serve's address, not the default 8787")

	servingTowers = func() []onair.Tower { return nil }
	out, err = runCLI(t, "invite", "--dir", dir)
	require.NoError(t, err)
	in, err = joininvite.Parse(joinLine.FindStringSubmatch(out)[1])
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:8787"}, in.Addrs, "not serving: the configured or default bind")
}
