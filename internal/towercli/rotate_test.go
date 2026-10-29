package towercli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerpin"
)

// tower_identity.feature: rotate-key serves under a new fingerprint and says every client and
// Station must re-join; status shows the short fingerprint, --full the whole one.
func TestRotateKeyAndStatusFingerprint(t *testing.T) {
	dir := t.TempDir() + "/lab"
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	before, _ := st.JoinFingerprint()

	var out bytes.Buffer
	require.NoError(t, run([]string{"status", "--dir", dir}, &out))
	require.Contains(t, out.String(), "fingerprint: "+towerpin.Short(before))
	require.NotContains(t, out.String(), before, "the short form only, unless --full")
	out.Reset()
	require.NoError(t, run([]string{"status", "--dir", dir, "--full"}, &out))
	require.Contains(t, out.String(), "fingerprint: sha256:"+before)

	out.Reset()
	require.NoError(t, run([]string{"rotate-key", "--dir", dir}, &out))
	after, _ := st.JoinFingerprint()
	require.NotEqual(t, before, after)
	require.Contains(t, out.String(), "sha256:"+after)
	require.Contains(t, out.String(), "every client and station must re-join with a new invite")

	out.Reset()
	require.NoError(t, run([]string{"invite", "--dir", dir, "--at", "127.0.0.1:8787"}, &out))
	require.True(t, strings.Contains(out.String(), "fp="+after) || strings.Contains(out.String(), after),
		"invites name the new fingerprint: %s", out.String())
}
