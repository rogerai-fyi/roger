package enroll

// Certificate renewal, per features/tower/public_enrollment.feature (certificate
// rotation) and the approved relay-link design.
//
// Certificates are deliberately short-lived, which is only safe if renewal is boring: it
// happens on the connection the Tower already holds, at two thirds of lifetime, with no
// operator involved. That last part is a security property as much as a convenience - an
// operator who is never asked to re-authenticate a Tower has no habit for a phishing mail
// to exploit.
//
// RENEWAL IS NOT A SECOND ADMISSION. It spends no token, consumes no quota, and creates no
// Tower. It re-proves possession of the identity key already on record and issues against
// the SAME Tower ID. Everything below exists to keep those two things from drifting apart.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/keypurpose"
	"rogerai.fm/roger/v5/internal/towercore/admit"
)

// admitted enrolls a Tower and returns it with the keys it holds.
func admitted(t *testing.T, h *harness, owner string) (Result, towerKeys) {
	t.Helper()
	k := newTowerKeys(t)
	res, err := h.enroller.Enroll(h.validRequest(t, owner, k))
	require.NoError(t, err)
	return res, k
}

// renewal builds a valid renewal for an admitted Tower.
func renewal(t *testing.T, h *harness, res Result, k towerKeys, newChannel *towerKeys) RenewRequest {
	t.Helper()
	ch, err := h.enroller.RenewChallenge(res.TowerID)
	require.NoError(t, err)

	channel := k
	if newChannel != nil {
		channel = *newChannel
	}
	return RenewRequest{
		TowerID:     res.TowerID,
		Nonce:       ch.Nonce,
		IdentityKey: k.identityPub,
		Signature:   ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:         csrFor(t, channel.tls),
		Now:         time.Now(),
	}
}

func TestARenewalIssuesAFreshCertificateForTheSameTower(t *testing.T) {
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	out, err := h.enroller.Renew(renewal(t, h, res, k, nil))
	require.NoError(t, err)

	require.Equal(t, res.TowerID, out.TowerID, "renewal never renames a Tower")
	require.NotEqual(t, res.Certificate.SerialNumber, out.Certificate.SerialNumber,
		"a renewal is a new certificate, not the old one handed back")

	id, err := h.authority.Authenticate(out.Certificate)
	require.NoError(t, err)
	require.Equal(t, res.TowerID, id)

	// The registry now names the new serial, which is what revocation would act on.
	tw, ok := h.registry.Get(res.TowerID)
	require.True(t, ok)
	require.Equal(t, out.Certificate.SerialNumber.String(), tw.CertSerial)
}

func TestTheOldCertificateKeepsWorkingUntilItExpires(t *testing.T) {
	// Overlap is the whole point of renewing early. Revoking the old certificate at
	// renewal would cut the live connection the renewal arrived on.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	_, err := h.enroller.Renew(renewal(t, h, res, k, nil))
	require.NoError(t, err)

	_, err = h.authority.Authenticate(res.Certificate)
	require.NoError(t, err, "the Tower is mid-rotation and still holds a live link on the old one")
}

func TestRenewalRotatesTheChannelKeyWithoutTouchingTheIdentity(t *testing.T) {
	// The reason the two keys are separate: rotating a certificate must never rename the
	// Tower, and a stolen channel key must not become proof of who the Tower is.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	fresh := newTowerKeys(t)
	out, err := h.enroller.Renew(renewal(t, h, res, k, &fresh))
	require.NoError(t, err)

	tw, ok := h.registry.Get(res.TowerID)
	require.True(t, ok)
	require.Equal(t, res.Tower.KeyHash, tw.KeyHash, "the identity is unchanged")
	require.NotEqual(t, res.Tower.TLSKeyHash, tw.TLSKeyHash, "the channel key rotated")
	require.Equal(t, res.TowerID, out.TowerID)
}

func TestRenewalSpendsNoTokenAndConsumesNoQuota(t *testing.T) {
	// A renewal that consumed quota would eventually lock an operator out of their own
	// Tower for the crime of staying connected.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	for i := 0; i < 3; i++ {
		_, err := h.enroller.Renew(renewal(t, h, res, k, nil))
		require.NoError(t, err, "renewal %d", i)
	}
	require.Len(t, h.registry.ByOwner("acct-1"), 1, "renewing never creates another Tower")
}

// --- what a renewal must refuse -------------------------------------------

func TestARenewalWithADifferentIdentityKeyIsRefused(t *testing.T) {
	// This is the one that matters most. If a renewal could present a NEW identity key,
	// anybody who learned a Tower ID could have a certificate for it issued to themselves.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	attacker := newTowerKeys(t)
	req := renewal(t, h, res, k, nil)
	ch, err := h.enroller.RenewChallenge(res.TowerID)
	require.NoError(t, err)
	req.Nonce = ch.Nonce
	req.IdentityKey = attacker.identityPub
	req.Signature = ed25519.Sign(attacker.identityPriv, ch.SigningInput())

	_, err = h.enroller.Renew(req)
	require.Error(t, err, "a renewal re-proves the identity already on record, or it is not a renewal")

	tw, _ := h.registry.Get(res.TowerID)
	require.Equal(t, res.Tower.KeyHash, tw.KeyHash, "and nothing about the Tower changed")
}

func TestARenewalForAnUnknownTowerIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := h.enroller.RenewChallenge("tw-nobody-admitted")
	require.Error(t, err)
}

func TestARevokedTowerCannotRenewItsWayBack(t *testing.T) {
	// Revocation is terminal. A certificate issued after it would put an abusive operator
	// straight back on the network with a credential we just took away.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	req := renewal(t, h, res, k, nil) // taken while still healthy
	require.NoError(t, h.registry.Transition(res.TowerID, admit.StateRevoked))

	_, err := h.enroller.Renew(req)
	require.Error(t, err)

	_, err = h.enroller.RenewChallenge(res.TowerID)
	require.Error(t, err, "and it cannot even take a fresh challenge")
}

func TestAnExpiredTowerCannotRenew(t *testing.T) {
	// An expired lease is re-admitted through quarantine on fresh proof and fresh probes.
	// Renewing one would route around that control entirely.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	req := renewal(t, h, res, k, nil)

	tw, ok := h.registry.Get(res.TowerID)
	require.True(t, ok)
	require.NoError(t, h.registry.ExpireLease(tw.ID))
	require.NoError(t, h.registry.Expire(tw.ID))

	_, err := h.enroller.Renew(req)
	require.Error(t, err)
}

func TestARenewalChallengeIsSpentOnce(t *testing.T) {
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	req := renewal(t, h, res, k, nil)

	_, err := h.enroller.Renew(req)
	require.NoError(t, err)

	_, err = h.enroller.Renew(req)
	require.Error(t, err, "a replayed renewal is still a replay")
}

func TestAnEnrollmentChallengeCannotBeUsedToRenew(t *testing.T) {
	// Domain separation. Without it, a challenge taken for one purpose is a signature that
	// satisfies the other, and the two flows stop being independent.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	token, err := h.registry.IssueToken("acct-1")
	require.NoError(t, err)
	enrollCh, err := h.enroller.Challenge(token)
	require.NoError(t, err)

	_, err = h.enroller.Renew(RenewRequest{
		TowerID:     res.TowerID,
		Nonce:       enrollCh.Nonce,
		IdentityKey: k.identityPub,
		Signature:   ed25519.Sign(k.identityPriv, enrollCh.SigningInput()),
		CSR:         csrFor(t, k.tls),
		Now:         time.Now(),
	})
	require.Error(t, err, "an enrollment challenge is not a renewal challenge")
}

func TestARenewalChallengeCannotBeUsedToEnroll(t *testing.T) {
	h := newHarness(t)
	res, _ := admitted(t, h, "acct-1")
	ch, err := h.enroller.RenewChallenge(res.TowerID)
	require.NoError(t, err)

	token, err := h.registry.IssueToken("acct-2")
	require.NoError(t, err)
	fresh := newTowerKeys(t)

	_, err = h.enroller.Enroll(Request{
		Operator: "acct-2", TokenID: token, TransactionID: "txn-x",
		Nonce: ch.Nonce, IdentityKey: fresh.identityPub,
		Signature: ed25519.Sign(fresh.identityPriv, ch.SigningInput()),
		CSR:       csrFor(t, fresh.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	})
	require.Error(t, err, "and the reverse must not work either")
	require.Empty(t, h.registry.ByOwner("acct-2"))
}

func TestARenewalReusingTheIdentityKeyAsTheChannelKeyIsRefused(t *testing.T) {
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	req := renewal(t, h, res, k, nil)
	req.CSR = csrFor(t, k.identityPriv)

	_, err := h.enroller.Renew(req)
	require.Error(t, err, "one key doing both jobs is what the two-key split exists to prevent")
}

func TestARenewalWithAClockOutsideTheSkewIsRefused(t *testing.T) {
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")

	req := renewal(t, h, res, k, nil)
	req.Now = time.Now().Add(-time.Hour)

	_, err := h.enroller.Renew(req)
	require.Error(t, err)
}

func TestRenewalIsRateLimitedPerTower(t *testing.T) {
	// A Tower that could renew in a loop would mint unbounded live certificates, each one
	// valid until its own expiry and each one a credential that has to be tracked.
	h := newHarness(t)
	h.enroller.cfg.MinRenewInterval = time.Hour
	res, k := admitted(t, h, "acct-1")

	_, err := h.enroller.Renew(renewal(t, h, res, k, nil))
	require.NoError(t, err)

	_, err = h.enroller.Renew(renewal(t, h, res, k, nil))
	require.Error(t, err, "renewing again immediately is refused")
}

func TestARenewalWithNoCSRIsRefused(t *testing.T) {
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	req := renewal(t, h, res, k, nil)
	req.CSR = nil

	_, err := h.enroller.Renew(req)
	require.Error(t, err)
}

func TestARenewalRefusalLeavesTheTowerExactlyAsItWas(t *testing.T) {
	// Nothing partial: a refused renewal must not leave a half-rotated Tower whose
	// registry serial names a certificate nobody holds.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	before, _ := h.registry.Get(res.TowerID)

	bad := renewal(t, h, res, k, nil)
	bad.Signature = nil
	_, err := h.enroller.Renew(bad)
	require.Error(t, err)

	after, _ := h.registry.Get(res.TowerID)
	require.Equal(t, before.CertSerial, after.CertSerial)
	require.Equal(t, before.TLSKeyHash, after.TLSKeyHash)
	require.Equal(t, before.KeyHash, after.KeyHash)
}

func TestRenewalKeepsALiveTowersLeaseFromLapsing(t *testing.T) {
	// A Tower that is connected and healthy should not lose its lease for the crime of
	// staying up. Renewal is the natural moment to extend it.
	h := newHarness(t)
	res, k := admitted(t, h, "acct-1")
	before, _ := h.registry.Get(res.TowerID)

	time.Sleep(2 * time.Millisecond)
	_, err := h.enroller.Renew(renewal(t, h, res, k, nil))
	require.NoError(t, err)

	after, _ := h.registry.Get(res.TowerID)
	require.True(t, after.LeaseExpires.After(before.LeaseExpires),
		"a renewing Tower's lease moves forward with it")
}
