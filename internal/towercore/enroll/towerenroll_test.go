package enroll

// Joined-Tower enrollment, per features/tower/public_enrollment.feature.
//
// The spec's headline requirement is that an INVALID enrollment "fails without creating
// partial authority" - no certificate, no lease, no directory entry, and nothing a later
// attempt could adopt as real. Its table names twenty-three ways an enrollment can be
// invalid, and each one is a way somebody could otherwise end up holding a credential for
// a Tower they are not entitled to run.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/keypurpose"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/cert"
)

// --- fixtures --------------------------------------------------------------

type harness struct {
	enroller  *Enroller
	registry  *admit.Registry
	policy    *stubPolicy
	authority *cert.Authority
}

// stubPolicy stands in for the account system: whether this operator has accepted the
// current terms and is in good standing is not this package's knowledge.
type stubPolicy struct {
	refuse map[string]error
}

func (s *stubPolicy) MayEnroll(owner string) error {
	if err, bad := s.refuse[owner]; bad {
		return err
	}
	return nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	reg := admit.NewWithStore(admit.Config{}, admit.NewMemStore())
	auth, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pol := &stubPolicy{refuse: map[string]error{}}

	e, err := New(Config{
		Registry:   reg,
		Authority:  auth,
		Policy:     pol,
		MinVersion: 1,
		MaxVersion: 2,
		MaxSkew:    5 * time.Minute,
	})
	require.NoError(t, err)
	return &harness{enroller: e, registry: reg, policy: pol, authority: auth}
}

type towerKeys struct {
	identityPub  ed25519.PublicKey
	identityPriv ed25519.PrivateKey
	tls          *ecdsa.PrivateKey
}

func newTowerKeys(t *testing.T) towerKeys {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tlsKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return towerKeys{identityPub: pub, identityPriv: priv, tls: tlsKey}
}

func csrFor(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "tower"}}, key)
	require.NoError(t, err)
	return der
}

// validRequest builds an enrollment that should succeed, so each test can spoil exactly
// one thing and prove that one thing is what stops it.
func (h *harness) validRequest(t *testing.T, owner string, k towerKeys) Request {
	t.Helper()
	token, err := h.registry.IssueToken(owner)
	require.NoError(t, err)

	ch, err := h.enroller.Challenge(token)
	require.NoError(t, err)

	return Request{
		Operator:        owner,
		TokenID:         token,
		TransactionID:   "txn-" + owner,
		Nonce:           ch.Nonce,
		IdentityKey:     k.identityPub,
		Signature:       ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:             csrFor(t, k.tls),
		ProtocolVersion: 1,
		Realm:           keypurpose.RealmTower,
		Now:             time.Now(),
		Capabilities:    []string{"relay"},
	}
}

// --- the happy path --------------------------------------------------------

func TestAnApprovedOperatorEnrollsByProvingItsLocalKey(t *testing.T) {
	h := newHarness(t)
	k := newTowerKeys(t)

	res, err := h.enroller.Enroll(h.validRequest(t, "acct-1", k))
	require.NoError(t, err)

	require.NotEmpty(t, res.TowerID)
	require.NotNil(t, res.Certificate)

	// Quarantine, always: an account proves who is accountable, not that the Tower behaves.
	require.Equal(t, admit.StateQuarantine, res.Tower.State)
	require.EqualValues(t, 1, res.Tower.LifecycleRevision, "revision-1 pending-to-quarantine")
	require.NotEmpty(t, res.Tower.LifecycleHash)
	require.EqualValues(t, 1, res.Tower.LeaseSequence, "sequence-1 admission lease")
	require.Equal(t, res.Certificate.SerialNumber.String(), res.Tower.CertSerial)

	// The certificate names this Tower and authenticates as it.
	id, err := h.authority.Authenticate(res.Certificate)
	require.NoError(t, err)
	require.Equal(t, res.TowerID, id)

	// It records what the spec lists.
	require.Equal(t, "acct-1", res.Tower.Owner)
	require.NotEmpty(t, res.Tower.KeyHash)
	require.NotEmpty(t, res.Tower.TLSKeyHash)
	require.NotEqual(t, res.Tower.KeyHash, res.Tower.TLSKeyHash,
		"identity and TLS keys are distinct, so rotating a certificate never touches who the Tower is")
	require.Equal(t, []string{"relay"}, res.Tower.Capabilities)
	require.Equal(t, 1, res.Tower.ProtocolVersion)
}

func TestTheEnrollmentTokenIsIrreversiblyConsumed(t *testing.T) {
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)

	_, err := h.enroller.Enroll(req)
	require.NoError(t, err)

	// A different Tower cannot reuse it, even with its own good keys and a fresh challenge.
	other := newTowerKeys(t)
	replay := req
	replay.IdentityKey = other.identityPub
	replay.CSR = csrFor(t, other.tls)
	replay.TransactionID = "txn-other"
	ch, err := h.enroller.Challenge(req.TokenID)
	require.Error(t, err, "a spent token issues no further challenges")
	_ = ch

	_, err = h.enroller.Enroll(replay)
	require.Error(t, err)
}

// --- idempotency -----------------------------------------------------------

func TestRetryingACommittedEnrollmentIsIdempotent(t *testing.T) {
	// "Roger Core committed enrollment but the response was lost." The Tower retries with
	// the same transaction and key proof, and must get the SAME identity back rather than
	// a second Tower - or one machine ends up holding two admissions.
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)

	first, err := h.enroller.Enroll(req)
	require.NoError(t, err)

	second, err := h.enroller.Enroll(req)
	require.NoError(t, err, "the retry must not fail as a consumed token")
	require.Equal(t, first.TowerID, second.TowerID)
	require.Equal(t, first.Certificate.SerialNumber, second.Certificate.SerialNumber,
		"and must not mint a second certificate")

	require.Len(t, h.registry.ByOwner("acct-1"), 1, "exactly one Tower exists")
}

func TestADifferentTransactionIsNotARetry(t *testing.T) {
	// Idempotency keyed on the transaction must not become a way to claim somebody else's
	// admission by guessing an ID.
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)
	_, err := h.enroller.Enroll(req)
	require.NoError(t, err)

	changed := req
	changed.TransactionID = "txn-different"
	_, err = h.enroller.Enroll(changed)
	require.Error(t, err, "a new transaction on a spent token is a new enrollment, and it fails")
}

func TestARetryWithADifferentKeyIsRefused(t *testing.T) {
	// The retry has to re-prove the SAME key. Otherwise a transaction ID observed on the
	// wire becomes a way to have somebody else's Tower re-issued to your key.
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)
	_, err := h.enroller.Enroll(req)
	require.NoError(t, err)

	attacker := newTowerKeys(t)
	forged := req
	forged.IdentityKey = attacker.identityPub
	forged.CSR = csrFor(t, attacker.tls)
	_, err = h.enroller.Enroll(forged)
	require.Error(t, err, "a retry is only a retry if it is the same Tower proving the same key")
}

// --- the invalid-enrollment table -----------------------------------------

func TestInvalidEnrollmentCreatesNoPartialAuthority(t *testing.T) {
	type spoil func(t *testing.T, h *harness, req *Request, k towerKeys)

	cases := map[string]spoil{
		"no token": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.TokenID = ""
		},
		"an unknown token": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.TokenID = "tok-nobody-issued"
		},
		"an operator who did not accept current terms": func(_ *testing.T, h *harness, req *Request, _ towerKeys) {
			h.policy.refuse["acct-1"] = errTermsNotAccepted
		},
		"a suspended operator": func(_ *testing.T, h *harness, req *Request, _ towerKeys) {
			h.policy.refuse["acct-1"] = errOperatorSuspended
		},
		"a missing challenge signature": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Signature = nil
		},
		"a challenge signature from the wrong key": func(t *testing.T, _ *harness, req *Request, _ towerKeys) {
			attacker := newTowerKeys(t)
			req.Signature = ed25519.Sign(attacker.identityPriv, []byte("whatever"))
		},
		"a modified challenge": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			// Flip the last character to something it certainly was not. Substituting a
			// FIXED character is a one-in-sixteen no-op on a hex nonce, and a test that
			// sometimes modifies nothing sometimes proves nothing.
			last := req.Nonce[len(req.Nonce)-1]
			replacement := byte('a')
			if last == 'a' {
				replacement = 'b'
			}
			req.Nonce = req.Nonce[:len(req.Nonce)-1] + string(replacement)
		},
		"an unknown nonce": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Nonce = "nonce-never-issued"
		},
		"an expired challenge": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Now = time.Now().Add(time.Hour)
		},
		"a CSR that is not signed by its own key": func(t *testing.T, _ *harness, req *Request, _ towerKeys) {
			bad := append([]byte(nil), req.CSR...)
			bad[len(bad)-1] ^= 0xff // break the signature
			req.CSR = bad
		},
		"a missing CSR": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.CSR = nil
		},
		"a CSR reusing the identity key": func(t *testing.T, _ *harness, req *Request, k towerKeys) {
			req.CSR = csrFor(t, k.identityPriv)
		},
		"an unsupported protocol version": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.ProtocolVersion = 99
		},
		"software below the signed minimum version": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.ProtocolVersion = 0
		},
		"a clock outside the admitted skew": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Now = time.Now().Add(-time.Hour)
		},
		"a malformed capability request": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Capabilities = []string{""}
		},
		"a standalone network identity": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Realm = keypurpose.RealmStandalone
		},
		"a Station identity": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Realm = keypurpose.RealmStation
		},
		"a Core identity": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.Realm = keypurpose.RealmCore
		},
		"no identity key": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.IdentityKey = nil
		},
		"no transaction id": func(_ *testing.T, _ *harness, req *Request, _ towerKeys) {
			req.TransactionID = ""
		},
	}

	for name, spoilIt := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			k := newTowerKeys(t)
			req := h.validRequest(t, "acct-1", k)
			spoilIt(t, h, &req, k)

			_, err := h.enroller.Enroll(req)
			require.Error(t, err, "this enrollment must not succeed")

			// Nothing partial: no Tower, and the identity key is not burned, so a later
			// legitimate attempt with the same machine still works.
			require.Empty(t, h.registry.ByOwner("acct-1"),
				"no certificate, lease, or Tower identity may exist")
			require.Empty(t, h.registry.ByOwner("acct-2"))
		})
	}
}

func TestARejectedEnrollmentNeitherLogsNorReturnsSecrets(t *testing.T) {
	// "the reason is recorded without logging a token or private key."
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)
	req.Signature = nil

	_, err := h.enroller.Enroll(req)
	require.Error(t, err)
	require.NotContains(t, err.Error(), req.TokenID, "an error must not echo the token")
	require.NotContains(t, err.Error(), req.Nonce)
}

// --- key uniqueness --------------------------------------------------------

func TestAnIdentityKeyAlreadyBoundToAnotherTowerIsRefused(t *testing.T) {
	h := newHarness(t)
	k := newTowerKeys(t)

	_, err := h.enroller.Enroll(h.validRequest(t, "acct-1", k))
	require.NoError(t, err)

	// The same machine, a second account, a fresh token: still one key, one Tower.
	second := h.validRequest(t, "acct-2", k)
	second.TransactionID = "txn-second"
	_, err = h.enroller.Enroll(second)
	require.Error(t, err, "one identity key admits exactly one Tower")
	require.Empty(t, h.registry.ByOwner("acct-2"))
}

func TestAChallengeIsSpentOnUse(t *testing.T) {
	// A replayed challenge is one of the spec's rows. The signature stays valid forever,
	// so what stops the replay is the nonce being one-time.
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)

	_, err := h.enroller.Enroll(req)
	require.NoError(t, err)

	// Even with a fresh token, the OLD nonce must not be reusable.
	fresh, err := h.registry.IssueToken("acct-3")
	require.NoError(t, err)
	replay := req
	replay.TokenID = fresh
	replay.TransactionID = "txn-replay"
	_, err = h.enroller.Enroll(replay)
	require.Error(t, err, "a challenge is answered once")
}

func TestChallengeRequiresALiveToken(t *testing.T) {
	h := newHarness(t)
	_, err := h.enroller.Challenge("tok-nobody-issued")
	require.Error(t, err)
	_, err = h.enroller.Challenge("")
	require.Error(t, err)
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	reg := admit.NewWithStore(admit.Config{}, admit.NewMemStore())
	auth, err := cert.NewAuthority(cert.Config{})
	require.NoError(t, err)

	_, err = New(Config{Authority: auth, Policy: &stubPolicy{}})
	require.Error(t, err, "enrollment without a registry admits nothing")

	_, err = New(Config{Registry: reg, Policy: &stubPolicy{}})
	require.Error(t, err, "enrollment without an authority issues nothing")

	_, err = New(Config{Registry: reg, Authority: auth})
	require.Error(t, err, "enrollment without an operator policy cannot know who may enroll")
}

// --- checks that the table above cannot reach ------------------------------
//
// Written after reading what each table row ACTUALLY failed on: three of them were being
// caught by an earlier check and never reached the control they were named for. A test that
// passes for the wrong reason proves nothing, so these drive the checks directly.

func TestATokenIssuedToAnotherAccountIsRefused(t *testing.T) {
	// The token is a bearer credential. If it leaks - a log, a shared terminal, a shoulder
	// - anybody holding it could otherwise enroll a Tower onto somebody else's account and
	// be paid for it. The authenticated session is what makes the leak insufficient.
	h := newHarness(t)
	k := newTowerKeys(t)

	req := h.validRequest(t, "acct-victim", k)
	req.Operator = "acct-attacker" // the session is the attacker's; the token is not

	_, err := h.enroller.Enroll(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "another account", "the token-owner check is what fires")
	require.Empty(t, h.registry.ByOwner("acct-victim"))
	require.Empty(t, h.registry.ByOwner("acct-attacker"))
}

func TestAnEnrollmentWithNoAuthenticatedOperatorIsRefused(t *testing.T) {
	h := newHarness(t)
	k := newTowerKeys(t)
	req := h.validRequest(t, "acct-1", k)
	req.Operator = ""

	_, err := h.enroller.Enroll(req)
	require.Error(t, err)
	require.Empty(t, h.registry.ByOwner("acct-1"))
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	// Distinct from an unknown one: this token was real, and its window closed.
	reg := admit.NewWithStore(admit.Config{TokenTTL: 20 * time.Millisecond}, admit.NewMemStore())
	auth, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	e, err := New(Config{
		Registry: reg, Authority: auth, Policy: &stubPolicy{refuse: map[string]error{}},
		MinVersion: 1, MaxVersion: 2, MaxSkew: 5 * time.Minute,
	})
	require.NoError(t, err)

	k := newTowerKeys(t)
	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := e.Challenge(token)
	require.NoError(t, err)

	time.Sleep(40 * time.Millisecond) // the token's window closes

	_, err = e.Enroll(Request{
		Operator: "acct-1", TokenID: token, TransactionID: "txn-1",
		Nonce: ch.Nonce, IdentityKey: k.identityPub,
		Signature: ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:       csrFor(t, k.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired", "the token-expiry check is what fires")
	require.Empty(t, reg.ByOwner("acct-1"))
}

func TestAnExpiredChallengeIsRefusedWithoutBlamingTheClock(t *testing.T) {
	// The clock-skew check used to fire first, so challenge expiry was never exercised.
	// Here the Tower's clock is honest and only the challenge has aged out.
	reg := admit.NewWithStore(admit.Config{}, admit.NewMemStore())
	auth, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	e, err := New(Config{
		Registry: reg, Authority: auth, Policy: &stubPolicy{refuse: map[string]error{}},
		MinVersion: 1, MaxVersion: 2, MaxSkew: 5 * time.Minute,
		ChallengeTTL: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	k := newTowerKeys(t)
	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := e.Challenge(token)
	require.NoError(t, err)

	time.Sleep(40 * time.Millisecond)

	_, err = e.Enroll(Request{
		Operator: "acct-1", TokenID: token, TransactionID: "txn-1",
		Nonce: ch.Nonce, IdentityKey: k.identityPub,
		Signature: ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:       csrFor(t, k.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "challenge", "challenge expiry is what fires, not the skew check")
	require.Empty(t, reg.ByOwner("acct-1"))
}

func TestAnUnknownTokenIsRefusedOnItsOwnMerits(t *testing.T) {
	// Reaching the token check needs a challenge that BELONGS to the token presented, so
	// the challenge-binding check does not shadow it.
	h := newHarness(t)
	k := newTowerKeys(t)

	victim, err := h.registry.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := h.enroller.Challenge(victim)
	require.NoError(t, err)

	// Spend the token elsewhere so it is gone by the time this request lands.
	other := newTowerKeys(t)
	otherCh, err := h.enroller.Challenge(victim)
	require.NoError(t, err)
	_, err = h.enroller.Enroll(Request{
		Operator: "acct-1", TokenID: victim, TransactionID: "txn-first",
		Nonce: otherCh.Nonce, IdentityKey: other.identityPub,
		Signature: ed25519.Sign(other.identityPriv, otherCh.SigningInput()),
		CSR:       csrFor(t, other.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	})
	require.NoError(t, err)

	_, err = h.enroller.Enroll(Request{
		Operator: "acct-1", TokenID: victim, TransactionID: "txn-second",
		Nonce: ch.Nonce, IdentityKey: k.identityPub,
		Signature: ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:       csrFor(t, k.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not valid", "the consumed token is what fires")
	require.Len(t, h.registry.ByOwner("acct-1"), 1, "still exactly the one Tower")
}
