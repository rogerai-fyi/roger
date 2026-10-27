package operator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpencodeConfigNamesTheProfilePinOnce: a band model that is already the profile
// reference, with that profile chosen, lists the reference once (no duplicate JSON key).
func TestOpencodeConfigNamesTheProfilePinOnce(t *testing.T) {
	var oc Guest
	for _, g := range Registry() {
		if g.Name == "opencode" {
			oc = g
		}
	}
	l, cleanup, err := Materialize(oc, Session{BaseURL: "http://127.0.0.1:1/v1", SessionKey: "sk", Model: "@profile/coding",
		Profile: "coding", ScratchRoot: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup() })
	raw, err := os.ReadFile(filepath.Join(l.Dir, "opencode.json"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), `"@profile/coding": {`), string(raw))
}
