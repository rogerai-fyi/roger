package keypurpose

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Purpose-separated keys. Contract: features/tower/key_separation.feature.
//
// The whole point: every signature and secret has one named purpose, so compromising a
// relay, cookie, pseudonym, admin channel, or signer cannot silently become settlement
// authority. Everything later in Phase 2 - certificates, dispatch leases, execution
// grants, settlement - rests on this, which is why it comes before any of them.

const specFile = "../../features/tower/key_separation.feature"

// specRoles reads the role table out of the approved feature file. Reading it rather than
// restating it is deliberate: a hand-copied list drifts from the spec silently, and this
// is exactly the kind of enumeration where a quietly missing row is a missing control.
func specRoles(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(specFile)
	require.NoError(t, err)
	defer f.Close()

	var roles []string
	inTable := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "Then every role below resolves to a distinct key identity:"):
			inTable = true
		case inTable && strings.HasPrefix(line, "|"):
			role := strings.TrimSpace(strings.Trim(line, "|"))
			if role != "role" {
				roles = append(roles, role)
			}
		case inTable:
			inTable = false
		}
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, roles, "the spec's role table must be readable, or this suite proves nothing")
	return roles
}

func TestEveryRoleNamedByTheSpecIsAKnownPurpose(t *testing.T) {
	for _, role := range specRoles(t) {
		p, ok := Lookup(role)
		require.True(t, ok, "the spec names role %q; the keyring does not know it", role)
		require.True(t, Known(p), "%s must be a known purpose", p)
	}
}

// The reverse direction: a purpose the keyring invents but the spec never named would be
// unreviewed authority.
func TestEveryKnownPurposeIsNamedBySpec(t *testing.T) {
	named := map[string]bool{}
	for _, role := range specRoles(t) {
		p, ok := Lookup(role)
		require.True(t, ok)
		named[string(p)] = true
	}
	for _, p := range AllPurposes() {
		require.True(t, named[string(p)], "purpose %q is not named by the approved spec", p)
	}
}

// --- distinctness at startup ----------------------------------------------

func TestAFullyPopulatedRingValidates(t *testing.T) {
	r := testRing(t)
	require.NoError(t, r.Validate())
}

// Startup must fail before anything is signed. A ring that is missing a role and validates
// anyway would let a service reach the first signature and only then discover it has no
// authority for it.
func TestValidateFailsWhenARoleIsMissing(t *testing.T) {
	r := testRing(t)
	r.remove(PurposeSettlementSigner)
	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), string(PurposeSettlementSigner))
}

// One root compromise must not be able to disguise itself as many roles.
func TestValidateRejectsSharedKeyMaterialAcrossRoles(t *testing.T) {
	for name, collide := range map[string]func(*Ring){
		"same raw key": func(r *Ring) {
			k := r.keys[PurposeSettlementSigner]
			dup := *k
			dup.Purpose = PurposeExecutionGrantSigner
			r.keys[PurposeExecutionGrantSigner] = &dup
		},
		"same alias": func(r *Ring) {
			r.keys[PurposeSettlementSigner].Alias = "shared/kms/alias"
			r.keys[PurposeExecutionGrantSigner].Alias = "shared/kms/alias"
		},
		"same derived root": func(r *Ring) {
			r.keys[PurposeSettlementSigner].DerivedFrom = "one-root-secret"
			r.keys[PurposePayoutAuthorization].DerivedFrom = "one-root-secret"
		},
		"same fallback": func(r *Ring) {
			r.keys[PurposeSettlementSigner].Fallback = "emergency-key"
			r.keys[PurposeDispatchLeaseSigner].Fallback = "emergency-key"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := testRing(t)
			collide(r)
			err := r.Validate()
			require.Error(t, err, "startup must fail before signing, settlement, or payout")
			// The error names the conflict so an operator can fix it, and names no secret.
			require.Contains(t, err.Error(), string(PurposeSettlementSigner))
			requireNoSecrets(t, r, err.Error())
		})
	}
}

// --- the Cartesian invariant ----------------------------------------------

// A valid signature from the wrong role is rejected. This is the property the whole
// package exists for, so it is asserted over every ordered pair rather than sampled -
// sampling a transition table is exactly what let four wrong edges through in
// internal/toweradmit.
func TestEveryOrderedPairOfDistinctRolesIsRejected(t *testing.T) {
	r := testRing(t)
	msg := []byte("an object requiring one exact purpose")

	// Signing roles. The symmetric ones are covered by the matching Cartesian test in
	// symmetric_test.go, and the two kinds are proven not to interchange there too.
	for _, required := range signingPurposes() {
		for _, presented := range signingPurposes() {
			sig, err := r.Sign(presented, msg)
			require.NoError(t, err)

			err = r.Verify(required, msg, sig)
			if required == presented {
				require.NoError(t, err, "%s must verify its own signature", required)
				continue
			}
			require.ErrorIs(t, err, ErrPurposeMismatch,
				"a valid %s signature must not satisfy %s", presented, required)
		}
	}
}

// The substitutions the spec calls out by name, including both directions of each. These
// are covered by the exhaustive test above; they are restated because these are the pairs
// with a stated reason, and a reader should see them.
func TestTheNamedSubstitutionsAreRejectedBothWays(t *testing.T) {
	r := testRing(t)
	msg := []byte("payload")

	for _, pair := range [][2]Purpose{
		{PurposeTowerCertificateIssuer, PurposeSettlementSigner},
		{PurposeTowerCertificateIssuer, PurposeExecutionGrantSigner},
		{PurposeExecutionGrantSigner, PurposeRogerCoreTLSServiceIdentity},
		{PurposeSettlementSigner, PurposeRogerCoreTLSServiceIdentity},
		{PurposeDispatchLeaseSigner, PurposeExecutionGrantSigner},
		{PurposeSettlementSigner, PurposeCompensationLedgerSigner},
		{PurposeCompensationLedgerHeadSigner, PurposeCompensationLedgerSigner},
		{PurposePayoutAuthorization, PurposeCompensationLedgerHeadSigner},
		{PurposeAdmissionLeaseSigner, PurposeTowerLifecycleSigner},
		{PurposePublicDirectorySigner, PurposeTrustDocumentSigner},
	} {
		for _, o := range [][2]Purpose{{pair[0], pair[1]}, {pair[1], pair[0]}} {
			sig, err := r.Sign(o[1], msg)
			require.NoError(t, err)
			require.ErrorIs(t, r.Verify(o[0], msg, sig), ErrPurposeMismatch,
				"a %s key must not exercise %s", o[1], o[0])
		}
	}
}

// A signature must still be checked, not merely labelled. Otherwise the purpose tag would
// be the only gate and anyone could assert it.
func TestATamperedMessageOrSignatureIsRejected(t *testing.T) {
	r := testRing(t)
	sig, err := r.Sign(PurposeSettlementSigner, []byte("pay 100"))
	require.NoError(t, err)

	require.Error(t, r.Verify(PurposeSettlementSigner, []byte("pay 900"), sig))

	forged := sig
	forged.Sig = strings.Repeat("00", len(forged.Sig)/2)
	require.Error(t, r.Verify(PurposeSettlementSigner, []byte("pay 100"), forged))
}

// An unknown purpose is refused rather than treated as some permissive default.
func TestAnUnknownPurposeCannotSignOrVerify(t *testing.T) {
	r := testRing(t)
	_, err := r.Sign(Purpose("root"), []byte("x"))
	require.ErrorIs(t, err, ErrUnknownPurpose)

	sig, err := r.Sign(PurposeSettlementSigner, []byte("x"))
	require.NoError(t, err)
	require.ErrorIs(t, r.Verify(Purpose("root"), []byte("x"), sig), ErrUnknownPurpose)
}

// --- fail closed -----------------------------------------------------------

// A missing key must stop the behavior that needs it and nothing else - and must never
// mint a replacement or reach for another role's key.
func TestAMissingKeyFailsClosedWithoutMintingOrBorrowing(t *testing.T) {
	r := testRing(t)
	before := r.keys[PurposeExecutionGrantSigner].KeyID
	r.remove(PurposeExecutionGrantSigner)

	_, err := r.Sign(PurposeExecutionGrantSigner, []byte("grant"))
	require.ErrorIs(t, err, ErrKeyUnavailable, "no new joined job can be issued")

	// Nothing was generated to paper over the gap.
	_, ok := r.keys[PurposeExecutionGrantSigner]
	require.False(t, ok, "a missing production authority must never be silently regenerated")

	// And an unrelated role still works: failure is scoped, not global.
	_, err = r.Sign(PurposeSettlementSigner, []byte("settle"))
	require.NoError(t, err)

	// A signature made by another role must not verify as the missing one.
	sig, err := r.Sign(PurposeSettlementSigner, []byte("grant"))
	require.NoError(t, err)
	require.Error(t, r.Verify(PurposeExecutionGrantSigner, []byte("grant"), sig))
	require.NotEmpty(t, before)
}

// --- rotation --------------------------------------------------------------

func TestRotationKeepsThePurposeAndHistoricalVerification(t *testing.T) {
	r := testRing(t)
	old := r.keys[PurposeTowerCertificateIssuer]
	oldSig, err := r.Sign(PurposeTowerCertificateIssuer, []byte("issued before rotation"))
	require.NoError(t, err)

	next, err := r.Rotate(PurposeTowerCertificateIssuer, time.Hour)
	require.NoError(t, err)
	require.Equal(t, PurposeTowerCertificateIssuer, next.Purpose, "rotation preserves the purpose")
	require.NotEqual(t, old.KeyID, next.KeyID, "the replacement has a new key ID")
	require.True(t, next.NotAfter.After(next.NotBefore), "and a bounded validity interval")

	// Historical verification metadata remains available: a certificate issued before the
	// rotation must not become unverifiable because the key moved on.
	require.NoError(t, r.Verify(PurposeTowerCertificateIssuer, []byte("issued before rotation"), oldSig),
		"history signed under the retired key must still verify")

	// New signatures come from the replacement.
	fresh, err := r.Sign(PurposeTowerCertificateIssuer, []byte("issued after rotation"))
	require.NoError(t, err)
	require.Equal(t, next.KeyID, fresh.KeyID)
}

// The retired key stops signing once its overlap ends. Overlap exists so in-flight work
// finishes, not so a superseded key keeps authority indefinitely.
func TestTheRetiredKeyStopsSigningAfterOverlap(t *testing.T) {
	r := testRing(t)
	old := r.keys[PurposeSettlementSigner]
	_, err := r.Rotate(PurposeSettlementSigner, 20*time.Millisecond)
	require.NoError(t, err)

	require.True(t, r.canSignWith(old), "during overlap the retired key may still finish work")
	time.Sleep(30 * time.Millisecond)
	require.False(t, r.canSignWith(old), "after overlap the retired private key stops signing")

	// It still VERIFIES - retiring a signer must not invalidate what it lawfully signed.
	require.True(t, r.canVerifyWith(old))
}

// Rotation must not become a way to change what a key is for.
func TestRotationCannotChangePurpose(t *testing.T) {
	r := testRing(t)
	_, err := r.Rotate(Purpose("not-a-role"), time.Hour)
	require.ErrorIs(t, err, ErrUnknownPurpose)

	next, err := r.Rotate(PurposeSessionHMAC, time.Hour)
	require.NoError(t, err)
	require.Equal(t, PurposeSessionHMAC, next.Purpose)

	// A rotated key is still rejected for every other role. (Both of these are symmetric
	// roles, so they authenticate rather than sign.)
	mac, err := r.MAC(PurposeSessionHMAC, []byte("x"))
	require.NoError(t, err)
	require.ErrorIs(t, r.VerifyMAC(PurposeAdminAuthentication, []byte("x"), mac), ErrPurposeMismatch)
}

// A ring stays valid across rotation: the replacement must not collide with another role.
func TestARotatedRingStillValidates(t *testing.T) {
	r := testRing(t)
	for _, p := range AllPurposes() {
		_, err := r.Rotate(p, time.Millisecond)
		require.NoError(t, err)
	}
	require.NoError(t, r.Validate())
}

// --- secrets stay out of sight --------------------------------------------

// Logs, status, doctor output, and panics must never carry key material. Public key IDs
// and expiry may appear; that is what makes an incident diagnosable.
func TestSecretMaterialIsAbsentFromEveryRendering(t *testing.T) {
	r := testRing(t)
	renderings := []string{r.String(), r.Describe(), r.keys[PurposeSettlementSigner].String()}
	for _, out := range renderings {
		requireNoSecrets(t, r, out)
	}
	require.Contains(t, r.Describe(), string(PurposeSettlementSigner), "a role must be nameable")
	require.Contains(t, r.Describe(), r.keys[PurposeSettlementSigner].KeyID, "public key IDs may appear")
}

func requireNoSecrets(t *testing.T, r *Ring, out string) {
	t.Helper()
	for _, k := range r.keys {
		// A key whose material was dropped renders as the empty string, and every output
		// contains that - so skip it rather than assert a tautology.
		if secret := k.secretHexForTest(); secret != "" {
			require.NotContains(t, out, secret, "key material must never be rendered")
		}
		if len(k.secret) > 0 {
			require.NotContains(t, out, string(k.secret), "a raw shared secret must never be rendered")
		}
		if k.DerivedFrom != "" {
			require.NotContains(t, out, k.DerivedFrom, "a derived-key root is secret material")
		}
	}
}

// signingPurposes is every role that signs.
func signingPurposes() []Purpose {
	var out []Purpose
	for _, p := range AllPurposes() {
		if KindOf(p) == KindSigning {
			out = append(out, p)
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------

func testRing(t *testing.T) *Ring {
	t.Helper()
	r, err := NewGeneratedRing()
	require.NoError(t, err)
	return r
}

// --- each layer of the purpose check, isolated -----------------------------
//
// Verify defends the same property three ways: the envelope's claimed purpose, the key's
// own purpose, and the purpose bound into the signed bytes. That redundancy is deliberate,
// but it means deleting any ONE guard leaves the other two and the suite stays green. The
// tests below isolate each layer so none of them is load-bearing on the others.

// A forged envelope: an attacker relabels a wrong-purpose signature with the purpose the
// verifier wants. The envelope's claim now matches, so only the key's own purpose stands
// between a settlement signer and an execution grant.
func TestARelabelledEnvelopeIsRejectedByTheKeysOwnPurpose(t *testing.T) {
	r := testRing(t)
	msg := []byte("issue a grant")

	sig, err := r.Sign(PurposeSettlementSigner, msg)
	require.NoError(t, err)

	forged := sig
	forged.Purpose = PurposeExecutionGrantSigner // the attacker's claim, not the key's

	err = r.Verify(PurposeExecutionGrantSigner, msg, forged)
	require.ErrorIs(t, err, ErrPurposeMismatch,
		"the key's own purpose is authoritative, never the envelope's claim about it")
	require.Contains(t, err.Error(), string(PurposeSettlementSigner),
		"the error must name what the key actually holds")
}

// The mirror: an honest envelope from the wrong role.
//
// Honest caveat: this one cannot be isolated. The envelope check is strictly subsumed by
// the key-purpose check, so deleting it leaves the suite green - it is kept as a fail-fast
// layer on the money path, not because a test proves it load-bearing. Recorded here so a
// later reader does not mistake this for a verified guard.
func TestAnHonestEnvelopeFromTheWrongRoleIsRejectedBeforeAnyCryptography(t *testing.T) {
	r := testRing(t)
	msg := []byte("settle 100")

	sig, err := r.Sign(PurposeSettlementSigner, msg)
	require.NoError(t, err)

	// Required: execution grant. Presented: an entirely valid settlement signature.
	err = r.Verify(PurposeExecutionGrantSigner, msg, sig)
	require.ErrorIs(t, err, ErrPurposeMismatch)
	require.NotErrorIs(t, err, ErrBadSignature,
		"this must be refused as wrong authority, not as bad cryptography - the key is real")
}

// The third layer: the purpose is bound into the bytes, so signature material cannot be
// lifted from one purpose's envelope into another's. Without this the purpose would be a
// label rather than a binding, and both checks above would be the only gate.
func TestThePurposeIsBoundIntoTheSignedBytes(t *testing.T) {
	msg := []byte("the same message under two roles")

	seen := map[string]Purpose{}
	for _, p := range AllPurposes() {
		b := string(signingBytes(p, msg))
		if other, clash := seen[b]; clash {
			t.Fatalf("%s and %s sign identical bytes; a signature could be moved between them", p, other)
		}
		seen[b] = p
	}

	// And the binding is a prefix that cannot be confused with message content: a purpose
	// whose name is a prefix of another's must not produce a reusable signing block.
	require.NotEqual(t,
		string(signingBytes(Purpose("ab"), []byte("c"))),
		string(signingBytes(Purpose("a"), []byte("bc"))),
		"the purpose and the message must not be able to trade characters")
}

// Belt and braces: with the three layers considered together, signature material made
// under one purpose must never verify under another, whichever guard is doing the work.
func TestSignatureMaterialCannotBeMovedBetweenPurposes(t *testing.T) {
	r := testRing(t)
	msg := []byte("payload")

	sig, err := r.Sign(PurposeSettlementSigner, msg)
	require.NoError(t, err)

	// Every way an attacker can dress it up as an execution grant.
	target := PurposeExecutionGrantSigner
	targetKeyID := r.keys[target].KeyID

	for name, forged := range map[string]Signature{
		"relabelled purpose":      {Purpose: target, KeyID: sig.KeyID, Sig: sig.Sig},
		"swapped key ID":          {Purpose: target, KeyID: targetKeyID, Sig: sig.Sig},
		"purpose and key swapped": {Purpose: sig.Purpose, KeyID: targetKeyID, Sig: sig.Sig},
		"untouched":               sig,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, r.Verify(target, msg, forged),
				"a settlement signature must never become an execution grant")
		})
	}
}

// --- remaining paths -------------------------------------------------------

// A signature naming a key the ring has never seen must not verify. This is the path a
// retired-and-discarded or foreign key takes, and it must fail as unavailable rather than
// falling through to whatever key currently holds the purpose.
func TestASignatureFromAnUnknownKeyIsRefused(t *testing.T) {
	r := testRing(t)
	sig, err := r.Sign(PurposeSettlementSigner, []byte("x"))
	require.NoError(t, err)

	sig.KeyID = "ffffffffffffffff"
	err = r.Verify(PurposeSettlementSigner, []byte("x"), sig)
	require.Error(t, err, "an unrecognised key must fail closed, not fall through to the current one")

	// The failure must not reveal whether the guessed key ID exists. A wrong-purpose key
	// and a nonexistent one must be indistinguishable, or the error is a key-enumeration
	// oracle.
	other, err2 := r.Sign(PurposeExecutionGrantSigner, []byte("x"))
	require.NoError(t, err2)
	known := other
	known.Purpose = PurposeSettlementSigner
	knownErr := r.Verify(PurposeSettlementSigner, []byte("x"), known)
	require.Error(t, knownErr)
	require.Equal(t, errKind(err), errKind(knownErr),
		"a guessed key ID must not be distinguishable from a real one by error")
	require.NotContains(t, err.Error(), "ffffffffffffffff",
		"the probed key ID must not be echoed back")
}

// errKind reduces an error to the sentinel it wraps, which is what a caller - or an
// attacker - can actually observe.
func errKind(err error) string {
	switch {
	case errors.Is(err, ErrPurposeMismatch):
		return "purpose-mismatch"
	case errors.Is(err, ErrKeyUnavailable):
		return "key-unavailable"
	case errors.Is(err, ErrBadSignature):
		return "bad-signature"
	case errors.Is(err, ErrUnknownPurpose):
		return "unknown-purpose"
	}
	return "other"
}

// Malformed signature bytes are a bad signature, not a panic and not a pass.
func TestUndecodableSignatureBytesAreRejected(t *testing.T) {
	r := testRing(t)
	sig, err := r.Sign(PurposeSettlementSigner, []byte("x"))
	require.NoError(t, err)

	for name, bad := range map[string]string{
		"not hex at all": "not hex at all",
		"odd length":     sig.Sig[:len(sig.Sig)-1],
		// The one that matters. hex.DecodeString returns the successfully decoded PREFIX
		// alongside its error, so a valid signature with garbage appended decodes to the
		// intact valid signature. Ignoring the error here is a signature-forgery bypass,
		// not a tidiness issue - an attacker appends two characters and verification
		// passes. Verified against Go's actual behaviour, not assumed.
		"valid signature with trailing garbage": sig.Sig + "zz",
	} {
		t.Run(name, func(t *testing.T) {
			forged := sig
			forged.Sig = bad
			require.ErrorIs(t, r.Verify(PurposeSettlementSigner, []byte("x"), forged), ErrBadSignature)
		})
	}
}

// Describe must say plainly when a role has no key, rather than omitting the line - a
// missing row reads as "fine" at exactly the moment it is not.
func TestDescribeNamesAMissingRole(t *testing.T) {
	r := testRing(t)
	r.remove(PurposeSettlementSigner)
	require.Contains(t, r.Describe(), "no key configured")
	require.Contains(t, r.String(), "56 of 57")
}

// A key retired without any overlap stops signing at once.
func TestRotationWithoutOverlapRetiresImmediately(t *testing.T) {
	r := testRing(t)
	old := r.keys[PurposeAttemptStateSigner]
	_, err := r.Rotate(PurposeAttemptStateSigner, 0)
	require.NoError(t, err)

	require.False(t, r.canSignWith(old), "no overlap means no grace")
	require.True(t, r.canVerifyWith(old), "but its history still verifies")
}

// Rotation carries the managed-key configuration forward. A replacement that silently
// dropped its alias could collide with another role and validate anyway.
func TestRotationCarriesManagedKeyConfigurationForward(t *testing.T) {
	r := testRing(t)
	r.keys[PurposePayoutRailAPI].Alias = "rail/alias"
	r.keys[PurposePayoutRailAPI].DerivedFrom = "rail-root"
	r.keys[PurposePayoutRailAPI].Fallback = "rail-fallback"

	next, err := r.Rotate(PurposePayoutRailAPI, time.Hour)
	require.NoError(t, err)
	require.Equal(t, "rail/alias", next.Alias)
	require.Equal(t, "rail-root", next.DerivedFrom)
	require.Equal(t, "rail-fallback", next.Fallback)

	// And the carried-forward configuration is still seen by the distinctness check.
	r.keys[PurposePaymentWebhookAuthentication].Alias = "rail/alias"
	require.Error(t, r.Validate())
}

// A ring is usable concurrently: signing, verification and rotation all take the lock.
func TestTheRingIsSafeUnderConcurrentUse(t *testing.T) {
	r := testRing(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sig, err := r.Sign(PurposeSettlementSigner, []byte("x"))
			if err == nil {
				_ = r.Verify(PurposeSettlementSigner, []byte("x"), sig)
			}
			_, _ = r.Rotate(PurposeSettlementSigner, time.Minute)
			_ = r.Describe()
			_ = r.Validate()
		}()
	}
	wg.Wait()
}

// --- the validity window is enforced on the production path ----------------

// The spec says the old private key stops signing after overlap. That has to be true of
// Sign, not merely of a predicate the tests call - which is what it was until a review
// pointed out that canSignWith had no production caller at all.
func TestSignRefusesAKeyOutsideItsWindow(t *testing.T) {
	t.Run("expired current key", func(t *testing.T) {
		r := testRing(t)
		r.keys[PurposeSettlementSigner].NotAfter = time.Now().Add(-time.Minute)

		_, err := r.Sign(PurposeSettlementSigner, []byte("settle"))
		require.ErrorIs(t, err, ErrKeyUnavailable,
			"a bounded validity interval that never stops a signature is decorative")
	})

	t.Run("not yet valid current key", func(t *testing.T) {
		r := testRing(t)
		r.keys[PurposeSettlementSigner].NotBefore = time.Now().Add(time.Minute)

		_, err := r.Sign(PurposeSettlementSigner, []byte("settle"))
		require.ErrorIs(t, err, ErrKeyUnavailable)
	})

	t.Run("a key inside its window signs", func(t *testing.T) {
		r := testRing(t)
		_, err := r.Sign(PurposeSettlementSigner, []byte("settle"))
		require.NoError(t, err)
	})
}

// A current key is signable; the retired-key branch is not the only one that matters.
func TestACurrentKeyMaySign(t *testing.T) {
	r := testRing(t)
	for _, p := range AllPurposes() {
		require.True(t, r.canSignWith(r.keys[p]), "%s is current and inside its window", p)
		_ = KindOf(p)
	}
}

// Retiring a signer must not invalidate what it lawfully signed, even though it can no
// longer sign. Verification and signing authority are different questions.
func TestHistoryFromARetiredKeyStillVerifiesAfterOverlap(t *testing.T) {
	r := testRing(t)
	before, err := r.Sign(PurposeTowerCertificateIssuer, []byte("issued while current"))
	require.NoError(t, err)

	_, err = r.Rotate(PurposeTowerCertificateIssuer, 10*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, r.Verify(PurposeTowerCertificateIssuer, []byte("issued while current"), before),
		"a lawfully issued certificate must not become unverifiable because the key retired")
}

// --- the offline root is not held at runtime -------------------------------

// A correctly operated Core keeps its offline root in a vault, so a ring that demanded it
// would fail exactly the deployments doing it right.
func TestValidatePassesWithoutTheOfflineRoot(t *testing.T) {
	r := testRing(t)
	r.remove(PurposeOfflineRoot)
	require.NoError(t, r.Validate(),
		"the offline root is absent from ordinary runtime by design")
	require.False(t, HeldAtRuntime(PurposeOfflineRoot))

	// Every other role IS required.
	r.remove(PurposeSettlementSigner)
	require.Error(t, r.Validate())
	require.True(t, HeldAtRuntime(PurposeSettlementSigner))
}

// --- distinctness sees the real key, not its label -------------------------

// KeyID is an exported display field a config loader can set independently of the key it
// names. If distinctness compared the label, two roles could load the SAME private key
// under different labels and validate - the exact one-root-many-hats case being defended.
func TestTwoRolesSharingOnePrivateKeyAreRejectedEvenWithDifferentLabels(t *testing.T) {
	r := testRing(t)
	victim := r.keys[PurposeSettlementSigner]

	dup := *victim
	dup.Purpose = PurposeExecutionGrantSigner
	dup.KeyID = "a-different-looking-label"
	r.keys[PurposeExecutionGrantSigner] = &dup

	err := r.Validate()
	require.Error(t, err, "a relabelled duplicate of a real key must not pass distinctness")
	require.Contains(t, err.Error(), "public key")
}

// The error names the public key IDs as well as the purposes: an operator fixing a
// collision needs to know which key to replace.
func TestTheDistinctnessErrorNamesThePublicKeyIDs(t *testing.T) {
	r := testRing(t)
	r.keys[PurposeSettlementSigner].Alias = "shared/alias"
	r.keys[PurposePayoutRailAPI].Alias = "shared/alias"

	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), r.keys[PurposeSettlementSigner].KeyID)
	require.Contains(t, err.Error(), r.keys[PurposePayoutRailAPI].KeyID)
	requireNoSecrets(t, r, err.Error())
	require.NotContains(t, err.Error(), "shared/alias",
		"the shared value itself is not named; the roles and their key IDs are")
}

// --- rotation hands back a copy --------------------------------------------

// The live record is read under the ring's lock, so returning a pointer to it invites an
// unsynchronised write from the first production caller.
func TestRotateReturnsACopyNotTheLiveRecord(t *testing.T) {
	r := testRing(t)
	next, err := r.Rotate(PurposeSettlementSigner, time.Hour)
	require.NoError(t, err)

	next.KeyID = "tampered"
	next.Alias = "tampered"
	require.NotEqual(t, "tampered", r.keys[PurposeSettlementSigner].KeyID,
		"a caller must not be able to reach into the ring")
	require.NotEqual(t, "tampered", r.keys[PurposeSettlementSigner].Alias)
}

// A retired key colliding with a current one makes key lookup nondeterministic, since
// verification resolves against both.
func TestARetiredKeyCollidingWithACurrentOneIsReported(t *testing.T) {
	r := testRing(t)
	stale := *r.keys[PurposeSettlementSigner]
	r.retired[r.keys[PurposeSettlementSigner].KeyID] = &stale

	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "retired key")
}

// --- the closed set cannot be edited by a caller ---------------------------

func TestAllPurposesCannotBeMutatedByACaller(t *testing.T) {
	got := AllPurposes()
	require.NotEmpty(t, got)
	got[0] = Purpose("tampered")

	require.NotEqual(t, Purpose("tampered"), AllPurposes()[0], "the closed set must be a copy")
	require.True(t, Known(AllPurposes()[0]))
}

// A key whose public half is malformed must be refused, not panic. ed25519.Verify panics
// on a wrong-size public key, and a panic report is exactly the surface that must never
// carry key material.
func TestAMalformedPublicKeyIsRefusedRatherThanPanicking(t *testing.T) {
	r := testRing(t)
	sig, err := r.Sign(PurposeSettlementSigner, []byte("x"))
	require.NoError(t, err)

	r.keys[PurposeSettlementSigner].pub = []byte{1, 2, 3}
	require.NotPanics(t, func() {
		require.ErrorIs(t, r.Verify(PurposeSettlementSigner, []byte("x"), sig), ErrBadSignature)
	})
}
