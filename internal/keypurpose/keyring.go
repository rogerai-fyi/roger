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

// allPurposes is the closed set, in the spec's order.
var allPurposes = []Purpose{
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

var purposeSet = func() map[Purpose]bool {
	m := make(map[Purpose]bool, len(allPurposes))
	for _, p := range allPurposes {
		m[p] = true
	}
	return m
}()

// heldAtRuntime is false for roles whose private key must NOT be in an ordinary serving
// process. The offline root is the whole example: a correctly operated Core keeps it in a
// vault and issues routine certificates through a bounded replaceable intermediate, so a
// ring that DEMANDED it would fail exactly the deployments that are doing it right.
var heldAtRuntime = map[Purpose]bool{PurposeOfflineRoot: false}

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
	mu      sync.RWMutex
	keys    map[Purpose]*Key
	retired map[string]*Key // key ID -> retired key
}

// NewGeneratedRing mints a fresh, fully populated ring. Used by tests and by a first-run
// standalone initialization; production loads its keys instead.
func NewGeneratedRing() (*Ring, error) {
	r := &Ring{keys: map[Purpose]*Key{}, retired: map[string]*Key{}}
	now := time.Now()
	for _, p := range allPurposes {
		k, err := generateKey(p, now)
		if err != nil {
			return nil, err
		}
		r.keys[p] = k
	}
	return r, nil
}

func generateKey(p Purpose, now time.Time) (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return &Key{
		Purpose:   p,
		KeyID:     hex.EncodeToString(pub[:8]),
		NotBefore: now,
		NotAfter:  now.Add(90 * 24 * time.Hour),
		pub:       pub,
		priv:      priv,
	}, nil
}

// Validate checks the ring before anything is signed.
//
// It fails startup rather than a later signature on purpose: a service that discovers a
// missing or shared authority at its first settlement has already accepted the job.
func (r *Ring) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var problems []string
	for _, p := range allPurposes {
		if r.keys[p] == nil && HeldAtRuntime(p) {
			problems = append(problems, fmt.Sprintf("no key is configured for %q", p))
		}
	}

	// Every way one root can wear several hats. Sharing any of these means a single
	// compromise silently holds several authorities.
	for _, dim := range []struct {
		what string
		of   func(*Key) string
	}{
		// The RAW public key, not KeyID. KeyID is an exported display field a config
		// loader can set independently of the key it names, so comparing it would let two
		// roles load the identical private key under different labels and validate -
		// precisely the one-root-many-hats case this check exists to stop.
		{"public key", func(k *Key) string { return string(k.pub) }},
		{"managed-key alias", func(k *Key) string { return k.Alias }},
		{"derived-key root", func(k *Key) string { return k.DerivedFrom }},
		{"fallback key", func(k *Key) string { return k.Fallback }},
	} {
		seen := map[string][]Purpose{}
		for _, p := range allPurposes {
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
		for _, p := range allPurposes {
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	k := r.keys[p]
	if k == nil || k.priv == nil {
		// Fail closed. Nothing is generated to paper over the gap, and no other role's
		// key is borrowed - either would turn a missing authority into a silent one.
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

// Rotate replaces a purpose's key, keeping the purpose. The retired key may finish
// in-flight work for the overlap and verifies forever after.
func (r *Ring) Rotate(p Purpose, overlap time.Duration) (*Key, error) {
	if !Known(p) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPurpose, p)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

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
	return fmt.Sprintf("keyring with %d of %d purposes configured", len(r.keys), len(allPurposes))
}

// Describe lists each configured role and its public key ID. Public key IDs and expiry may
// appear; private keys, symmetric secrets, and derivation roots never do.
func (r *Ring) Describe() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	for _, p := range allPurposes {
		k := r.keys[p]
		if k == nil {
			fmt.Fprintf(&b, "%s: no key configured\n", p)
			continue
		}
		fmt.Fprintf(&b, "%s: %s until %s\n", p, k.KeyID, k.NotAfter.Format(time.RFC3339))
	}
	return b.String()
}
