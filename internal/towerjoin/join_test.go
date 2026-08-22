package towerjoin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
)

// This package holds the JOINED-mode account flow. It lives outside internal/tower on
// purpose: signing in needs the network, and internal/tower is covered by a gate test
// that fails if any file there gains the ability to reach it. Keeping the network here
// is what lets the standalone core stay provably egress-free.
//
// Contract: features/tower/operator_login.feature.

func joinedTower(t *testing.T) *tower.State {
	t.Helper()
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeJoined)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	return st
}

func standaloneTower(t *testing.T) *tower.State {
	t.Helper()
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	return st
}

// The founder ruling: standalone never involves an account, so registering one is not a
// missing feature - it is a category error, and the message must say so.
func TestStandaloneCannotBeRegistered(t *testing.T) {
	st := standaloneTower(t)
	err := Register(st, Account{Login: "alice"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "standalone")
	require.Contains(t, err.Error(), "new data directory")
}

func TestRegisteringJoinedRequiresAnAccount(t *testing.T) {
	st := joinedTower(t)
	err := Register(st, Account{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign in")
}

// Registration must refuse BEFORE any network call that would create state, so a
// signed-out operator cannot leave a half-enrolled Tower behind.
func TestRegisteringWithoutAnAccountMakesNoNetworkCall(t *testing.T) {
	st := joinedTower(t)
	calls := 0
	restore := setEnrollForTest(func(*tower.State, Account) error { calls++; return nil })
	t.Cleanup(restore)

	require.Error(t, Register(st, Account{}))
	require.Zero(t, calls, "registration must fail before it would call out")
}

func TestRegisteringWithAnAccountReachesEnrollment(t *testing.T) {
	st := joinedTower(t)
	calls := 0
	restore := setEnrollForTest(func(*tower.State, Account) error { calls++; return nil })
	t.Cleanup(restore)

	require.NoError(t, Register(st, Account{Login: "alice"}))
	require.Equal(t, 1, calls)
}

// Phase 2 does not exist, so the real enrollment path must say so rather than failing
// obscurely or pretending to have registered something.
func TestRealEnrollmentReachesTheNetwork(t *testing.T) {
	// Registration is implemented, so this proves the path is WIRED: with the broker
	// pointed somewhere nothing answers, the failure is about reaching RogerAI rather than
	// about a feature that does not exist. An operator's next step differs completely
	// between those two messages.
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())

	st := joinedTower(t)
	err := Register(st, Account{Login: "alice"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "not implemented")
	require.Contains(t, err.Error(), "could not reach RogerAI")
}

// --- credential storage ----------------------------------------------------

func TestAccountCredentialIsStoredOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SaveAccount(dir, Account{Login: "alice", Token: "secret-token"}))

	fi, err := os.Stat(filepath.Join(dir, accountFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestLoadedAccountRoundTrips(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SaveAccount(dir, Account{Login: "alice", Token: "secret-token"}))

	got, ok := LoadAccount(dir)
	require.True(t, ok)
	require.Equal(t, "alice", got.Login)
}

func TestNoAccountReadsAsSignedOut(t *testing.T) {
	_, ok := LoadAccount(t.TempDir())
	require.False(t, ok)
}

// The stored token must never be rendered. Describing an account is for humans; it names
// who they are, not what their credential is.
func TestAccountDescriptionNeverIncludesTheToken(t *testing.T) {
	a := Account{Login: "alice", Token: "super-secret-token"}
	require.Contains(t, a.String(), "alice")
	require.NotContains(t, a.String(), "super-secret-token")
}

func TestSignOutRemovesTheCredentialButNotTheIdentity(t *testing.T) {
	st := joinedTower(t)
	dir := st.Dir()
	require.NoError(t, SaveAccount(dir, Account{Login: "alice", Token: "t"}))

	require.NoError(t, SignOut(dir))
	_, ok := LoadAccount(dir)
	require.False(t, ok, "the account credential is gone")

	// The Tower's own identity is untouched: it still opens with the same id.
	reopened, err := tower.Open(dir)
	require.NoError(t, err)
	require.Equal(t, st.TowerID, reopened.TowerID)
}

func TestSignOutWhenNotSignedInIsNotAnError(t *testing.T) {
	require.NoError(t, SignOut(t.TempDir()), "signing out twice must be harmless")
}

func TestCorruptAccountFileReadsAsSignedOut(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, accountFile), []byte("{broken"), 0o600))
	_, ok := LoadAccount(dir)
	require.False(t, ok, "unreadable credentials must read as signed out, not as a usable account")
}

func TestAccountDescriptionForASignedOutOperator(t *testing.T) {
	require.Equal(t, "not signed in", Account{}.String())
}

func TestSaveAndSignOutFailOnAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	// A read-only directory blocks CREATING and REMOVING entries. (Overwriting an
	// existing file would still succeed, since that needs permission on the file, not
	// the directory - so the save case must start with no credential present.)
	saveDir := t.TempDir()
	require.NoError(t, os.Chmod(saveDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(saveDir, 0o700) })
	require.Error(t, SaveAccount(saveDir, Account{Login: "alice", Token: "t"}),
		"a credential that cannot be persisted must not report success")

	rmDir := t.TempDir()
	require.NoError(t, SaveAccount(rmDir, Account{Login: "alice", Token: "t"}))
	require.NoError(t, os.Chmod(rmDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(rmDir, 0o700) })
	require.Error(t, SignOut(rmDir),
		"a sign-out that cannot remove the credential must not claim to have done so")
}
