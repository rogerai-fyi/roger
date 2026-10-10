package main

// "roger edge invite needs no joiner key first" (features/join/invite.feature, ruling 2026-10-10):
// the owner mints a key-less invite; a device that never had its key allowed joins with one paste,
// once. A real local authority served over real HTTP; the joiner is a second machine (its own
// config and its own user key).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/joininvite"
)

// ownerEdgeInvite designates a local authority in a fresh config, serves it, and mints an invite
// with `roger edge invite`. It returns the invite and the authority.
func ownerEdgeInvite(t *testing.T) (string, *edgeauth.Local) {
	t.Helper()
	offline(t)
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	srv := httptest.NewServer(enrollhttp.Handler(local))
	t.Cleanup(srv.Close)
	at := strings.TrimPrefix(srv.URL, "http://")

	out, code := edgeRun(t, "edge", "invite", "--at", at)
	require.Equal(t, 0, code, out)
	require.NotContains(t, strings.ToLower(out), "user key", "a key-less invite asks for no key:\n%s", out)
	m := joinLineRE.FindStringSubmatch(out)
	require.NotNil(t, m, "edge invite prints `roger join <invite>`:\n%s", out)
	in, err := joininvite.Parse(m[1])
	require.NoError(t, err)
	require.Equal(t, joininvite.Edge, in.Kind)
	return m[1], local
}

// asSecondMachine switches this test to another machine: its own config and its own user key.
func asSecondMachine(t *testing.T) string {
	t.Helper()
	offline(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	prev := edgeUserKey
	edgeUserKey = func() ed25519.PrivateKey { return priv }
	t.Cleanup(func() { edgeUserKey = prev })
	return hex.EncodeToString(pub)
}

func TestEdgeInviteAdmitsADeviceWhoseKeyWasNeverAllowed(t *testing.T) {
	invite, local := ownerEdgeInvite(t)
	key := asSecondMachine(t)

	out, code := edgeRun(t, "join", invite, "workshop")
	require.Equal(t, 0, code, out)
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Contains(t, allowed, key, "the joiner's key is admitted when it presents the code")
}

func TestAnEdgeInviteWorksOnce(t *testing.T) {
	invite, _ := ownerEdgeInvite(t)
	asSecondMachine(t)
	_, code := edgeRun(t, "join", invite, "workshop")
	require.Equal(t, 0, code)

	asSecondMachine(t)
	out, code := edgeRun(t, "join", invite, "bench")
	require.NotEqual(t, 0, code, "a spent invite admits nobody else:\n%s", out)
}
