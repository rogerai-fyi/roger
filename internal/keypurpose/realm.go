package keypurpose

import "fmt"

// Realm is the trust root a key belongs to.
//
// Four scenarios in the approved spec are really one property: a standalone trust root has
// no public-network validity; a public RogerAI key has no implicit local admin power; a
// joined Tower key cannot exercise central or leaf authority; a Station key cannot
// exercise Tower or central authority. Each says material issued under one trust root
// carries no authority under another.
//
// One realm check implements all four. That matters more than the line count: a role added
// later inherits the separation automatically, instead of needing somebody to remember to
// add it to a fifth rejection list.
type Realm string

const (
	// RealmCore is the public RogerAI network's own authority.
	RealmCore Realm = "Roger Core"
	// RealmStandalone is a private Tower's pinned local root. It is a different network
	// with a different trust root, and deliberately shares nothing with the public one.
	RealmStandalone Realm = "standalone Tower"
	// RealmTower is a joined Tower's own keys, including its local bridge authorities.
	RealmTower Realm = "joined Tower"
	// RealmStation is a Station's own keys.
	RealmStation Realm = "Station"
)

// AllRealms returns every trust root.
func AllRealms() []Realm { return []Realm{RealmCore, RealmStandalone, RealmTower, RealmStation} }

// --- standalone Tower roles ------------------------------------------------
//
// The twenty roles the spec's standalone scenario names, in its order. A standalone Tower
// runs a whole private network, so it needs its own trust document, policy, admission,
// certificate, grant and ledger authorities - none of which are the public network's.

const (
	PurposeStandalonePinnedOfflineRoot      Purpose = "pinned offline root"
	PurposeStandaloneTrustDocument          Purpose = "local trust-document"
	PurposeStandaloneTrustPublication       Purpose = "local trust-publication"
	PurposeStandalonePolicy                 Purpose = "local policy"
	PurposeStandaloneClientAdmission        Purpose = "local client-admission"
	PurposeStandaloneClientCertificate      Purpose = "local client-certificate"
	PurposeStandaloneBootstrapVerifierAuth  Purpose = "local_bootstrap_verifier_authority signer"
	PurposeStandaloneBootstrapVerifierHMAC  Purpose = "bootstrap-verifier HMAC"
	PurposeStandaloneOperatorSet            Purpose = "local_operator_set signer"
	PurposeStandaloneStationAdmission       Purpose = "local Station-admission"
	PurposeStandaloneStationCertificate     Purpose = "local Station-certificate"
	PurposeStandaloneBridgeAuthority        Purpose = "local_station_bridge_authority"
	PurposeStandaloneBridgeCertificate      Purpose = "local_station_bridge_certificate"
	PurposeStandaloneGrant                  Purpose = "local grant"
	PurposeStandaloneReceiptLedger          Purpose = "local receipt-ledger"
	PurposeStandaloneAdministratorAudit     Purpose = "local administrator-audit"
	PurposeStandaloneKeyEscrowAuthorization Purpose = "local_key_escrow_authorization signer"
	PurposeStandaloneKeyEscrowResult        Purpose = "local_key_escrow_result signer"
	PurposeStandaloneBackupEncryption       Purpose = "backup encryption"
	PurposeStandaloneTLS                    Purpose = "local TLS service"
)

// --- joined Tower roles ----------------------------------------------------

const (
	// PurposeTowerStatementKey is a Tower's persistent identity. It is separate from TLS
	// so rotating a certificate never touches who the Tower is, and stealing a TLS key
	// proves nothing about its identity.
	PurposeTowerStatementKey Purpose = "Tower statement key"
	PurposeTowerTLS          Purpose = "Tower TLS"
	// The Tower-local bridge authorities. Local by name and by authority: Roger Core
	// rejects anything they sign.
	PurposeTowerBridgeAuthority   Purpose = "Tower local_station_bridge_authority"
	PurposeTowerBridgeCertificate Purpose = "Tower local_station_bridge_certificate"
)

// --- Station roles ---------------------------------------------------------

const (
	// PurposeStationAssertionSigner signs what a Station claims to offer.
	PurposeStationAssertionSigner Purpose = "Station provider-assertion signer"
	// PurposeStationTLS is its secure-session identity. Possession of either must not
	// exercise the other purpose.
	PurposeStationTLS Purpose = "Station secure-session TLS"
	// PurposeStationBridgeTLS is the Station-owned key for its local bridge to a Tower.
	PurposeStationBridgeTLS Purpose = "Station bridge TLS"
)

var realmPurposes = map[Realm][]Purpose{
	RealmCore: allCorePurposes,
	RealmStandalone: {
		PurposeStandalonePinnedOfflineRoot, PurposeStandaloneTrustDocument,
		PurposeStandaloneTrustPublication, PurposeStandalonePolicy,
		PurposeStandaloneClientAdmission, PurposeStandaloneClientCertificate,
		PurposeStandaloneBootstrapVerifierAuth, PurposeStandaloneBootstrapVerifierHMAC,
		PurposeStandaloneOperatorSet, PurposeStandaloneStationAdmission,
		PurposeStandaloneStationCertificate, PurposeStandaloneBridgeAuthority,
		PurposeStandaloneBridgeCertificate, PurposeStandaloneGrant,
		PurposeStandaloneReceiptLedger, PurposeStandaloneAdministratorAudit,
		PurposeStandaloneKeyEscrowAuthorization, PurposeStandaloneKeyEscrowResult,
		PurposeStandaloneBackupEncryption, PurposeStandaloneTLS,
	},
	RealmTower: {
		PurposeTowerStatementKey, PurposeTowerTLS,
		PurposeTowerBridgeAuthority, PurposeTowerBridgeCertificate,
	},
	RealmStation: {
		PurposeStationAssertionSigner, PurposeStationTLS, PurposeStationBridgeTLS,
	},
}

var purposeRealm = func() map[Purpose]Realm {
	m := map[Purpose]Realm{}
	for realm, ps := range realmPurposes {
		for _, p := range ps {
			if prior, clash := m[p]; clash {
				// A name in two realms would make RealmOf ambiguous, and a lookup in one
				// network could silently resolve another's role. Refuse to start.
				panic(fmt.Sprintf("purpose %q is declared in both %s and %s", p, prior, realm))
			}
			m[p] = realm
		}
	}
	return m
}()

// PurposesIn returns every role belonging to one trust root.
func PurposesIn(realm Realm) []Purpose {
	return append([]Purpose(nil), realmPurposes[realm]...)
}

// RealmOf reports which trust root a purpose belongs to.
func RealmOf(p Purpose) Realm { return purposeRealm[p] }

// LookupIn resolves a spec role name within one realm. The realm matters: "local
// Station-admission" and "Station-admission/origin signer" are different authorities on
// different networks, and resolving by bare name across realms is how they get confused.
func LookupIn(realm Realm, role string) (Purpose, bool) {
	p := Purpose(role)
	return p, purposeRealm[p] == realm && purposeSet[p]
}
