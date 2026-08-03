package tower

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Phase 1.2: initializing a Tower data directory. Contract: features/tower/modes.feature.
//
// The properties that matter: a directory is one mode for life, private material is
// owner-only, a failed init leaves nothing behind, and two processes cannot both own one
// identity directory.

func TestInitCreatesAJoinedDirectory(t *testing.T) {
	dir := t.TempDir()
	st, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	require.Equal(t, ModeJoined, st.Mode)

	// A joined Tower binds to the RogerAI public network and mints no trust root of
	// its own: Roger Core is the authority.
	require.Empty(t, st.LocalNetworkID, "a joined Tower must not mint a local network ID")
	require.NotEmpty(t, st.TowerID)

	loaded, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, ModeJoined, loaded.Mode)
	require.Equal(t, st.TowerID, loaded.TowerID)
}

func TestInitCreatesAStandaloneDirectoryWithItsOwnNetwork(t *testing.T) {
	dir := t.TempDir()
	st, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	require.Equal(t, ModeStandalone, st.Mode)
	require.NotEmpty(t, st.LocalNetworkID, "a standalone Tower mints its own network ID")
	require.NotEqual(t, PublicNetworkID, st.LocalNetworkID,
		"a local network ID must never collide with the public network")
}

func TestTwoStandaloneDirectoriesGetDifferentNetworks(t *testing.T) {
	a, err := Init(t.TempDir(), ModeStandalone)
	require.NoError(t, err)
	b, err := Init(t.TempDir(), ModeStandalone)
	require.NoError(t, err)
	require.NotEqual(t, a.LocalNetworkID, b.LocalNetworkID)
	require.NotEqual(t, a.TowerID, b.TowerID)
}

func TestPrivateMaterialIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	err = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		if filepath.Ext(p) == ".key" {
			require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(),
				"%s holds private material and must be owner-read-only", p)
		}
		return nil
	})
	require.NoError(t, err)

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "the identity directory must be owner-only")
}

func TestInitRejectsAnInvalidModeWithoutCreatingAnything(t *testing.T) {
	for _, bad := range []string{"", "public", "private", "hybrid", "joined,standalone", "unknown"} {
		dir := t.TempDir()
		_, err := Init(dir, Mode(bad))
		require.Error(t, err, "mode %q must be rejected", bad)

		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		require.Empty(t, entries, "a rejected init must leave no identity or partial configuration")
	}
}

func TestInitRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	_, err = Init(dir, ModeStandalone)
	require.Error(t, err, "re-initializing an existing Tower directory must fail")
}

// A directory is one mode for life. Changing it in place would carry an identity, trust
// root, or Station registry across the boundary those modes exist to separate.
func TestModeCannotChangeInPlace(t *testing.T) {
	for _, tc := range []struct{ original, requested Mode }{
		{ModeJoined, ModeStandalone},
		{ModeStandalone, ModeJoined},
	} {
		dir := t.TempDir()
		_, err := Init(dir, tc.original)
		require.NoError(t, err)

		st, err := Open(dir)
		require.NoError(t, err)
		err = st.RequireMode(tc.requested)
		require.Error(t, err, "%s directory must refuse to run as %s", tc.original, tc.requested)
		require.Contains(t, err.Error(), "new data directory",
			"the error must tell the operator what to do instead")
	}
}

func TestRequireModeAcceptsItsOwnMode(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, st.RequireMode(ModeStandalone))
}

// One identity directory, one owner. A second process must fail before it can reuse the
// first's session or sequence.
func TestOneProcessOwnsTheIdentityDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	first, err := Open(dir)
	require.NoError(t, err)
	release, err := first.Lock()
	require.NoError(t, err)

	second, err := Open(dir)
	require.NoError(t, err)
	_, err = second.Lock()
	require.Error(t, err, "a second process must not acquire the identity-directory lock")

	require.NoError(t, release())

	// Once released, the directory can be owned again - a crash must not wedge it.
	release2, err := second.Lock()
	require.NoError(t, err)
	require.NoError(t, release2())
}

func TestOpenRejectsADirectoryThatIsNotATower(t *testing.T) {
	_, err := Open(t.TempDir())
	require.Error(t, err)
}
