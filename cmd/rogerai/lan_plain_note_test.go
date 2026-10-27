package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/client"
)

// features/join/tower_identity.feature section 4: a plain-http Tower still works for one release,
// marked unverified, with the one-time trust note and a line asking for the upgrade. A Tower
// pinned to its TLS key gets neither: it is verified.
func TestThePlainHTTPNoteIsOnlyForUnpinnedTowers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pinned := []client.LANSource{{Kind: client.LANTower, ID: "192.168.1.20:8787", Verified: true}}
	out := captureStdout(t, func() { lanTrustNoteOnce(pinned, "") })
	require.Empty(t, strings.TrimSpace(out), "a pinned Tower prints no trust note")

	plain := []client.LANSource{{Kind: client.LANTower, ID: "192.168.1.30:8787"}}
	out = captureStdout(t, func() { lanTrustNoteOnce(plain, "") })
	require.Contains(t, out, "this Tower uses plain http; ask its owner to upgrade roger so it can be verified")
	require.Contains(t, out, "(shown once)", "the one-time trust note")

	out = captureStdout(t, func() { lanTrustNoteOnce(plain, "") })
	require.Contains(t, out, "this Tower uses plain http; ask its owner to upgrade roger so it can be verified",
		"the upgrade line names the Tower every time it is used")
	require.NotContains(t, out, "(shown once)", "the trust note is not repeated")
}
