package towercli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/tower"
)

// features/join/migration.feature: the old commands still work for one release, each with one
// line naming the new way.
func TestTheOldCommandsNameTheNewWay(t *testing.T) {
	dir := t.TempDir() + "/lab"
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, run([]string{"invite", "--dir", dir, "--client", "u_owner"}, &out))
	require.Contains(t, out.String(), "the new way: roger tower invite (one paste: roger join)")
	id, code := field(out.String(), "invitation: "), field(out.String(), "code: ")
	out.Reset()
	require.NoError(t, run([]string{"admit", "--dir", dir, "--client", "u_owner", "--id", id, "--code", code}, &out))
	require.Contains(t, out.String(), "the new way: roger tower invite, then roger join")
	out.Reset()
	require.NoError(t, run([]string{"attach", "--dir", dir, "--station", "s", "--key", "abababababababab", "--models", "m"}, &out))
	require.Contains(t, out.String(), "the new way: roger tower invite --station")
}
