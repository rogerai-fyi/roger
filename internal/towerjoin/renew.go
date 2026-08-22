package towerjoin

// renew.go keeps a joined Tower's certificate and lease alive.
//
// Contract: features/tower/public_enrollment.feature.
//
// # THE BUG THIS CLOSES
//
// Core's renewal logic and this Tower-side client were both absent from the running system.
// Certificates and leases are 24 hours by default, so every Tower stopped working a day after
// it enrolled - permanently, with re-enrollment through quarantine as the only recovery, for
// an operator who had done nothing wrong. Renewal is not an optimisation here; without it the
// product has a one-day fuse.
//
// # RENEWED EARLY, AND WHY
//
// At two thirds of the certificate's lifetime, which leaves a third of it as margin. A Tower
// that renewed at the last minute would have no room for a broker restart, a network
// partition, or its own clock being wrong - and the failure mode is not a retry, it is
// re-enrollment through an administrator.
//
// The OLD certificate keeps working until it lapses. That overlap is the point of renewing
// early: Core does not revoke on renewal, because revoking would cut the very connection the
// renewal arrived on.
//
// # IT PROVES POSSESSION AGAIN
//
// The identity key is presented and signed with, not inherited from the connection. Renewal
// spends no token, consumes no quota, creates no Tower and cannot change an identity, an
// owner or a lifecycle state - it re-proves a key already on record and gets a fresh
// certificate for the same Tower ID.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v6/internal/tower"
)

// renewAt is the fraction of a certificate's life after which renewal is attempted.
const renewAtFraction = 2.0 / 3.0

// renewalCheckEvery is how often a serving Tower asks whether it is time yet. Frequent
// enough that a Tower which was asleep or partitioned catches up quickly, and cheap: the
// check is a comparison against a stored time, not a network call.
const renewalCheckEvery = 15 * time.Minute

// DueAt reports when renewal should be attempted for a credential issued over this window.
//
// Exported so the schedule is testable as a pure function rather than only observable by
// waiting for a timer, which is the difference between a test that pins the policy and a
// test that pins the plumbing.
func DueAt(issued, notAfter time.Time) time.Time {
	life := notAfter.Sub(issued)
	if life <= 0 {
		// A credential with no life left is due immediately rather than never - "never" is
		// how a clock problem turns into a Tower that quietly stops.
		return issued
	}
	return issued.Add(time.Duration(float64(life) * renewAtFraction))
}

// RenewIfDue renews when the certificate is far enough through its life, and reports whether
// it did.
//
// It takes `now` so the schedule can be tested without sleeping, and returns (false, nil)
// when nothing was due - a no-op is the ordinary case and must not read as a failure.
func RenewIfDue(st *tower.State, now time.Time) (bool, error) {
	dir := st.Dir()
	adm, found := LoadAdmission(dir)
	if !found || adm.TowerID == "" || adm.NotAfter.IsZero() {
		// Not enrolled yet. Nothing to renew, and not an error: `serve` starts before an
		// operator has necessarily finished setting the Tower up.
		return false, nil
	}
	issued := issuedAt(dir, adm)
	if now.Before(DueAt(issued, adm.NotAfter)) {
		return false, nil
	}
	if err := renew(st, adm); err != nil {
		return false, err
	}
	return true, nil
}

// issuedAt recovers when the current certificate was issued.
//
// From the certificate itself rather than from a timestamp we wrote down: the two can differ
// after a clock change or a file copied between machines, and the certificate is the thing
// Core will actually judge.
func issuedAt(dir string, adm Admission) time.Time {
	raw, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		return adm.NotAfter.Add(-24 * time.Hour)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return adm.NotAfter.Add(-24 * time.Hour)
	}
	return cert.NotBefore
}

// renew performs the exchange and replaces the stored credential.
func renew(st *tower.State, adm Admission) error {
	broker := brokerBase()
	identity, err := st.IdentityKey()
	if err != nil {
		return fmt.Errorf("this Tower's identity key is unreadable: %w", err)
	}
	tlsKey, err := st.TLSKey()
	if err != nil {
		return fmt.Errorf("this Tower's channel key is unreadable: %w", err)
	}

	nonce, signingInput, err := renewChallenge(broker, identity, adm.TowerID)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, tlsKey)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"tower_id":     adm.TowerID,
		"nonce":        nonce,
		"identity_key": base64.StdEncoding.EncodeToString(identity.Public().(ed25519.PublicKey)),
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(identity, signingInput)),
		"csr":          base64.StdEncoding.EncodeToString(csr),
	})
	if err != nil {
		return err
	}

	var out struct {
		TowerID      string `json:"tower_id"`
		Certificate  string `json:"certificate"`
		CA           string `json:"ca"`
		State        string `json:"state"`
		LeaseExpires int64  `json:"lease_expires"`
		NotAfter     int64  `json:"not_after"`
	}
	if err := signedPost(broker+"/tower/renew", identity, body, &out); err != nil {
		return err
	}

	certDER, err := base64.StdEncoding.DecodeString(out.Certificate)
	if err != nil {
		return errors.New("the broker returned a certificate that could not be read")
	}
	caDER, err := base64.StdEncoding.DecodeString(out.CA)
	if err != nil {
		return errors.New("the broker returned an issuer certificate that could not be read")
	}
	// PARSED BEFORE ANYTHING IS OVERWRITTEN. Writing bytes we cannot read would replace a
	// working credential with a broken one, turning a renewal into the outage it exists to
	// prevent.
	if _, err := x509.ParseCertificate(certDER); err != nil {
		return fmt.Errorf("the reissued certificate is unusable: %w", err)
	}
	if _, err := x509.ParseCertificate(caDER); err != nil {
		return fmt.Errorf("the issuer certificate is unusable: %w", err)
	}

	dir := st.Dir()
	if err := os.WriteFile(filepath.Join(dir, certFile), certDER, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, caFile), caDER, 0o644); err != nil {
		return err
	}
	adm.State = out.State
	adm.LeaseExpires = time.Unix(out.LeaseExpires, 0)
	adm.NotAfter = time.Unix(out.NotAfter, 0)
	return saveAdmission(dir, adm)
}

func renewChallenge(broker string, identity ed25519.PrivateKey, towerID string) (nonce string, signingInput []byte, err error) {
	body, err := json.Marshal(map[string]any{"tower_id": towerID})
	if err != nil {
		return "", nil, err
	}
	var out struct {
		Nonce        string `json:"nonce"`
		SigningInput string `json:"signing_input"`
	}
	if err := signedPost(broker+"/tower/renew/challenge", identity, body, &out); err != nil {
		return "", nil, err
	}
	if out.Nonce == "" || out.SigningInput == "" {
		return "", nil, errors.New("the broker issued no renewal challenge")
	}
	raw, err := base64.StdEncoding.DecodeString(out.SigningInput)
	if err != nil {
		return "", nil, errors.New("the broker's renewal challenge could not be read")
	}
	return out.Nonce, raw, nil
}

// KeepRenewed runs the renewal schedule until stopped.
//
// A FAILED RENEWAL IS REPORTED AND RETRIED, never fatal. The current certificate is still
// valid - that is the whole reason for renewing at two thirds - so a broker that is briefly
// down must not take the Tower down with it. What would be unforgivable is failing silently,
// so every attempt that fails says so.
func KeepRenewed(st *tower.State, out io.Writer, stop <-chan struct{},
	ticker func(time.Duration) (<-chan time.Time, func())) {

	tick, cancel := ticker(renewalCheckEvery)
	defer cancel()
	for {
		select {
		case <-stop:
			return
		case <-tick:
			did, err := RenewIfDue(st, time.Now())
			switch {
			case err != nil:
				fmt.Fprintf(out, "could not renew this Tower's certificate: %v\n"+
					"the current one is still valid; this will be retried.\n", err)
			case did:
				fmt.Fprint(out, "renewed this Tower's certificate and lease\n")
			}
		}
	}
}
