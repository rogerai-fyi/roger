// Package keypurpose gives every Roger Core signature and secret exactly one named
// purpose.
//
// Contract: features/tower/key_separation.feature.
//
// The property it exists for: compromising a relay, a cookie, a pseudonym, an admin
// channel, or any single signer cannot silently become settlement authority. A valid
// signature from the wrong role is not a weaker credential - it is no credential, and is
// rejected before state, money, network, or rail authority is touched.
//
// Everything later in Phase 2 rests on this. Tower certificates, dispatch leases,
// execution grants, and settlement all name the purpose they require, so this package
// comes before any of them.
package keypurpose

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Purpose is one named authority role. The set is closed and comes from the approved
// spec: a purpose the keyring invented but the spec never named would be unreviewed
// authority, and a role the spec named but the keyring lacks is a missing control. A test
// reads the spec's own table and asserts both directions.
type Purpose string

const (
	PurposeOfflineRoot                           Purpose = "offline root"
	PurposeRogerCoreTLSServiceIdentity           Purpose = "Roger Core TLS service identity"
	PurposeTowerCertificateIssuer                Purpose = "Tower-certificate issuer"
	PurposeStationSecureSessionCertificateIssuer Purpose = "Station secure-session certificate issuer"
	PurposeAdmissionLeaseSigner                  Purpose = "admission-lease signer"
	PurposeTowerLifecycleSigner                  Purpose = "Tower lifecycle signer"
	PurposeStationLifecycleSigner                Purpose = "Station lifecycle signer"
	PurposeStationAdmissionOriginSigner          Purpose = "Station-admission/origin signer"
	PurposeStationEpochSigner                    Purpose = "Station-epoch signer"
	PurposePublicDirectorySigner                 Purpose = "public-directory signer"
	PurposeTrustDocumentSigner                   Purpose = "trust-document signer"
	PurposeTrustDocumentPublicationSigner        Purpose = "trust-document publication signer"
	PurposeTowerCompensationPolicySigner         Purpose = "tower-compensation-policy signer"
	PurposeFundingAllocationPolicySigner         Purpose = "funding-allocation-policy signer"
	PurposePayoutPolicySigner                    Purpose = "payout-policy signer"
	PurposeFeeFinalityPolicySigner               Purpose = "fee-finality-policy signer"
	PurposeMaturityPolicySigner                  Purpose = "maturity-policy signer"
	PurposePayoutEligibilityPolicySigner         Purpose = "payout-eligibility-policy signer"
	PurposeCompensationEnforcementPolicySigner   Purpose = "compensation-enforcement-policy signer"
	PurposeDebtWriteoffPolicySigner              Purpose = "debt-writeoff-policy signer"
	PurposeCompensationEnforcementFindingSigner  Purpose = "compensation-enforcement-finding signer"
	PurposeDebtWriteoffApprovalSigner            Purpose = "debt-writeoff-approval signer"
	PurposeCompensatedCapabilitySigner           Purpose = "compensated-capability signer"
	PurposeConsumerCashCreditSigner              Purpose = "consumer-cash-credit signer"
	PurposePlatformGrantCreditSigner             Purpose = "platform-grant-credit signer"
	PurposeFundingSourceLedgerSigner             Purpose = "funding-source-ledger signer"
	PurposePayoutIdentityVerificationSigner      Purpose = "payout-identity-verification signer"
	PurposeOperatorAccountStatusSigner           Purpose = "operator-account-status signer"
	PurposePayoutTermsAcceptanceSigner           Purpose = "payout-terms-acceptance signer"
	PurposeSanctionsScreeningSigner              Purpose = "sanctions-screening signer"
	PurposePayoutJurisdictionSigner              Purpose = "payout-jurisdiction signer"
	PurposePayoutDestinationVerificationSigner   Purpose = "payout-destination-verification signer"
	PurposeTaxProfileFactSigner                  Purpose = "tax-profile-fact signer"
	PurposeAttemptStateSigner                    Purpose = "attempt-state signer"
	PurposeDispatchLeaseSigner                   Purpose = "dispatch-lease signer"
	PurposeExecutionGrantSigner                  Purpose = "execution-grant signer"
	PurposeCoreTransitObservationSigner          Purpose = "Core-transit-observation signer"
	PurposeSettlementSigner                      Purpose = "settlement signer"
	PurposeCompensationLedgerSigner              Purpose = "compensation-ledger signer"
	PurposeCompensationLedgerHeadSigner          Purpose = "compensation-ledger-head signer"
	PurposeMaturityAuthoritySigner               Purpose = "maturity-authority signer"
	PurposePublicTransparencyCheckpointSigner    Purpose = "public-transparency checkpoint signer"
	PurposeCompensationForfeitureDecisionSigner  Purpose = "compensation-forfeiture decision signer"
	PurposeDebtWriteoffDecisionSigner            Purpose = "debt-writeoff decision signer"
	PurposePayoutAuthorization                   Purpose = "payout authorization"
	PurposePayoutEligibilityDecisionSigner       Purpose = "payout-eligibility decision signer"
	PurposePayoutEligibilityIncidentSigner       Purpose = "payout-eligibility incident signer"
	PurposeTaxWithholdingDecisionSigner          Purpose = "tax-withholding decision signer"
	PurposeTaxCorrectionIncidentSigner           Purpose = "tax-correction incident signer"
	PurposeFeeFinalityIncidentSigner             Purpose = "fee-finality incident signer"
	PurposePaymentWebhookAuthentication          Purpose = "payment-webhook authentication"
	PurposePaymentReconciliationAPI              Purpose = "payment-reconciliation API"
	PurposePayoutRailAPI                         Purpose = "payout-rail API"
	PurposeSessionHMAC                           Purpose = "session HMAC"
	PurposePseudonymHMAC                         Purpose = "pseudonym HMAC"
	PurposeAdminAuthentication                   Purpose = "admin authentication"
	PurposeEvidenceEncryption                    Purpose = "evidence-encryption"
)

// allCorePurposes is Roger Core's closed set, in the spec's order.
var allCorePurposes = []Purpose{
	PurposeOfflineRoot,
	PurposeRogerCoreTLSServiceIdentity,
	PurposeTowerCertificateIssuer,
	PurposeStationSecureSessionCertificateIssuer,
	PurposeAdmissionLeaseSigner,
	PurposeTowerLifecycleSigner,
	PurposeStationLifecycleSigner,
	PurposeStationAdmissionOriginSigner,
	PurposeStationEpochSigner,
	PurposePublicDirectorySigner,
	PurposeTrustDocumentSigner,
	PurposeTrustDocumentPublicationSigner,
	PurposeTowerCompensationPolicySigner,
	PurposeFundingAllocationPolicySigner,
	PurposePayoutPolicySigner,
	PurposeFeeFinalityPolicySigner,
	PurposeMaturityPolicySigner,
	PurposePayoutEligibilityPolicySigner,
	PurposeCompensationEnforcementPolicySigner,
	PurposeDebtWriteoffPolicySigner,
	PurposeCompensationEnforcementFindingSigner,
	PurposeDebtWriteoffApprovalSigner,
	PurposeCompensatedCapabilitySigner,
	PurposeConsumerCashCreditSigner,
	PurposePlatformGrantCreditSigner,
	PurposeFundingSourceLedgerSigner,
	PurposePayoutIdentityVerificationSigner,
	PurposeOperatorAccountStatusSigner,
	PurposePayoutTermsAcceptanceSigner,
	PurposeSanctionsScreeningSigner,
	PurposePayoutJurisdictionSigner,
	PurposePayoutDestinationVerificationSigner,
	PurposeTaxProfileFactSigner,
	PurposeAttemptStateSigner,
	PurposeDispatchLeaseSigner,
	PurposeExecutionGrantSigner,
	PurposeCoreTransitObservationSigner,
	PurposeSettlementSigner,
	PurposeCompensationLedgerSigner,
	PurposeCompensationLedgerHeadSigner,
	PurposeMaturityAuthoritySigner,
	PurposePublicTransparencyCheckpointSigner,
	PurposeCompensationForfeitureDecisionSigner,
	PurposeDebtWriteoffDecisionSigner,
	PurposePayoutAuthorization,
	PurposePayoutEligibilityDecisionSigner,
	PurposePayoutEligibilityIncidentSigner,
	PurposeTaxWithholdingDecisionSigner,
	PurposeTaxCorrectionIncidentSigner,
	PurposeFeeFinalityIncidentSigner,
	PurposePaymentWebhookAuthentication,
	PurposePaymentReconciliationAPI,
	PurposePayoutRailAPI,
	PurposeSessionHMAC,
	PurposePseudonymHMAC,
	PurposeAdminAuthentication,
	PurposeEvidenceEncryption}

// allPurposes is every role on every trust root. Roger Core's are only one realm's worth:
// a standalone Tower, a joined Tower and a Station each run their own authorities, and
// none of them are the public network's.
var allPurposes = func() []Purpose {
	out := append([]Purpose(nil), allCorePurposes...)
	for _, realm := range []Realm{RealmStandalone, RealmTower, RealmStation} {
		out = append(out, realmPurposes[realm]...)
	}
	return out
}()

var purposeSet = func() map[Purpose]bool {
	m := make(map[Purpose]bool, len(allPurposes))
	for _, p := range allPurposes {
		m[p] = true
	}
	return m
}()

// Kind separates roles that SIGN from roles that are a shared secret. A session cookie,
// a pseudonym, an admin token, an evidence key, a webhook secret and two API credentials
// are not signers, and treating them as one would let the same bytes do double duty while
// "one purpose per key" stayed true of the name only.
type Kind string

const (
	KindSigning   Kind = "signing"
	KindSymmetric Kind = "symmetric"
)

// symmetricPurposes is the set the spec names as secrets rather than signers.
var symmetricPurposes = map[Purpose]bool{
	PurposeSessionHMAC:                  true,
	PurposePseudonymHMAC:                true,
	PurposeAdminAuthentication:          true,
	PurposeEvidenceEncryption:           true,
	PurposePaymentWebhookAuthentication: true,
	PurposePaymentReconciliationAPI:     true,
	PurposePayoutRailAPI:                true,
	// A standalone Tower's own shared secrets.
	PurposeStandaloneBootstrapVerifierHMAC: true,
	PurposeStandaloneBackupEncryption:      true,
}

// KindOf reports whether a purpose signs or holds a shared secret.
func KindOf(p Purpose) Kind {
	if symmetricPurposes[p] {
		return KindSymmetric
	}
	return KindSigning
}

// LoadFailure is why a role's key is unusable. The spec's failure scenario names five, and
// they are distinguished because an operator repairing a malformed key does something
// different from one whose key is merely unavailable.
type LoadFailure string

const (
	LoadMissing     LoadFailure = "missing"
	LoadMalformed   LoadFailure = "malformed"
	LoadUnreadable  LoadFailure = "unreadable"
	LoadDuplicated  LoadFailure = "duplicated across roles"
	LoadUnavailable LoadFailure = "unavailable"
)

// heldAtRuntime is false for roles whose private key must NOT be in an ordinary serving
// process. The offline root is the whole example: a correctly operated Core keeps it in a
// vault and issues routine certificates through a bounded replaceable intermediate, so a
// ring that DEMANDED it would fail exactly the deployments that are doing it right.
var heldAtRuntime = map[Purpose]bool{
	PurposeOfflineRoot:                 false,
	PurposeStandalonePinnedOfflineRoot: false,
}

// HeldAtRuntime reports whether an ordinary serving process is expected to hold this
// role's private key.
func HeldAtRuntime(p Purpose) bool {
	held, ok := heldAtRuntime[p]
	return !ok || held
}

// AllPurposes returns every known purpose.
func AllPurposes() []Purpose { return append([]Purpose(nil), allPurposes...) }

// Known reports whether a purpose is in the closed set.
func Known(p Purpose) bool { return purposeSet[p] }

// Lookup resolves a spec role name to its purpose.
func Lookup(role string) (Purpose, bool) {
	p := Purpose(role)
	return p, purposeSet[p]
}

var (
	// ErrPurposeMismatch is a cryptographically valid signature presented for a role it
	// does not hold. It is deliberately distinct from a bad signature: the key is real,
	// the authority is not.
	ErrPurposeMismatch = errors.New("this key is valid, but not for the purpose required")
	// ErrUnknownPurpose is a role outside the closed set. It is refused rather than
	// treated as some permissive default.
	ErrUnknownPurpose = errors.New("that is not a known key purpose")
	// ErrKeyUnavailable is a configured role whose key is missing or unusable. The
	// behavior needing it stops; nothing is minted and no other role is borrowed.
	ErrKeyUnavailable = errors.New("no usable key is loaded for this purpose")
	// ErrBadSignature is an unverifiable signature.
	ErrBadSignature = errors.New("the signature does not verify")
	// ErrWrongRealm is material from one trust root presented under another. A standalone
	// Tower's root, a joined Tower's key, a Station's key and Roger Core's own authority
	// are four separate networks, and none of them vouches for the others.
	ErrWrongRealm = errors.New("this key belongs to another network and trust root")
	// ErrWrongKeyKind is a signing key asked to authenticate, or a shared secret asked to
	// sign. The bytes of one must never do the work of the other.
	ErrWrongKeyKind = errors.New("this purpose is not that kind of key")
)

// Key is one purpose-bound signing key.
//
// Alias, DerivedFrom and Fallback exist so the distinctness check can see the ways one
// root compromise disguises itself as many roles: the same managed-key alias, the same
// derivation root, or a shared emergency key. Distinct public keys alone would miss all
// three.
type Key struct {
	Purpose Purpose
	// KeyID is public and may appear in logs, status, and errors. That is what makes an
	// incident diagnosable without exposing anything.
	KeyID string
	// Alias is the managed-key identifier or alias, when one backs this role.
	Alias string
	// DerivedFrom names the derivation root, when this key is derived. It is secret
	// material and is never rendered.
	DerivedFrom string
	// Fallback names a shared emergency key, when configured.
	Fallback string

	NotBefore time.Time
	NotAfter  time.Time
	// SigningUntil bounds a retired key. During its overlap it may finish in-flight work;
	// after it, the private key stops signing while its history stays verifiable.
	SigningUntil time.Time

	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	// secret backs a symmetric role. Exactly one of priv/secret is ever set.
	secret []byte
	// failure records why this role could not be loaded. A non-empty value fails every
	// use of the role closed; it is cleared only by an explicit repair, never as a side
	// effect of rotating.
	failure LoadFailure
}

// materialCommitment identifies a key's underlying material without revealing it. For a
// signer that is its public key; for a shared secret it is a one-way digest, so the
// distinctness check can compare secrets that must never be rendered.
//
// Symmetric roles used to fall out of that check entirely: they have no public key, and
// the check skips empty values, so every secret role could have shared one secret and
// validated.
func (k *Key) materialCommitment() string {
	if len(k.pub) > 0 {
		return "pub:" + string(k.pub)
	}
	if len(k.secret) > 0 {
		sum := sha256.Sum256(k.secret)
		return "sec:" + string(sum[:])
	}
	return ""
}

// String names the key without its secret.
func (k *Key) String() string {
	return fmt.Sprintf("%s key %s (valid %s to %s)",
		k.Purpose, k.KeyID, k.NotBefore.Format(time.RFC3339), k.NotAfter.Format(time.RFC3339))
}

// Signature carries the purpose it was made for and the key that made it. Both are
// checked: the purpose alone would be a label anyone could assert.
type Signature struct {
	Purpose Purpose `json:"purpose"`
	KeyID   string  `json:"key_id"`
	Sig     string  `json:"sig"`
}

// Ring holds the current key for every purpose, plus retired keys kept for verification.
type Ring struct {
	mu    sync.RWMutex
	realm Realm
	keys  map[Purpose]*Key
	// retired keys are kept for verification only.
	retired map[string]*Key // key ID -> retired key
}

// Realm reports which trust root this ring serves.
func (r *Ring) Realm() Realm { return r.realm }

// inRealm refuses a role that belongs to another network. Asking a standalone Tower to
// sign with a Roger Core purpose is a configuration error, not a signature that happens
// not to check out, and it must say so.
func (r *Ring) inRealm(p Purpose) error {
	if got := RealmOf(p); got != r.realm {
		return fmt.Errorf("%w: %s is a %s role, and this is a %s keyring",
			ErrWrongRealm, p, got, r.realm)
	}
	return nil
}

// NewGeneratedRing mints a fresh, fully populated Roger Core ring. Used by tests and by a
// first-run initialization; production loads its keys instead.
func NewGeneratedRing() (*Ring, error) { return NewGeneratedRingFor(RealmCore) }

// NewGeneratedRingFor mints a ring for one trust root, holding that realm's roles and no
// others. A ring never carries another network's keys: material that is never held cannot
// be stolen, and the realm check is not the only thing standing between the two.
func NewGeneratedRingFor(realm Realm) (*Ring, error) {
	roles := realmPurposes[realm]
	if len(roles) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrWrongRealm, realm)
	}
	r := &Ring{realm: realm, keys: map[Purpose]*Key{}, retired: map[string]*Key{}}
	now := time.Now()
	for _, p := range roles {
		k, err := generateKey(p, now)
		if err != nil {
			return nil, err
		}
		r.keys[p] = k
	}
	return r, nil
}

func generateKey(p Purpose, now time.Time) (*Key, error) {
	k := &Key{Purpose: p, NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour)}

	if KindOf(p) == KindSymmetric {
		// Independent material per role. Deriving these from one root would be exactly
		// the "cross-role key derived from the possessed bytes" the spec forbids.
		k.secret = make([]byte, 32)
		if _, err := rand.Read(k.secret); err != nil {
			return nil, err
		}
		// A one-way commitment, so a secret role still has a public identifier that can
		// appear in logs and errors without exposing anything.
		sum := sha256.Sum256(k.secret)
		k.KeyID = hex.EncodeToString(sum[:8])
		return k, nil
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	k.KeyID, k.pub, k.priv = hex.EncodeToString(pub[:8]), pub, priv
	return k, nil
}

// Validate checks the ring before anything is signed.
//
// It fails startup rather than a later signature on purpose: a service that discovers a
// missing or shared authority at its first settlement has already accepted the job.
func (r *Ring) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var problems []string
	realmRoles := realmPurposes[r.realm]
	for _, p := range realmRoles {
		k := r.keys[p]
		if k == nil && HeldAtRuntime(p) {
			problems = append(problems, fmt.Sprintf("no key is configured for %q", p))
			continue
		}
		if k != nil && k.failure != "" {
			problems = append(problems, fmt.Sprintf("the key for %q is %s", p, k.failure))
		}
	}

	// Every way one root can wear several hats. Sharing any of these means a single
	// compromise silently holds several authorities.
	for _, dim := range []struct {
		what string
		of   func(*Key) string
	}{
		// The underlying MATERIAL, not KeyID. KeyID is an exported display field a config
		// loader can set independently of the key it names, so comparing it would let two
		// roles load the identical private key under different labels and validate -
		// precisely the one-root-many-hats case this check exists to stop. For a shared
		// secret the commitment is a digest, so secrets are compared without being held.
		{"public key", func(k *Key) string { return k.materialCommitment() }},
		{"managed-key alias", func(k *Key) string { return k.Alias }},
		{"derived-key root", func(k *Key) string { return k.DerivedFrom }},
		{"fallback key", func(k *Key) string { return k.Fallback }},
	} {
		seen := map[string][]Purpose{}
		for _, p := range realmRoles {
			k := r.keys[p]
			if k == nil {
				continue
			}
			if v := dim.of(k); v != "" {
				seen[v] = append(seen[v], p)
			}
		}
		for _, roles := range seen {
			if len(roles) < 2 {
				continue
			}
			names := make([]string, 0, len(roles))
			for _, p := range roles {
				// The public key ID is named alongside the purpose, because an operator
				// fixing this needs to know WHICH key to replace. The shared value itself
				// is never named: a derivation root and a fallback key are secrets.
				names = append(names, fmt.Sprintf("%s (key %s)", p, r.keys[p].KeyID))
			}
			sort.Strings(names)
			problems = append(problems, fmt.Sprintf(
				"these purposes share one %s: %s", dim.what, strings.Join(names, ", ")))
		}
	}

	// Retired keys still resolve during verification, so a retired entry colliding with a
	// current one would make key lookup nondeterministic.
	for id, old := range r.retired {
		for _, p := range realmRoles {
			if cur := r.keys[p]; cur != nil && cur.KeyID == id && cur != old {
				problems = append(problems, fmt.Sprintf(
					"retired key %s collides with the current key for %q", id, p))
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("the key configuration is unsafe: %s", strings.Join(problems, "; "))
}

// MarkUnloadable records that a role's key could not be loaded. Every use of the role
// then fails closed, and startup fails, until the role is repaired explicitly.
func (r *Ring) MarkUnloadable(p Purpose, why LoadFailure) error {
	if !Known(p) {
		return fmt.Errorf("%w: %q", ErrUnknownPurpose, p)
	}
	if err := r.inRealm(p); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.keys[p]
	if k == nil {
		k = &Key{Purpose: p}
		r.keys[p] = k
	}
	k.failure = why
	// The material is dropped, not kept beside a failure flag: a key that is malformed or
	// duplicated must not remain usable by any path that forgets to check the flag.
	k.priv, k.secret = nil, nil
	return nil
}

// usableLocked resolves the key for a purpose, or explains why there is none.
func (r *Ring) usableLocked(p Purpose) (*Key, error) {
	k := r.keys[p]
	if k == nil {
		return nil, fmt.Errorf("%w: %s", ErrKeyUnavailable, p)
	}
	if k.failure != "" {
		// Named, so an operator repairs the right thing. Nothing is minted to cover the
		// gap and no other role's key is reached for - either would turn a missing
		// authority into a silent one.
		return nil, fmt.Errorf("%w: the %s key is %s", ErrKeyUnavailable, p, k.failure)
	}
	return k, nil
}

// signingBytes binds the purpose into what is signed, so a signature cannot be relabelled
// by editing the envelope. Without this the purpose tag would be a claim, not a binding.
func signingBytes(p Purpose, msg []byte) []byte {
	b := make([]byte, 0, len(p)+1+len(msg))
	b = append(b, []byte(p)...)
	b = append(b, 0)
	return append(b, msg...)
}

// Sign produces a signature bound to one purpose.
func (r *Ring) Sign(p Purpose, msg []byte) (Signature, error) {
	if !Known(p) {
		return Signature{}, fmt.Errorf("%w: %q", ErrUnknownPurpose, p)
	}
	if err := r.inRealm(p); err != nil {
		return Signature{}, err
	}
	if KindOf(p) != KindSigning {
		return Signature{}, fmt.Errorf("%w: %s is a shared secret, not a signer", ErrWrongKeyKind, p)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	k, err := r.usableLocked(p)
	if err != nil {
		return Signature{}, err
	}
	if k.priv == nil {
		return Signature{}, fmt.Errorf("%w: %s", ErrKeyUnavailable, p)
	}
	// The validity window is enforced HERE, on the production path. It previously lived
	// only in a predicate the tests called, which made "the old private key stops signing
	// after overlap" true of the test helper and not of the keyring.
	if !r.canSignWithLocked(k) {
		return Signature{}, fmt.Errorf("%w: the %s key is outside its signing window", ErrKeyUnavailable, p)
	}
	return Signature{
		Purpose: p,
		KeyID:   k.KeyID,
		Sig:     hex.EncodeToString(ed25519.Sign(k.priv, signingBytes(p, msg))),
	}, nil
}

// Verify accepts only a key whose purpose is exactly the one required.
//
// The order matters: the purpose is checked before the cryptography, so a valid signature
// from the wrong role can never reach the state, money, network, or rail path it was
// presented for.
func (r *Ring) Verify(required Purpose, msg []byte, sig Signature) error {
	if !Known(required) {
		return fmt.Errorf("%w: %q", ErrUnknownPurpose, required)
	}
	if err := r.inRealm(required); err != nil {
		return err
	}
	// The PRESENTED material's trust root, checked before its purpose. Material from
	// another network is refused as foreign rather than as a wrong-purpose key: those are
	// different problems, and an operator needs to see which one they have.
	if got := RealmOf(sig.Purpose); got != r.realm {
		return fmt.Errorf("%w: a %s signature was presented to a %s keyring",
			ErrWrongRealm, got, r.realm)
	}
	if KindOf(required) != KindSigning {
		return fmt.Errorf("%w: %s is a shared secret, not a signer", ErrWrongKeyKind, required)
	}
	// A fail-fast layer, before the ring is even consulted. It is strictly subsumed by
	// the key-purpose check below - deleting it leaves the suite green, and that is
	// recorded rather than hidden - but it is kept deliberately: this is a validation
	// guard on the money path, and cheap redundancy there is worth more than the four
	// lines it costs.
	if sig.Purpose != required {
		return fmt.Errorf("%w: %s presented for %s", ErrPurposeMismatch, sig.Purpose, required)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	k := r.findLocked(sig.KeyID)
	if k == nil {
		// Deliberately indistinguishable from a wrong-purpose key below: a discriminating
		// error would let an attacker enumerate valid key IDs by probing.
		return fmt.Errorf("%w: no key of that identity holds %s", ErrPurposeMismatch, required)
	}
	// The key's own purpose is authoritative, not the envelope's claim about it.
	if k.Purpose != required {
		return fmt.Errorf("%w: key %s holds %s", ErrPurposeMismatch, k.KeyID, k.Purpose)
	}
	// ed25519.Verify panics on a wrong-size public key, and a panic report is exactly the
	// surface that must never carry key material. Refuse it as a bad signature instead.
	if len(k.pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}

	raw, err := hex.DecodeString(sig.Sig)
	if err != nil || !ed25519.Verify(k.pub, signingBytes(required, msg), raw) {
		return ErrBadSignature
	}
	return nil
}

func (r *Ring) findLocked(keyID string) *Key {
	for _, k := range r.keys {
		if k.KeyID == keyID {
			return k
		}
	}
	// Retired keys still verify: retiring a signer must not invalidate what it lawfully
	// signed while it held authority.
	return r.retired[keyID]
}

// MAC authenticates a message under a shared-secret role.
func (r *Ring) MAC(p Purpose, msg []byte) (Signature, error) {
	if !Known(p) {
		return Signature{}, fmt.Errorf("%w: %q", ErrUnknownPurpose, p)
	}
	if err := r.inRealm(p); err != nil {
		return Signature{}, err
	}
	if KindOf(p) != KindSymmetric {
		return Signature{}, fmt.Errorf("%w: %s is a signer, not a shared secret", ErrWrongKeyKind, p)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	k, err := r.usableLocked(p)
	if err != nil {
		return Signature{}, err
	}
	if len(k.secret) == 0 {
		return Signature{}, fmt.Errorf("%w: %s", ErrKeyUnavailable, p)
	}
	if !r.canSignWithLocked(k) {
		return Signature{}, fmt.Errorf("%w: the %s secret is outside its window", ErrKeyUnavailable, p)
	}
	return Signature{Purpose: p, KeyID: k.KeyID, Sig: hex.EncodeToString(macBytes(k.secret, p, msg))}, nil
}

// VerifyMAC checks a message under a shared-secret role, and only that role.
func (r *Ring) VerifyMAC(required Purpose, msg []byte, sig Signature) error {
	if !Known(required) {
		return fmt.Errorf("%w: %q", ErrUnknownPurpose, required)
	}
	if err := r.inRealm(required); err != nil {
		return err
	}
	if got := RealmOf(sig.Purpose); got != r.realm {
		return fmt.Errorf("%w: a %s tag was presented to a %s keyring",
			ErrWrongRealm, got, r.realm)
	}
	if KindOf(required) != KindSymmetric {
		return fmt.Errorf("%w: %s is a signer, not a shared secret", ErrWrongKeyKind, required)
	}
	if sig.Purpose != required {
		return fmt.Errorf("%w: %s presented for %s", ErrPurposeMismatch, sig.Purpose, required)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	k, err := r.usableLocked(required)
	if err != nil {
		return err
	}
	if len(k.secret) == 0 {
		return fmt.Errorf("%w: %s", ErrKeyUnavailable, required)
	}

	got, decErr := hex.DecodeString(sig.Sig)
	// The error must be checked: hex.DecodeString returns the successfully decoded prefix
	// alongside it, so ignoring it would accept a valid tag with garbage appended.
	if decErr != nil {
		return ErrBadSignature
	}
	// Constant time, so a wrong tag reveals nothing about how much of it was right.
	if subtle.ConstantTimeCompare(got, macBytes(k.secret, required, msg)) != 1 {
		return ErrBadSignature
	}
	return nil
}

// macBytes binds the purpose into the tag exactly as signingBytes does for a signature.
func macBytes(secret []byte, p Purpose, msg []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(signingBytes(p, msg))
	return m.Sum(nil)
}

// Rotate replaces a purpose's key, keeping the purpose. The retired key may finish
// in-flight work for the overlap and verifies forever after.
func (r *Ring) Rotate(p Purpose, overlap time.Duration) (*Key, error) {
	if !Known(p) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPurpose, p)
	}
	if err := r.inRealm(p); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if k := r.keys[p]; k != nil && k.failure != "" {
		// Repairing a role is an explicit act. Rotating over a failure would clear it as
		// a side effect of asking for a new key, which is how a known-bad role quietly
		// returns to service.
		return nil, fmt.Errorf("%w: the %s key is %s and must be repaired, not rotated",
			ErrKeyUnavailable, p, k.failure)
	}

	now := time.Now()
	next, err := generateKey(p, now)
	if err != nil {
		return nil, err
	}
	if old := r.keys[p]; old != nil {
		old.SigningUntil = now.Add(overlap)
		// Carry the configuration forward so a rotated ring stays distinct: a replacement
		// that dropped its alias could silently collide with another role.
		next.Alias, next.DerivedFrom, next.Fallback = old.Alias, old.DerivedFrom, old.Fallback
		r.retired[old.KeyID] = old
	}
	r.keys[p] = next
	// A copy: the live record is read under the ring's lock by Validate and Describe, so
	// handing the caller a pointer to it invites an unsynchronised write.
	out := *next
	return &out, nil
}

func (r *Ring) canSignWithLocked(k *Key) bool {
	if k == nil {
		return false
	}
	now := time.Now()
	if cur := r.keys[k.Purpose]; cur != nil && cur.KeyID == k.KeyID {
		// The current key still has to be inside its own bounded validity interval; an
		// expired key that signed forever would make that interval decorative.
		return !now.Before(k.NotBefore) && now.Before(k.NotAfter)
	}
	// A retired key may finish in-flight work for its overlap, and no longer.
	return !k.SigningUntil.IsZero() && now.Before(k.SigningUntil)
}

// String summarises the ring without any secret.
func (r *Ring) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fmt.Sprintf("%s keyring with %d of %d purposes configured",
		r.realm, len(r.keys), len(realmPurposes[r.realm]))
}

// Describe lists each configured role and its public key ID. Public key IDs and expiry may
// appear; private keys, symmetric secrets, and derivation roots never do.
func (r *Ring) Describe() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	for _, p := range realmPurposes[r.realm] {
		k := r.keys[p]
		if k == nil {
			fmt.Fprintf(&b, "%s: no key configured\n", p)
			continue
		}
		fmt.Fprintf(&b, "%s: %s until %s\n", p, k.KeyID, k.NotAfter.Format(time.RFC3339))
	}
	return b.String()
}
