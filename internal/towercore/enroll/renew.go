package enroll

// renew.go re-issues a joined Tower's certificate on the link it already holds.
//
// Certificates are deliberately short-lived, which is only safe if renewal is boring: it
// happens on the existing connection at two thirds of lifetime, with no operator involved.
// That last part is a security property as much as a convenience - an operator who is never
// asked to re-authenticate a Tower has no habit for a phishing mail to exploit.
//
// RENEWAL IS NOT A SECOND ADMISSION. It spends no enrollment token, consumes no quota, and
// creates no Tower. It re-proves possession of the identity key ALREADY ON RECORD and
// issues against the same Tower ID. Every check below exists to keep those two facts from
// drifting apart - because a renewal that could present a new identity key would let anyone
// who learned a Tower ID have a certificate for it issued to themselves.
//
// THE OLD CERTIFICATE IS NOT REVOKED. Overlap is the point of renewing early: revoking at
// renewal would cut the live connection the renewal arrived on. The old one lapses on its
// own schedule, which is what short lifetimes are for.

import (
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"fmt"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/admit"
)

// RenewRequest is one renewal attempt, arriving on an authenticated session.
type RenewRequest struct {
	TowerID string
	Nonce   string
	// IdentityKey must be the key already on record. It is presented rather than assumed
	// so possession is proved fresh, not inherited from the session.
	IdentityKey ed25519.PublicKey
	Signature   []byte
	CSR         []byte // DER, carrying the channel key - which MAY be a new one
	Now         time.Time
}

// RenewResult is the reissued credential.
type RenewResult struct {
	TowerID     string
	Tower       admit.Tower
	Certificate *x509.Certificate
}

// RenewChallenge issues a nonce for a renewal.
//
// It refuses a Tower that may not hold a credential at all, so a revoked operator cannot
// even begin - and learns nothing from trying, since the refusal is the same one an unknown
// Tower gets.
func (e *Enroller) RenewChallenge(towerID string) (Challenge, error) {
	tw, ok := e.cfg.Registry.Get(towerID)
	if !ok || !renewable(tw.State) {
		return Challenge{}, errRejected
	}
	return e.issueChallenge(towerID, PurposeRenew)
}

// renewable reports whether a Tower in this state may be reissued a certificate.
//
// Revoked and expired are terminal here for different reasons. Revocation is a decision we
// made about an operator, and a certificate issued after it would put them straight back on
// the network with the credential we just took away. An expired lease is re-admitted through
// quarantine on fresh proof and fresh probes; renewing one would route around that control.
func renewable(s admit.State) bool {
	switch s {
	case admit.StateQuarantine, admit.StateActive,
		admit.StateDraining, admit.StateSuspended:
		// Suspended and draining still renew: both are reversible, and a Tower whose
		// certificate lapsed while suspended could never be cleared back into service
		// without a full re-enrollment it does not deserve.
		return true
	default:
		return false
	}
}

// Renew reissues the certificate, or refuses without changing anything.
func (e *Enroller) Renew(req RenewRequest) (RenewResult, error) {
	fail := func(reason string) (RenewResult, error) {
		// The reason is for us. It never carries the nonce or any key material - an error
		// string travels into logs and support tickets.
		return RenewResult{}, fmt.Errorf("%w: %s", errRejected, reason)
	}

	tw, ok := e.cfg.Registry.Get(req.TowerID)
	if !ok || !renewable(tw.State) {
		return fail("that Tower may not be reissued a certificate")
	}
	if len(req.IdentityKey) != ed25519.PublicKeySize {
		return fail("no usable identity key")
	}
	now := time.Now()
	if req.Now.IsZero() || absDuration(now.Sub(req.Now)) > e.cfg.MaxSkew {
		return fail("clock outside the admitted skew")
	}

	// Rate limited BEFORE the challenge is spent, so a Tower that is renewing too often
	// does not also burn its nonce on every attempt.
	if e.cfg.MinRenewInterval > 0 && !tw.RenewedAt.IsZero() &&
		now.Sub(tw.RenewedAt) < e.cfg.MinRenewInterval {
		return fail("that Tower renewed too recently")
	}

	// Spent before the signature is checked, exactly as enrollment does: a nonce spent only
	// on success lets an attacker probe the same challenge repeatedly.
	ch, live := e.spendChallenge(req.Nonce, now)
	if !live {
		return fail("unknown, spent, or expired challenge")
	}
	// Domain separation. An enrollment challenge is not a renewal challenge, whatever its
	// signature says - without this the two flows stop being independent.
	if ch.Purpose != PurposeRenew || ch.Subject != req.TowerID {
		return fail("that challenge was issued for something else")
	}
	if len(req.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(req.IdentityKey, ch.SigningInput(), req.Signature) {
		return fail("the challenge signature does not verify")
	}

	// THE CHECK THIS WHOLE PATH EXISTS FOR. The presented identity must be the one already
	// on record; otherwise a renewal is a way to have somebody else's Tower reissued to a
	// key of your choosing.
	if subtle.ConstantTimeCompare(hashKey(req.IdentityKey), []byte(tw.KeyHash)) != 1 {
		return fail("that is not this Tower's identity key")
	}

	csr, err := x509.ParseCertificateRequest(req.CSR)
	if err != nil {
		return fail("the certificate request could not be read")
	}
	if err := csr.CheckSignature(); err != nil {
		return fail("the certificate request is not signed by its own key")
	}
	if sameKey(csr.PublicKey, req.IdentityKey) {
		return fail("the channel key must differ from the identity key")
	}

	cert, err := e.cfg.Authority.Issue(req.TowerID, csr.PublicKey)
	if err != nil {
		return RenewResult{}, err
	}
	tlsHash, err := hashCSRKey(csr.PublicKey)
	if err != nil {
		return fail("that channel key is unusable")
	}

	// One write, and only after everything above passed: a refused renewal must leave a
	// Tower whose registry serial names a certificate somebody actually holds.
	updated, err := e.cfg.Registry.RecordRenewal(req.TowerID, admit.Renewal{
		CertSerial: cert.SerialNumber.String(),
		TLSKeyHash: tlsHash,
		At:         now,
	})
	if err != nil {
		return RenewResult{}, err
	}
	return RenewResult{TowerID: req.TowerID, Tower: updated, Certificate: cert}, nil
}
