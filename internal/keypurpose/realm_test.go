package keypurpose

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A key belongs to a NETWORK, not only to a role - key_separation.feature.
//
// Four separate scenarios in the approved spec turn out to be one property:
//
//	a standalone trust root has no public-network validity;
//	a public RogerAI key has no implicit local admin power;
//	a joined Tower key cannot exercise central or leaf authority;
//	a Station key cannot exercise Tower or central authority.
//
// Each is "material issued under one trust root must not carry authority under another".
// Implementing them as one realm check rather than four bespoke rejections is what makes
// the guarantee total: a role added later inherits the separation instead of needing
// somebody to remember a fifth rejection list.

// standaloneSpecRoles parses the prose list in the standalone purpose-separation scenario.
// Parsed rather than restated for the same reason as the Core role table: a hand-copied
// list of twenty roles drifts, and a dropped role is a dropped control.
func standaloneSpecRoles(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(specFile)
	require.NoError(t, err)
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "Then pinned offline root,") {
			continue
		}
		list := strings.TrimPrefix(line, "Then ")
		list = strings.TrimSuffix(list, " resolve to distinct key identities")
		var roles []string
		for _, part := range strings.Split(list, ",") {
			part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "and "))
			part = strings.TrimSuffix(part, " roles")
			if part != "" {
				roles = append(roles, part)
			}
		}
		require.Len(t, roles, 20, "the spec names twenty standalone roles")
		return roles
	}
	t.Fatal("the standalone role list is not in the spec file")
	return nil
}

func TestEveryStandaloneRoleTheSpecNamesExists(t *testing.T) {
	for _, role := range standaloneSpecRoles(t) {
		p, ok := LookupIn(RealmStandalone, role)
		require.True(t, ok, "the spec names standalone role %q; the keyring does not know it", role)
		require.Equal(t, RealmStandalone, RealmOf(p))
	}
}

func TestEveryPurposeBelongsToExactlyOneRealm(t *testing.T) {
	for _, realm := range AllRealms() {
		for _, p := range PurposesIn(realm) {
			require.Equal(t, realm, RealmOf(p), "%s must belong to %s alone", p, realm)
		}
	}

	// And no purpose name is reused across realms, which would make RealmOf ambiguous and
	// let a lookup in one network silently resolve another network's role.
	seen := map[Purpose]Realm{}
	for _, realm := range AllRealms() {
		for _, p := range PurposesIn(realm) {
			if prior, clash := seen[p]; clash {
				t.Fatalf("purpose %q exists in both %s and %s", p, prior, realm)
			}
			seen[p] = realm
		}
	}
}

// --- the cross-realm invariant --------------------------------------------

// Material issued under one trust root carries no authority under another. Asserted over
// every ordered pair of realms and every role in them, so the guarantee does not depend on
// anybody maintaining a rejection list.
func TestNoRealmsMaterialCarriesAuthorityInAnother(t *testing.T) {
	rings := map[Realm]*Ring{}
	for _, realm := range AllRealms() {
		r, err := NewGeneratedRingFor(realm)
		require.NoError(t, err)
		rings[realm] = r
	}
	msg := []byte("an object presented to the wrong network")

	for _, from := range AllRealms() {
		for _, to := range AllRealms() {
			if from == to {
				continue
			}
			for _, p := range PurposesIn(from) {
				sig, err := rings[from].use(p, msg)
				require.NoError(t, err)

				// Presented to another realm's ring, for its own matching role.
				for _, target := range PurposesIn(to) {
					if KindOf(target) != KindOf(p) {
						continue
					}
					err = rings[to].check(target, msg, sig)
					require.ErrorIs(t, err, ErrWrongRealm,
						"%s material must not carry %s authority", from, to)
					break
				}
			}
		}
	}
}

// A ring refuses to operate a role from another network at all, rather than merely failing
// to verify it. Asking a standalone Tower to sign with a Roger Core purpose is a
// configuration error, not a signature that happens not to check out.
func TestARingRefusesAForeignRealmsRoleOutright(t *testing.T) {
	standalone, err := NewGeneratedRingFor(RealmStandalone)
	require.NoError(t, err)

	_, err = standalone.Sign(PurposeSettlementSigner, []byte("settle"))
	require.ErrorIs(t, err, ErrWrongRealm, "a standalone Tower holds no settlement authority")

	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	_, err = core.Sign(PurposeStandalonePinnedOfflineRoot, []byte("x"))
	require.ErrorIs(t, err, ErrWrongRealm, "Roger Core holds no standalone local root")
}

// The four spec scenarios, named. Each is covered by the exhaustive test above; they are
// restated because these are the ones with a stated reason.
func TestTheNamedCrossNetworkRejections(t *testing.T) {
	for name, tc := range map[string]struct{ holder, target Realm }{
		"a standalone trust root has no public-network validity": {RealmStandalone, RealmCore},
		"a public RogerAI key has no implicit local admin power": {RealmCore, RealmStandalone},
		"a joined Tower key cannot exercise central authority":   {RealmTower, RealmCore},
		"a Station key cannot exercise Tower authority":          {RealmStation, RealmTower},
		"a Station key cannot exercise central authority":        {RealmStation, RealmCore},
	} {
		t.Run(name, func(t *testing.T) {
			holder, err := NewGeneratedRingFor(tc.holder)
			require.NoError(t, err)
			target, err := NewGeneratedRingFor(tc.target)
			require.NoError(t, err)

			msg := []byte("authorize me")
			for _, p := range PurposesIn(tc.holder) {
				if KindOf(p) != KindSigning {
					continue
				}
				sig, sErr := holder.Sign(p, msg)
				require.NoError(t, sErr)

				for _, t2 := range PurposesIn(tc.target) {
					if KindOf(t2) != KindSigning {
						continue
					}
					require.ErrorIs(t, target.Verify(t2, msg, sig), ErrWrongRealm)
				}
			}
		})
	}
}

// --- within-realm separation ----------------------------------------------

// Each realm's own roles are distinct from each other, exactly as Core's are. A realm that
// validated with two roles sharing a key would have separation in name only.
func TestEveryRealmValidatesAndSeparatesItsOwnRoles(t *testing.T) {
	for _, realm := range AllRealms() {
		r, err := NewGeneratedRingFor(realm)
		require.NoError(t, err)
		require.NoError(t, r.Validate(), "%s must validate when fully populated", realm)

		roles := PurposesIn(realm)
		require.NotEmpty(t, roles)

		// Every ordered pair within the realm is rejected too.
		msg := []byte("x")
		for _, required := range roles {
			for _, presented := range roles {
				if required == presented || KindOf(required) != KindOf(presented) {
					continue
				}
				sig, err := r.use(presented, msg)
				require.NoError(t, err)
				require.ErrorIs(t, r.check(required, msg, sig), ErrPurposeMismatch,
					"%s must not exercise %s inside %s", presented, required, realm)
			}
		}
	}
}

// A Tower's persistent identity-statement key and its rotating TLS key are distinct, so
// rotating TLS never touches identity and stealing a TLS key proves nothing about who the
// Tower is.
func TestATowerSeparatesItsIdentityFromItsTLSKey(t *testing.T) {
	r, err := NewGeneratedRingFor(RealmTower)
	require.NoError(t, err)

	identity := r.keyIDForTest(PurposeTowerStatementKey)
	tls := r.keyIDForTest(PurposeTowerTLS)
	require.NotEmpty(t, identity)
	require.NotEqual(t, identity, tls)

	// Rotating TLS leaves identity untouched: that is the point of separating them.
	_, err = r.Rotate(PurposeTowerTLS, 0)
	require.NoError(t, err)
	require.Equal(t, identity, r.keyIDForTest(PurposeTowerStatementKey))
	require.NotEqual(t, tls, r.keyIDForTest(PurposeTowerTLS))
}

// A Station's provider-assertion signing key and its secure-session TLS key are distinct,
// and possession of either cannot exercise the other purpose.
func TestAStationSeparatesAssertionSigningFromItsTLSIdentity(t *testing.T) {
	r, err := NewGeneratedRingFor(RealmStation)
	require.NoError(t, err)
	msg := []byte("an offer")

	assertion, err := r.Sign(PurposeStationAssertionSigner, msg)
	require.NoError(t, err)
	require.ErrorIs(t, r.Verify(PurposeStationTLS, msg, assertion), ErrPurposeMismatch)

	tls, err := r.Sign(PurposeStationTLS, msg)
	require.NoError(t, err)
	require.ErrorIs(t, r.Verify(PurposeStationAssertionSigner, msg, tls), ErrPurposeMismatch)
}

// A Tower's local bridge authorities have no public-network authority. They are distinct
// from each other, from Tower identity and TLS, and from every Roger Core role.
func TestTowerBridgeAuthoritiesHaveNoPublicNetworkAuthority(t *testing.T) {
	tower, err := NewGeneratedRingFor(RealmTower)
	require.NoError(t, err)
	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	msg := []byte("issue this")

	bridgeAuthority := r_keyID(t, tower, PurposeTowerBridgeAuthority)
	bridgeCert := r_keyID(t, tower, PurposeTowerBridgeCertificate)
	require.NotEqual(t, bridgeAuthority, bridgeCert)
	require.NotEqual(t, bridgeAuthority, r_keyID(t, tower, PurposeTowerStatementKey))
	require.NotEqual(t, bridgeCert, r_keyID(t, tower, PurposeTowerTLS))

	// Roger Core rejects locally issued objects for every central authority.
	for _, p := range []Purpose{PurposeTowerBridgeAuthority, PurposeTowerBridgeCertificate} {
		sig, sErr := tower.Sign(p, msg)
		require.NoError(t, sErr)
		for _, central := range []Purpose{
			PurposeTowerCertificateIssuer, PurposeExecutionGrantSigner,
			PurposeSettlementSigner, PurposeAdmissionLeaseSigner,
		} {
			require.ErrorIs(t, core.Verify(central, msg, sig), ErrWrongRealm,
				"a Tower-local bridge key must hold no central authority")
		}
	}
}

func r_keyID(t *testing.T, r *Ring, p Purpose) string {
	t.Helper()
	id := r.keyIDForTest(p)
	require.NotEmpty(t, id, "%s must be configured", p)
	return id
}

// A ring built for one realm must not carry another realm's keys at all - not merely
// refuse to use them. Material that is never held cannot be stolen.
func TestARingHoldsOnlyItsOwnRealmsKeys(t *testing.T) {
	for _, realm := range AllRealms() {
		r, err := NewGeneratedRingFor(realm)
		require.NoError(t, err)
		for _, p := range AllPurposes() {
			held := r.keyIDForTest(p) != ""
			require.Equal(t, RealmOf(p) == realm, held,
				"%s ring holding %s (%s) is wrong", realm, p, RealmOf(p))
		}
	}
}

// --- realm refusals on every entry point -----------------------------------

// Every way into the ring must refuse a foreign realm's role, not only the two that are
// obvious. A single unguarded entry point is the whole separation gone.
func TestEveryEntryPointRefusesAForeignRealmsRole(t *testing.T) {
	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	standalone, err := NewGeneratedRingFor(RealmStandalone)
	require.NoError(t, err)

	// A standalone signer and a standalone secret, presented to Roger Core.
	foreignSigner := PurposeStandaloneGrant
	foreignSecret := PurposeStandaloneBackupEncryption
	msg := []byte("x")

	_, err = core.Sign(foreignSigner, msg)
	require.ErrorIs(t, err, ErrWrongRealm)

	_, err = core.MAC(foreignSecret, msg)
	require.ErrorIs(t, err, ErrWrongRealm)

	_, err = core.Rotate(foreignSigner, 0)
	require.ErrorIs(t, err, ErrWrongRealm, "Roger Core must not rotate another network's key")

	require.ErrorIs(t, core.MarkUnloadable(foreignSigner, LoadMissing), ErrWrongRealm)

	sig, err := standalone.Sign(foreignSigner, msg)
	require.NoError(t, err)
	require.ErrorIs(t, core.Verify(foreignSigner, msg, sig), ErrWrongRealm)

	mac, err := standalone.MAC(foreignSecret, msg)
	require.NoError(t, err)
	require.ErrorIs(t, core.VerifyMAC(foreignSecret, msg, mac), ErrWrongRealm)
}

// A foreign signature aimed at a role the ring DOES hold is refused as foreign, not as a
// wrong-purpose key. They are different problems and an operator needs to see which.
func TestForeignMaterialIsRefusedAsForeignNotAsWrongPurpose(t *testing.T) {
	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	tower, err := NewGeneratedRingFor(RealmTower)
	require.NoError(t, err)
	msg := []byte("issue a certificate")

	sig, err := tower.Sign(PurposeTowerStatementKey, msg)
	require.NoError(t, err)

	err = core.Verify(PurposeTowerCertificateIssuer, msg, sig)
	require.ErrorIs(t, err, ErrWrongRealm)
	require.NotErrorIs(t, err, ErrPurposeMismatch,
		"a key from another network is not merely the wrong role on this one")
}

// A realm nobody defined has no ring. Failing closed here matters because the alternative
// is an empty ring that validates and signs nothing while looking configured.
func TestARingCannotBeBuiltForAnUnknownRealm(t *testing.T) {
	_, err := NewGeneratedRingFor(Realm("some-other-network"))
	require.ErrorIs(t, err, ErrWrongRealm)
}

// Each realm's renderings name that realm and carry no secrets.
func TestEachRealmDescribesItselfWithoutSecrets(t *testing.T) {
	for _, realm := range AllRealms() {
		r, err := NewGeneratedRingFor(realm)
		require.NoError(t, err)

		require.Contains(t, r.String(), string(realm))
		require.Equal(t, realm, r.Realm())
		requireNoSecrets(t, r, r.Describe())
		requireNoSecrets(t, r, r.String())

		// It describes its own roles and no other realm's.
		for _, p := range PurposesIn(realm) {
			require.Contains(t, r.Describe(), string(p))
		}
	}
}

// Both offline roots are absent from ordinary runtime, and neither absence blocks startup.
func TestNeitherOfflineRootIsRequiredAtRuntime(t *testing.T) {
	for _, tc := range []struct {
		realm Realm
		root  Purpose
	}{
		{RealmCore, PurposeOfflineRoot},
		{RealmStandalone, PurposeStandalonePinnedOfflineRoot},
	} {
		r, err := NewGeneratedRingFor(tc.realm)
		require.NoError(t, err)
		r.remove(tc.root)

		require.False(t, HeldAtRuntime(tc.root))
		require.NoError(t, r.Validate(),
			"%s keeps its root offline; demanding it would fail the correct deployment", tc.realm)
	}
}

// A standalone Tower's symmetric roles are secrets, and its signers are signers - the kind
// split holds on every network, not just the public one.
func TestTheKindSplitHoldsInEveryRealm(t *testing.T) {
	for _, realm := range AllRealms() {
		r, err := NewGeneratedRingFor(realm)
		require.NoError(t, err)
		for _, p := range PurposesIn(realm) {
			if KindOf(p) == KindSymmetric {
				_, err = r.Sign(p, []byte("x"))
				require.ErrorIs(t, err, ErrWrongKeyKind, "%s is a secret", p)
				continue
			}
			_, err = r.MAC(p, []byte("x"))
			require.ErrorIs(t, err, ErrWrongKeyKind, "%s is a signer", p)
		}
	}
}

// A foreign tag aimed at a role this ring DOES hold. The required-role check cannot catch
// this one, so it isolates the check on the presented material's realm.
func TestAForeignTagAimedAtALocalSecretIsRefusedAsForeign(t *testing.T) {
	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	standalone, err := NewGeneratedRingFor(RealmStandalone)
	require.NoError(t, err)
	msg := []byte("authenticate me")

	foreign, err := standalone.MAC(PurposeStandaloneBackupEncryption, msg)
	require.NoError(t, err)

	// Required is a Roger Core secret this ring holds, so the required-role realm check
	// passes and only the presented-material check stands.
	err = core.VerifyMAC(PurposeSessionHMAC, msg, foreign)
	require.ErrorIs(t, err, ErrWrongRealm)
	require.NotErrorIs(t, err, ErrPurposeMismatch,
		"material from another network is foreign, not merely the wrong role here")
}

// The same, for signatures.
func TestAForeignSignatureAimedAtALocalRoleIsRefusedAsForeign(t *testing.T) {
	core, err := NewGeneratedRingFor(RealmCore)
	require.NoError(t, err)
	station, err := NewGeneratedRingFor(RealmStation)
	require.NoError(t, err)
	msg := []byte("settle")

	foreign, err := station.Sign(PurposeStationAssertionSigner, msg)
	require.NoError(t, err)

	err = core.Verify(PurposeSettlementSigner, msg, foreign)
	require.ErrorIs(t, err, ErrWrongRealm)
	require.NotErrorIs(t, err, ErrPurposeMismatch)
}

// A role name resolves only inside its own network. Resolving by bare name across realms
// is how "local Station-admission" and "Station-admission/origin signer" get confused -
// two different authorities on two different networks.
func TestARoleNameResolvesOnlyWithinItsOwnRealm(t *testing.T) {
	for _, realm := range AllRealms() {
		for _, p := range PurposesIn(realm) {
			_, ok := LookupIn(realm, string(p))
			require.True(t, ok, "%s must resolve in its own realm %s", p, realm)

			for _, other := range AllRealms() {
				if other == realm {
					continue
				}
				_, ok := LookupIn(other, string(p))
				require.False(t, ok,
					"%s belongs to %s and must not resolve in %s", p, realm, other)
			}
		}
	}
}
