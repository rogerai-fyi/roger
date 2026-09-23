package edge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPersistentAgentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	require.Empty(t, PersistentAgents(dir))
	require.False(t, IsPersistent(dir, "scout"))

	require.NoError(t, MarkPersistent(dir, "n_abc", "scout", time.Unix(100, 0)))
	require.NoError(t, MarkPersistent(dir, "n_abc", "scribe", time.Unix(101, 0)))
	got := PersistentAgents(dir)
	require.Len(t, got, 2)
	require.Equal(t, "scout", got[0].Name) // sorted asc: "scout" < "scribe" (o < r at index 2)
	require.Equal(t, "scribe", got[1].Name)
	require.True(t, IsPersistent(dir, "scout"))

	had, err := UnmarkPersistent(dir, "scout")
	require.NoError(t, err)
	require.True(t, had)
	require.False(t, IsPersistent(dir, "scout"))

	// removing a missing one is a clean no-op.
	had, err = UnmarkPersistent(dir, "scout")
	require.NoError(t, err)
	require.False(t, had)

	// a name with odd characters is stored safely.
	require.NoError(t, MarkPersistent(dir, "n_x", "a/../b name", time.Unix(102, 0)))
	require.True(t, IsPersistent(dir, "a/../b name"))
}
