package edgeauth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// ReAdmit is the OWNER's deliberate re-admission of a device that was forgotten or that left the
// Edge and wants back. It must make a fresh claim succeed again - the routine leave/forget -> rejoin
// flow the founder needs - WITHOUT weakening the security stance that a plain Revoke is final.

func TestReAdmitLetsAForgottenDeviceRejoin(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)

	// Adopt -> claim -> a live member.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	oldCert, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)

	// The owner FORGETS it (revokes its cert, clears the grant).
	_, _, err = local.Forget(id)
	require.NoError(t, err)

	// A plain re-grant is still refused - forget revoked its serial and left the history, so the
	// claim path treats it as final. (This is the security stance staying intact.)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.Error(t, err, "a plain re-grant after forget must NOT mint a fresh cert")

	// The owner deliberately RE-ADMITS it. Now a fresh claim succeeds with a NEW certificate.
	_, err = local.ReAdmit(id, "pixel-8")
	require.NoError(t, err)
	respB, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err, "after ReAdmit a deliberate re-adopt mints a fresh certificate")
	newCert, err := edgeauth.DecodeCert(respB.Cert)
	require.NoError(t, err)
	require.NotEqual(t, oldCert.SerialNumber.String(), newCert.SerialNumber.String(), "the re-admitted device gets a NEW serial")

	// The new cert authenticates under the root; the OLD serial stays revoked (a held copy is refused).
	gotID, err := local.Authority().Authenticate(newCert)
	require.NoError(t, err)
	require.Equal(t, id, gotID)
	revs, err := local.Revocations()
	require.NoError(t, err)
	require.Contains(t, revs, oldCert.SerialNumber.String(), "ReAdmit keeps the old certificate revoked")
}

func TestReAdmitDoesNotWeakenPlainRevokeFinality(t *testing.T) {
	// A plain Revoke (the security action, "this device is compromised") is still final: without an
	// explicit ReAdmit, a reappearing grant cannot mint a fresh cert.
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)

	_, err = local.Revoke(id)
	require.NoError(t, err)

	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted, "Revoke stays final until the owner explicitly ReAdmits")
}

func TestReAdmitRevokesAStillHeldCertBeforeReissuing(t *testing.T) {
	// Re-admitting a device that LEFT but was never forgotten (its old cert not yet revoked) must
	// still revoke that old cert - it is abandoned - before minting the new one, so a copy cannot
	// keep working (no double-issue).
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	oldCert, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)

	// No forget/revoke - the device simply left. The owner re-admits it.
	_, err = local.ReAdmit(id, "pixel-8")
	require.NoError(t, err)
	respB, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	newCert, err := edgeauth.DecodeCert(respB.Cert)
	require.NoError(t, err)

	revs, err := local.Revocations()
	require.NoError(t, err)
	require.Contains(t, revs, oldCert.SerialNumber.String(), "the abandoned old cert is revoked on re-admit")
	require.NotContains(t, revs, newCert.SerialNumber.String(), "the fresh cert is not revoked")
}

// ReAdmitRoutine re-admits ONLY a device that is not currently revoked - the routine rejoin - and
// refuses (ErrRevokedNeedsReadmit) a device the owner revoked, so a routine adopt never silently
// lifts a deliberate revocation. The check and the re-admit are one atomic lock.
func TestReAdmitRoutineGatesRevokedDevices(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)

	// A device that merely LEFT (not revoked): ReAdmitRoutine re-admits it.
	revoked, err := local.ReAdmitRoutine(id, "pixel-8")
	require.NoError(t, err)
	require.Empty(t, revoked, "nothing is revoked until the device proves its key (claim.feature, 2026-10-26)")
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err, "a routine re-admit lets it claim afresh")

	// Now REVOKE it (a deliberate security action). ReAdmitRoutine refuses, pointing to explicit readmit.
	_, err = local.Revoke(id)
	require.NoError(t, err)
	_, err = local.ReAdmitRoutine(id, "pixel-8")
	require.ErrorIs(t, err, edgeauth.ErrRevokedNeedsReadmit)

	// The deliberate ReAdmit still lifts it.
	_, err = local.ReAdmit(id, "pixel-8")
	require.NoError(t, err)
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err, "the explicit readmit lifts the revocation")
}

// claim.feature (2026-10-26): "re-adopting a member that joins by claim revokes its old certificate
// only when the device proves its key". A discovery advert is not authenticated, so the adopt that a
// forged advert can trigger must change nothing a real member depends on.
func TestARoutineReAdmitRevokesTheOldCertificateOnlyWhenTheDeviceClaims(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	var told []string
	local.OnRevoked = func(serials []string) { told = append(told, serials...) }
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	oldCert, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)
	old := oldCert.SerialNumber.String()

	_, err = local.ReAdmitRoutine(id, "pixel-8")
	require.NoError(t, err)
	revs, err := local.Revocations()
	require.NoError(t, err)
	require.NotContains(t, revs, old, "the adopt alone revokes nothing")
	require.True(t, local.ClaimGranted(id), "a claim is granted")

	respB, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	revs, err = local.Revocations()
	require.NoError(t, err)
	require.Contains(t, revs, old, "the device proved its key: the old certificate is revoked")
	require.Equal(t, []string{old}, told, "and the authority is told at once")
	newCert, err := edgeauth.DecodeCert(respB.Cert)
	require.NoError(t, err)
	require.NotEqual(t, old, newCert.SerialNumber.String())
	gotID, err := local.Authority().Authenticate(newCert)
	require.NoError(t, err, "the new certificate stands")
	require.Equal(t, id, gotID)
}

func TestARoutineReAdmitThatIsNeverClaimedChangesNothing(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	oldCert, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)

	_, err = local.ReAdmitRoutine(id, "pixel-8")
	require.NoError(t, err)
	late := time.Now().Add(edgeauth.GrantValidity + time.Minute)
	_, err = local.Claim(signedClaim(t, pub, priv, late), late)
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted, "the grant expired")
	revs, err := local.Revocations()
	require.NoError(t, err)
	require.NotContains(t, revs, oldCert.SerialNumber.String(), "the phone's certificate still stands")
	gotID, err := local.Authority().Authenticate(oldCert)
	require.NoError(t, err)
	require.Equal(t, id, gotID)
}

// The authority records how each node joined, so whether a member serves its own certificate is read
// from its own record, never from an advert (claim.feature, 2026-10-26).
func TestTheAuthorityRecordsHowANodeJoined(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.False(t, local.JoinedByEnrollment(id), "nothing recorded yet")
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	require.False(t, local.JoinedByEnrollment(id), "a device that claimed did not enroll")
	require.False(t, local.JoinedByEnrollment("n_never"))
}
