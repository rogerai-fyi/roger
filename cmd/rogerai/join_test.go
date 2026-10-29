package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/joincode"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/localplane"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerpin"
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

// joinTowerFixture serves a real standalone Tower's plane on loopback and returns its state and
// address.
func joinTowerFixture(t *testing.T) (*tower.State, string) {
	t.Helper()
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	key, err := st.TLSKey()
	require.NoError(t, err)
	cfg, err := towerpin.ServerTLS(key)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return st, ln.Addr().String()
}

func towerInvite(t *testing.T, st *tower.State, addr string, kind tower.JoinKind) joininvite.Invite {
	t.Helper()
	_, code, err := st.CreateJoinInvitation(kind, "", nil, 15*time.Minute)
	require.NoError(t, err)
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	k := joininvite.TowerClient
	if kind == tower.JoinStation {
		k = joininvite.TowerStation
	}
	return joininvite.Invite{Kind: k, Addrs: []string{addr}, FP: fp, Code: code}
}

// features/join/invite.feature: a client joins a Tower with one pasted command; the Tower admits
// its key, and the client records the Tower with its pinned fingerprint and says what to do next.
func TestJoinATowerWithOnePastedInvite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinClient)
	out := captureStdout(t, func() { require.NoError(t, cmdJoin(loadConfig(), []string{inv.String()})) })
	require.Contains(t, out, "joined the Tower at "+addr)
	require.Contains(t, out, "try: roger --lan use <band>")
	c := loadConfig()
	require.Contains(t, c.LanTowers, addr)
	require.Equal(t, inv.FP, c.LanTowerPins[addr], "the Tower is recorded WITH its pinned fingerprint")
	key := client.LoadOrCreateUserKey()
	require.True(t, st.IsAdmitted(protocol.UserIDFromPubkey(hex.EncodeToString(key.Public().(ed25519.PublicKey)))))

	// The invite works once: a second machine is refused, and nothing is recorded for it.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cmdJoin(loadConfig(), []string{inv.String()})
	require.ErrorContains(t, err, "that invite was already used. Ask the owner for a new one")
	require.Empty(t, loadConfig().LanTowers)
}

// A station invite names the kind when a client uses it, and does not spend it.
func TestJoinWithTheWrongKindSaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinStation)
	inv.Kind = joininvite.TowerClient // a client presenting a station's code
	err := cmdJoin(loadConfig(), []string{inv.String()})
	require.ErrorContains(t, err, "this invite is for a station, not a client")
	require.Len(t, st.LiveInvitations(), 1, "the invite was not spent")
}

// The client tries the invite's addresses in order and records the one that worked.
func TestJoinTriesTheAddressesInOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinClient)
	inv.Addrs = append([]string{"127.0.0.1:1"}, inv.Addrs...) // nothing answers there
	captureStdout(t, func() { require.NoError(t, cmdJoin(loadConfig(), []string{inv.String()})) })
	require.Equal(t, []string{addr}, loadConfig().LanTowers)
}

// features/join/invite.feature: the client checks the Tower's identity against the invite's
// fingerprint BEFORE sending anything; a device at that address with another key gets nothing.
func TestJoinChecksTheTowersKeyBeforeSendingAnything(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinClient)
	other, _ := joinTowerFixture(t)
	inv.FP, _ = other.JoinFingerprint() // the invite pins a different Tower's key
	err := cmdJoin(loadConfig(), []string{inv.String()})
	require.ErrorContains(t, err, "is not the one in the invite")
	require.Len(t, st.LiveInvitations(), 1, "nothing reached the Tower: the invite is unspent")
	require.Empty(t, loadConfig().LanTowers)
}

// features/join/tower_identity.feature: after joining, every request checks the pin; a device
// answering with another key is refused before any prompt is sent, with the recovery hint, and
// the pin is not changed.
func TestAJoinedTowerIsReadOverItsPinAndAChangedKeyIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { lanmode.SetPins(nil) })
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinClient)
	captureStdout(t, func() { require.NoError(t, cmdJoin(loadConfig(), []string{inv.String()})) })

	_, status := client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{addr}})
	require.Len(t, status, 1)
	require.NotEqual(t, client.LANTowerRefused, status[0].State, status[0].Detail)
	require.NotEqual(t, client.LANTowerNotAdmitted, status[0].State, "the joined key is admitted, over TLS")

	other, _ := joinTowerFixture(t)
	wrong, _ := other.JoinFingerprint()
	lanmode.SetPins(map[string]string{addr: wrong}) // the device at addr no longer matches the pin
	_, status = client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{addr}})
	require.Equal(t, client.LANTowerRefused, status[0].State)
	require.Contains(t, status[0].Detail, "is not the one you joined (its key changed)")
	require.Contains(t, status[0].Detail, "roger join <invite>")
	require.Equal(t, inv.FP, loadConfig().LanTowerPins[addr], "the stored pin is not changed")
}

// Live E2E 2026-10-27: a station joining under a name another station already holds is told so, and
// how to join under another name, with the invite still good.
func TestAStationNameClashIsSaidAsSuch(t *testing.T) {
	err := joinStatusRefusal(http.StatusConflict, []byte(`{"error":"station_name_taken","name":"RogGentoo"}`), "127.0.0.1:28787")
	require.Error(t, err)
	require.Contains(t, err.Error(), `a station named "RogGentoo" is already on this Tower`)
	require.Contains(t, err.Error(), "the invite is still good")
	require.Contains(t, err.Error(), "roger join <invite> <another name>")
	require.NotContains(t, err.Error(), "expired")
}
