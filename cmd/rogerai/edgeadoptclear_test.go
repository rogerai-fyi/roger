package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
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

	_, err = edgeRevokeOnForget("n_gone")
	require.NoError(t, err)

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

	_, err = edgeRevokeOnForget("n_somebody")
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
	warn, err := edgeRefreshTrust(st, "http://127.0.0.1:1/edge", nil, false, time.Now())
	require.NoError(t, err, "a failed refresh does not fail the caller")
	require.NotEmpty(t, warn, "a failed refresh returns a warning for the caller to surface")

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
	_, err := edgeRevokeOnForget("n_whatever")
	require.Error(t, err, "a forget must report an unreadable identity, not silently do nothing")
	require.Contains(t, err.Error(), "could not read this machine's identity",
		"the error names the actual failure, not some other one")
}

// Forgetting a device that was ADOPTED but has not claimed yet (so it is not in the fleet) must
// cancel its pending claim grant - otherwise it could still claim a certificate later (audit 2026-09-24).
func TestForgetCancelsAPendingAdoption(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_pending", "gentle-ibex-14"))
	require.True(t, local.ClaimGranted("n_pending"))

	// Forget by the friendly name it was adopted under (confirmed via --yes).
	_, err = captureEdgeStdout(func() error {
		return cmdEdgeForget(loadConfig(), []string{"gentle-ibex-14", "--yes"})
	})
	require.NoError(t, err)

	reopened, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reopened.ClaimGranted("n_pending"), "the pending grant is gone")

	// Forgetting again resolves nothing to cancel.
	ids, _, err := edgeResolveNotInFleet("gentle-ibex-14")
	require.NoError(t, err)
	require.Empty(t, ids)
}

// Forgetting a device that CLAIMED its certificate but has not checked in yet (so it is not in the
// fleet and has no standing grant) must REVOKE that certificate - otherwise it could present the
// still-valid cert later and rejoin (audit 2026-09-24).
func TestForgetRevokesAClaimedButNotYetPresentDevice(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)

	// A device is adopted and claims its certificate (grant is consumed), but never presents.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := edgeauth.NodeID(pub)
	require.NoError(t, local.GrantClaim(id, "gentle-ibex-14"))
	req := edgeauth.NewClaimRequest("gentle-ibex-14", "mobile", pub, time.Now())
	req.Sign(priv)
	resp, err := local.Claim(req, time.Now())
	require.NoError(t, err)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	serial := leaf.SerialNumber.String()
	require.False(t, local.ClaimGranted(id), "the grant was consumed by the claim")

	// It is not in the fleet (never presented) and its grant is gone, so it is forgotten by its node
	// id (confirmed via --yes), and that revokes its certificate.
	_, err = captureEdgeStdout(func() error {
		return cmdEdgeForget(loadConfig(), []string{id, "--yes"})
	})
	require.NoError(t, err)

	reopened, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	revs, err := reopened.Revocations()
	require.NoError(t, err)
	require.Contains(t, revs, serial, "the claimed-but-not-present device's certificate is revoked")

	// A mistyped node id that matches nothing resolves nothing - it is NOT falsely reported as revoked.
	ids, _, err := edgeResolveNotInFleet("n_typodoesnotexist000000000000000000000000000")
	require.NoError(t, err)
	require.Empty(t, ids, "a node id with no grant and no issued cert is not 'forgotten'")
}

// Forgetting by a NAME that more than one pending adoption shares is ambiguous - it refuses and
// lists the node ids rather than silently forgetting them all (audit 2026-09-24).
func TestForgetRefusesAnAmbiguousName(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_one", "iPhone"))
	require.NoError(t, local.GrantClaim("n_two", "iPhone"))

	_, _, err = edgeResolveNotInFleet("iPhone")
	require.Error(t, err, "an ambiguous name must be refused, not applied to every match")
	require.Contains(t, err.Error(), "node id")
}

// Forgetting a not-in-fleet device is DESTRUCTIVE (it revokes a certificate / gives up an identity),
// so it must ask first, exactly as the in-fleet path does. Answering no leaves everything intact
// (audit 2026-09-24).
func TestForgetNotInFleetAsksBeforeRevoking(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_ask", "gentle-ibex-14"))

	// No --yes, and the prompt is answered "n".
	prev := edgeStdin
	t.Cleanup(func() { edgeStdin = prev })
	edgeStdin = strings.NewReader("n\n")
	out, err := captureEdgeStdout(func() error {
		return cmdEdgeForget(loadConfig(), []string{"gentle-ibex-14"})
	})
	require.NoError(t, err)
	require.Contains(t, out, "left alone")

	reopened, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reopened.ClaimGranted("n_ask"), "declining the prompt leaves the grant intact")
}

// A duplicate NAME in the fleet is ambiguous: edgeResolve must refuse and list the ids, not silently
// pick the first - a forget --yes on the wrong device is destructive (audit 2026-09-24).
func TestEdgeResolveRefusesADuplicateName(t *testing.T) {
	nodes := []store.EdgeNode{
		{ID: "n_aaa", Name: "iPhone"},
		{ID: "n_bbb", Name: "iPhone"},
	}
	_, ok, err := edgeResolve(nodes, "iPhone")
	require.False(t, ok)
	require.Error(t, err, "a name shared by two nodes must be refused")
	require.Contains(t, err.Error(), "more than one")
}
