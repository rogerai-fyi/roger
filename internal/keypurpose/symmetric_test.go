package keypurpose

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// "Symmetric secrets cannot authenticate another role" - key_separation.feature.
//
// Some roles are not signers at all. A session cookie, a pseudonym, an admin token, an
// evidence key, a webhook secret and two API credentials are shared secrets, and the
// spec's claim about them is stronger than "they cannot sign": possessing the raw bytes
// of one must not derive authority for another.
//
// The Cartesian test in keyring_test.go covers "a key cannot act for another role". This
// file covers the part it cannot: that the two KINDS do not interchange, and that no
// symmetric secret is derivable from another.

// specSymmetricSecrets reads the secret column of the spec's symmetric table, so the set
// of symmetric roles is taken from the approved spec rather than from my judgement.
func specSymmetricSecrets(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(specFile)
	require.NoError(t, err)
	defer f.Close()

	var rows []string
	state := "before"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "Scenario Outline: Symmetric secrets cannot authenticate another role"):
			state = "seeking-header"
		case state == "seeking-header" && strings.HasPrefix(line, "| secret"):
			state = "in-table"
		case state == "in-table" && strings.HasPrefix(line, "|"):
			rows = append(rows, strings.TrimSpace(strings.Split(strings.Trim(line, "|"), "|")[0]))
		case state == "in-table" && strings.HasPrefix(line, "Scenario"):
			state = "done"
		}
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, rows)
	return rows
}

// symmetricRowAliases maps the symmetric table's wording onto the role table's wording.
// Written out rather than inferred, for the same reason as the failure-table aliases.
var symmetricRowAliases = map[string]Purpose{
	"web-session HMAC":                  PurposeSessionHMAC,
	"pseudonym HMAC":                    PurposePseudonymHMAC,
	"administrator token":               PurposeAdminAuthentication,
	"evidence-encryption key":           PurposeEvidenceEncryption,
	"payment-webhook secret":            PurposePaymentWebhookAuthentication,
	"payment-reconciliation credential": PurposePaymentReconciliationAPI,
	"payout-rail credential":            PurposePayoutRailAPI,
}

// Every secret the spec names is a symmetric role in the ring, and vice versa.
func TestTheSymmetricRolesAreExactlyTheOnesTheSpecNames(t *testing.T) {
	named := map[Purpose]bool{}
	for _, row := range specSymmetricSecrets(t) {
		p, ok := symmetricRowAliases[row]
		require.True(t, ok, "the spec names secret %q with no mapping to a purpose", row)
		require.Equal(t, KindSymmetric, KindOf(p), "%s must be a symmetric role", p)
		named[p] = true
	}
	for _, p := range AllPurposes() {
		if KindOf(p) == KindSymmetric {
			require.True(t, named[p], "%s is symmetric but the spec never names it as a secret", p)
		}
	}
}

// The two kinds do not interchange. A symmetric secret cannot produce a signature, and a
// signing key cannot produce a MAC - otherwise "one purpose per key" would hold for the
// name while the bytes did double duty.
func TestTheTwoKindsDoNotInterchange(t *testing.T) {
	r := testRing(t)

	for _, p := range AllPurposes() {
		switch KindOf(p) {
		case KindSymmetric:
			_, err := r.Sign(p, []byte("x"))
			require.ErrorIs(t, err, ErrWrongKeyKind, "%s is a shared secret, not a signer", p)
		case KindSigning:
			_, err := r.MAC(p, []byte("x"))
			require.ErrorIs(t, err, ErrWrongKeyKind, "%s is a signer, not a shared secret", p)
		default:
			t.Fatalf("%s has no kind", p)
		}
	}
}

// A MAC is bound to its purpose exactly as a signature is.
func TestAMACFromOneSecretDoesNotAuthenticateAnother(t *testing.T) {
	r := testRing(t)
	msg := []byte("authenticate me")

	var symmetric []Purpose
	for _, p := range AllPurposes() {
		if KindOf(p) == KindSymmetric {
			symmetric = append(symmetric, p)
		}
	}
	require.NotEmpty(t, symmetric)

	for _, required := range symmetric {
		for _, presented := range symmetric {
			mac, err := r.MAC(presented, msg)
			require.NoError(t, err)

			err = r.VerifyMAC(required, msg, mac)
			if required == presented {
				require.NoError(t, err)
				continue
			}
			require.ErrorIs(t, err, ErrPurposeMismatch,
				"a valid %s secret must not authenticate %s", presented, required)
		}
	}
}

// The claim the Cartesian test cannot make: no cross-role key is derived from the
// possessed bytes. Each symmetric secret is independent material, so holding one gives an
// attacker no algebraic route to another.
func TestNoSymmetricSecretIsDerivableFromAnother(t *testing.T) {
	r := testRing(t)

	seen := map[string]Purpose{}
	for _, p := range AllPurposes() {
		if KindOf(p) != KindSymmetric {
			continue
		}
		raw := r.secretForTest(p)
		require.GreaterOrEqual(t, len(raw), 32, "%s needs a full-strength secret", p)

		if other, clash := seen[string(raw)]; clash {
			t.Fatalf("%s and %s hold identical secret bytes", p, other)
		}
		seen[string(raw)] = p

		// And no secret is a substring or transform of another - the cheapest sign that
		// one root was stretched across several roles.
		for prev := range seen {
			if prev == string(raw) {
				continue
			}
			require.False(t, strings.Contains(prev, string(raw)))
			require.False(t, strings.Contains(string(raw), prev))
		}
	}
	require.Len(t, seen, 7, "the spec names seven symmetric secrets")
}

// Possessing one role's raw bytes must not let an attacker forge another role's MAC, even
// with the algorithm in hand. This is the concrete form of the spec's claim.
func TestPossessingOneSecretForgesNothingElse(t *testing.T) {
	r := testRing(t)
	msg := []byte("promote me to administrator")

	stolen := r.secretForTest(PurposeSessionHMAC)

	// The attacker builds a ring of their own from the stolen bytes and MACs with it.
	attacker := ringFromSecretForTest(PurposeAdminAuthentication, stolen)
	forged, err := attacker.MAC(PurposeAdminAuthentication, msg)
	require.NoError(t, err)

	require.Error(t, r.VerifyMAC(PurposeAdminAuthentication, msg, forged),
		"a stolen session secret must not authenticate as administrator")
}

// A tampered message or tag is rejected, so the purpose label is not the only gate.
func TestATamperedMACIsRejected(t *testing.T) {
	r := testRing(t)
	mac, err := r.MAC(PurposeSessionHMAC, []byte("user=alice"))
	require.NoError(t, err)

	require.Error(t, r.VerifyMAC(PurposeSessionHMAC, []byte("user=root"), mac))

	forged := mac
	forged.Sig = strings.Repeat("00", len(forged.Sig)/2)
	require.Error(t, r.VerifyMAC(PurposeSessionHMAC, []byte("user=alice"), forged))

	forged = mac
	forged.Sig = "not hex"
	require.Error(t, r.VerifyMAC(PurposeSessionHMAC, []byte("user=alice"), forged))
}

// Symmetric roles are covered by the distinctness check too. They have no public key, and
// an earlier version of that check skipped empty values - which would have let every
// symmetric role share one secret and validate.
func TestTwoSymmetricRolesSharingOneSecretAreRejected(t *testing.T) {
	r := testRing(t)
	shareSecretForTest(r, PurposeSessionHMAC, PurposePseudonymHMAC)

	err := r.Validate()
	require.Error(t, err, "two roles holding one secret is one compromise wearing two hats")
	requireNoSecrets(t, r, err.Error())
}

// Raw key material must never authenticate as an admin credential.
func TestRawKeyMaterialIsNotAnAdminCredential(t *testing.T) {
	r := testRing(t)

	// Every encoding an attacker might paste into an admin header.
	for name, presented := range map[string]string{
		"a signing key's public ID": r.keyIDForTest(PurposeSettlementSigner),
		"a symmetric secret":        string(r.secretForTest(PurposeSessionHMAC)),
	} {
		t.Run(name, func(t *testing.T) {
			mac := Signature{Purpose: PurposeAdminAuthentication, KeyID: "x", Sig: presented}
			require.Error(t, r.VerifyMAC(PurposeAdminAuthentication, []byte("admin"), mac),
				"key material is not a bearer token")
		})
	}
}

// --- remaining symmetric and failure paths ---------------------------------

// An unknown purpose is refused by the symmetric path too, not treated as permissive.
func TestMACRefusesAnUnknownPurpose(t *testing.T) {
	r := testRing(t)
	_, err := r.MAC(Purpose("root"), []byte("x"))
	require.ErrorIs(t, err, ErrUnknownPurpose)

	mac := Signature{Purpose: Purpose("root"), Sig: "00"}
	require.ErrorIs(t, r.VerifyMAC(Purpose("root"), []byte("x"), mac), ErrUnknownPurpose)
}

// A symmetric role with no material fails closed rather than authenticating on nothing.
func TestAMissingSecretFailsClosedOnBothSides(t *testing.T) {
	r := testRing(t)
	mac, err := r.MAC(PurposeSessionHMAC, []byte("x"))
	require.NoError(t, err)

	r.remove(PurposeSessionHMAC)
	_, err = r.MAC(PurposeSessionHMAC, []byte("x"))
	require.ErrorIs(t, err, ErrKeyUnavailable)
	require.ErrorIs(t, r.VerifyMAC(PurposeSessionHMAC, []byte("x"), mac), ErrKeyUnavailable)
}

// A shared secret outside its window stops authenticating, exactly as a signer does.
func TestASecretOutsideItsWindowStopsAuthenticating(t *testing.T) {
	r := testRing(t)
	r.keys[PurposeSessionHMAC].NotAfter = time.Now().Add(-time.Minute)

	_, err := r.MAC(PurposeSessionHMAC, []byte("x"))
	require.ErrorIs(t, err, ErrKeyUnavailable)
}

// A failed symmetric role fails both MAC and VerifyMAC, and drops its material rather
// than leaving it usable beside a flag.
func TestAFailedSecretDropsItsMaterial(t *testing.T) {
	r := testRing(t)
	mac, err := r.MAC(PurposePseudonymHMAC, []byte("x"))
	require.NoError(t, err)

	require.NoError(t, r.MarkUnloadable(PurposePseudonymHMAC, LoadDuplicated))
	require.Empty(t, r.secretForTest(PurposePseudonymHMAC),
		"a duplicated secret must not remain usable by any path that forgets the flag")

	_, err = r.MAC(PurposePseudonymHMAC, []byte("x"))
	require.ErrorIs(t, err, ErrKeyUnavailable)
	require.ErrorIs(t, r.VerifyMAC(PurposePseudonymHMAC, []byte("x"), mac), ErrKeyUnavailable)
}

// Marking a role that has no key at all still records the failure, so a role missing from
// configuration reports why rather than reporting nothing.
func TestMarkingAnAbsentRoleStillRecordsTheFailure(t *testing.T) {
	r := testRing(t)
	r.remove(PurposeSettlementSigner)
	require.NoError(t, r.MarkUnloadable(PurposeSettlementSigner, LoadUnavailable))

	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), string(LoadUnavailable))
}

// A key holding neither kind of material has no commitment, and must not collide with
// every other empty one in the distinctness check.
func TestAnEmptyKeyHasNoMaterialCommitment(t *testing.T) {
	require.Empty(t, (&Key{}).materialCommitment())

	r := testRing(t)
	require.NoError(t, r.MarkUnloadable(PurposeSettlementSigner, LoadMissing))
	require.NoError(t, r.MarkUnloadable(PurposeDispatchLeaseSigner, LoadMissing))

	err := r.Validate()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "share one public key",
		"two roles with no material are not two roles sharing one key")
}

// Signing and symmetric roles must not collide with each other in the distinctness check
// either - the commitments are namespaced so a digest can never equal a public key.
func TestSigningAndSymmetricCommitmentsCannotCollide(t *testing.T) {
	r := testRing(t)
	for _, p := range AllPurposes() {
		c := r.keys[p].materialCommitment()
		require.NotEmpty(t, c)
		if KindOf(p) == KindSymmetric {
			require.True(t, strings.HasPrefix(c, "sec:"))
		} else {
			require.True(t, strings.HasPrefix(c, "pub:"))
		}
	}
}

// The purpose binding inside the MAC is the last line if two roles ever hold the same
// secret. Validate refuses that configuration at startup, but a running ring can be put
// into it, and at that point independent-secrets reasoning no longer protects anything -
// only the binding does. Without it, a session cookie would authenticate as a pseudonym.
func TestBindingStillSeparatesTwoRolesThatShareASecret(t *testing.T) {
	r := testRing(t)
	shareSecretForTest(r, PurposeSessionHMAC, PurposePseudonymHMAC)
	msg := []byte("subject=alice")

	mac, err := r.MAC(PurposeSessionHMAC, msg)
	require.NoError(t, err)

	// Relabelled by an attacker, so the envelope check cannot be what refuses it.
	forged := mac
	forged.Purpose = PurposePseudonymHMAC

	require.ErrorIs(t, r.VerifyMAC(PurposePseudonymHMAC, msg, forged), ErrBadSignature,
		"the purpose is bound into the tag, so identical secrets still do not interchange")
}
