package edge

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

func newGrantStore(t *testing.T) *GrantStore {
	t.Helper()
	return &GrantStore{Path: filepath.Join(t.TempDir(), "edge", "grants.json")}
}

func TestGrantStorePutListGet(t *testing.T) {
	s := newGrantStore(t)

	// A fresh store is empty, not an error - the first Put creates the file.
	got, err := s.List()
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, s.Put(store.Grant{ID: "g1", Owner: "acct", Caller: "n_a", Nodes: []string{"n_b"}, Caps: []string{"operate"}}))
	require.NoError(t, s.Put(store.Grant{ID: "g2", Owner: "acct", Caller: "n_a", Nodes: []string{"n_c"}, Caps: []string{"sense"}}))

	got, err = s.List()
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "g1", got[0].ID, "listed in the order issued")
	require.Equal(t, "g2", got[1].ID)

	g, ok, err := s.Get("g2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"sense"}, g.Caps)

	_, ok, err = s.Get("nope")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestGrantStorePutReplacesSameID(t *testing.T) {
	s := newGrantStore(t)
	require.NoError(t, s.Put(store.Grant{ID: "g1", Caps: []string{"operate"}}))
	require.NoError(t, s.Put(store.Grant{ID: "g1", Caps: []string{"operate", "sense"}}))
	got, err := s.List()
	require.NoError(t, err)
	require.Len(t, got, 1, "a re-issue replaces, it does not duplicate")
	require.Equal(t, []string{"operate", "sense"}, got[0].Caps)
}

func TestGrantStoreRevoke(t *testing.T) {
	s := newGrantStore(t)
	require.NoError(t, s.Put(store.Grant{ID: "g1"}))

	found, err := s.Revoke("g1")
	require.NoError(t, err)
	require.True(t, found)

	g, _, err := s.Get("g1")
	require.NoError(t, err)
	require.True(t, g.Revoked, "a revoked grant is kept, marked revoked, not deleted")

	// Revoking again is idempotent; revoking an unknown id reports not-found without error.
	found, err = s.Revoke("g1")
	require.NoError(t, err)
	require.True(t, found)
	found, err = s.Revoke("nope")
	require.NoError(t, err)
	require.False(t, found)
}

func TestGrantStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge", "grants.json")
	s1 := &GrantStore{Path: path}
	require.NoError(t, s1.Put(store.Grant{ID: "g1", Caller: "n_a"}))

	s2 := &GrantStore{Path: path}
	got, err := s2.List()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "n_a", got[0].Caller, "the store is on disk, so a second opener sees it")
}
