package main

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// features/join/migration.feature: a Tower already in lan_towers with no pin is pinned the first
// time it answers over TLS after the upgrade, once, logged; an older plain-http Tower is left as
// it was; an address not in lan_towers is never pinned this way.
func TestFirstUsePinsAnAddedTowerOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := tower.Init(t.TempDir()+"/lab", tower.ModeStandalone)
	require.NoError(t, err)
	key, _ := st.TLSKey()
	cfg, _ := towerpin.ServerTLS(key)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ps := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = ps.Serve(plain) }()
	t.Cleanup(func() { _ = ps.Close() })
	tlsAt, plainAt := ln.Addr().String(), plain.Addr().String()
	fp, _ := st.JoinFingerprint()

	c := loadConfig()
	c.LanTowers = []string{tlsAt, plainAt}
	require.NoError(t, saveConfig(c))

	out := captureStdout(t, func() { lanFirstUsePins(&c) })
	require.Contains(t, out, "pinned the Tower at "+tlsAt+" now (first use after upgrade): sha256:"+fp)
	saved := loadConfig()
	require.Equal(t, fp, saved.LanTowerPins[tlsAt])
	require.True(t, saved.LanTowerPinFirstUse[tlsAt], "the config records that the pin came from first use")
	require.NotContains(t, saved.LanTowerPins, plainAt, "an older plain-http Tower is left as it was")
	require.Equal(t, fp, c.LanTowerPins[tlsAt], "the running process uses the pin at once")

	out = captureStdout(t, func() { lanFirstUsePins(&c) })
	require.False(t, strings.Contains(out, "pinned the Tower"), "first use happens once: %s", out)
}

// features/join/migration.feature: `roger lan add` of a TLS Tower shows its short fingerprint and
// asks before pinning; only y records the pin. It also names the new way to join.
func TestLanAddAsksBeforePinning(t *testing.T) {
	for _, answer := range []bool{false, true} {
		t.Run(map[bool]string{false: "no", true: "yes"}[answer], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			st, err := tower.Init(t.TempDir()+"/lab", tower.ModeStandalone)
			require.NoError(t, err)
			key, _ := st.TLSKey()
			cfg, _ := towerpin.ServerTLS(key)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			srv := &http.Server{Handler: localplane.New(st).Handler()}
			go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
			t.Cleanup(func() { _ = srv.Close() })
			fp, _ := st.JoinFingerprint()
			prev := lanAddConfirm
			lanAddConfirm = func() bool { return answer }
			t.Cleanup(func() { lanAddConfirm = prev })

			var b strings.Builder
			require.NoError(t, lanAddTo(&b, []string{ln.Addr().String()}))
			require.Contains(t, b.String(), towerpin.Short(fp)+"; pin this Tower? [y/N]")
			require.Contains(t, b.String(), "the new way: roger join <invite>")
			_, pinned := loadConfig().LanTowerPins[ln.Addr().String()]
			require.Equal(t, answer, pinned, "only y records the pin")
		})
	}
}

// features/join/migration.feature: a Tower the owner declined to pin at `roger lan add` stays
// unverified, as the decline says, until it is joined with an invite. The first-use migration is
// for Towers added before joins were by invite; it never overrides an answer the owner gave.
func TestADeclinedPinIsNeverTakenOnFirstUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := tower.Init(t.TempDir()+"/lab", tower.ModeStandalone)
	require.NoError(t, err)
	key, _ := st.TLSKey()
	cfg, _ := towerpin.ServerTLS(key)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	prev := lanAddConfirm
	lanAddConfirm = func() bool { return false }
	t.Cleanup(func() { lanAddConfirm = prev })

	var b strings.Builder
	require.NoError(t, lanAddTo(&b, []string{ln.Addr().String()}))
	require.Contains(t, b.String(), "not pinned")

	c := loadConfig()
	out := captureStdout(t, func() { lanFirstUsePins(&c) })
	require.NotContains(t, out, "pinned the Tower", "a declined pin is not taken on first use")
	_, pinned := loadConfig().LanTowerPins[ln.Addr().String()]
	require.False(t, pinned, "the owner's no stands")
}
