package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// Adopting a candidate clears it from DISCOVERED at once (not on the next pass), and a candidate
// that has already been granted a claim is not re-offered (it is adopting, not to-adopt).
func TestAdoptInstantlyClearsCandidate(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	st, err := loadEdgeState()
	require.NoError(t, err)
	// A phone candidate (empty Pin -> claim path).
	st.candidates = []store.EdgeNode{{ID: "n_phone", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceCandidate)}}
	require.NoError(t, st.save())

	_, hooks := edgeHostOff(t)
	require.NoError(t, hooks.EdgeAdopt("n_phone", "gentle-ibex-14"))

	require.Empty(t, hooks.EdgeCandidates(), "an adopted candidate leaves DISCOVERED immediately")
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, local.ClaimGranted("n_phone"), "adopting a phone grants its claim")
}

func TestDropGrantedFiltersAdoptingCandidates(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_adopting", "gentle-ibex-14"))

	got := edgeDropGranted([]store.EdgeNode{{ID: "n_adopting"}, {ID: "n_new"}})
	require.Len(t, got, 1)
	require.Equal(t, "n_new", got[0].ID, "a granted (adopting) candidate is filtered out")
}

// Forgetting a node must clear any standing claim grant, so a revoked/forgotten node cannot lean on
// a leftover grant to POST /edge/claim for a fresh certificate and rejoin (audit 2026-09-23).
func TestForgettingANodeConsumesItsGrant(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_gone", "gentle-ibex-14"))
	require.True(t, local.ClaimGranted("n_gone"))

	require.NoError(t, edgeRevokeOnForget("n_gone"))

	reopened, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reopened.ClaimGranted("n_gone"), "forgetting a node clears its claim grant")
}

// A node already in the fleet must not be re-adopted from the TUI. Re-adopting a member mints a
// second certificate while the first is still live; a later forget would revoke only the newest and
// leave the old one working. The CLI adopt already refused a member; the TUI adopt must too, and a
// member re-advertising as a candidate is dropped from DISCOVERED (audit 2026-09-24).
func TestTuiAdoptRefusesANodeAlreadyAMember(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	// A member of the fleet that is ALSO showing up as a candidate (its advert still says account="").
	edgeSeed(t, store.EdgeNode{ID: "n_member", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceVerified)})

	h, hooks := edgeHostOff(t)
	h.mu.Lock()
	h.st.candidates = []store.EdgeNode{{ID: "n_member", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceCandidate)}}
	_ = h.st.save()
	h.mu.Unlock()

	err := hooks.EdgeAdopt("n_member", "gentle-ibex-14")
	require.Error(t, err, "a node already a member must not be adopted again")
	require.Contains(t, err.Error(), "already a member")

	// And a member re-advertising as a candidate is filtered from what is on offer.
	require.Empty(t, edgeDropMembers(h.st.candidates, h.st.fleet),
		"a member is not offered for adoption")
}

// Forgetting a node when the authority cannot be opened (a corrupt issued.json / revoked.json) must
// FAIL, not silently skip revocation and report success - the fail-open the audit caught. The node
// stays in the fleet so the forget can be retried once the state is repaired (audit 2026-09-24).
func TestForgetFailsWhenTheAuthorityIsCorrupt(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	// Corrupt the authority's issued-certificate record so OpenLocal cannot read it.
	require.NoError(t, os.WriteFile(filepath.Join(edgeAuthDir(), edgeauth.AuthorityDir, "issued.json"), []byte("{bad"), 0o600))

	err = edgeRevokeOnForget("n_somebody")
	require.Error(t, err, "a forget that could not revoke must report the failure")
}

// A failed revocation refresh must NOT stamp the stored list as freshly refreshed - otherwise the
// staleness warning is suppressed and a revoked node keeps verifying with no warning (audit 2026-09-24).
func TestRefreshTrustDoesNotMarkFreshWhenTheFetchFails(t *testing.T) {
	useTempConfig(t)
	st := edgeIdentityStore()
	old := time.Now().Add(-72 * time.Hour)
	require.NoError(t, st.SaveTrust([]string{"11"}, old))

	// A fetch against an endpoint that cannot answer.
	err := edgeRefreshTrust(st, "http://127.0.0.1:1/edge", nil, false, time.Now())
	require.NoError(t, err, "a failed refresh does not fail the caller")

	got, err := st.LoadTrust()
	require.NoError(t, err)
	require.Equal(t, old.Unix(), got.RefreshedAt, "a failed refresh leaves the list stale, not freshly stamped")
	require.Equal(t, []string{"11"}, got.Revoked, "and does not lose the list it had")
}

// An enrolled machine that has NEVER refreshed its revocation list must warn - "never refreshed" is
// not the same as "fresh". A machine that never enrolled says nothing (audit 2026-09-24).
func TestTrustNoteWarnsWhenEnrolledButNeverRefreshed(t *testing.T) {
	offline(t)
	require.Empty(t, edgeTrustNote(time.Now()), "a machine that never enrolled has nothing to warn about")

	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)

	// Force the stored list to the never-refreshed state a failed first refresh would leave.
	require.NoError(t, edgeIdentityStore().SaveTrust(nil, time.Unix(0, 0)))
	require.Contains(t, edgeTrustNote(time.Now()), "never been refreshed",
		"an enrolled machine that never synced its revocation list must warn, not stay silent")
}

// Forget must surface an unreadable identity rather than claim success having revoked nothing.
func TestForgetSurfacesAnUnreadableIdentity(t *testing.T) {
	offline(t)
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)

	// Corrupt this machine's node key so LoadIdentity fails.
	require.NoError(t, os.WriteFile(filepath.Join(edgeAuthDir(), "node.key"), []byte("not-hex"), 0o600))
	err := edgeRevokeOnForget("n_whatever")
	require.Error(t, err, "a forget must report an unreadable identity, not silently do nothing")
}
