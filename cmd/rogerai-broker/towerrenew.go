package main

// towerrenew.go is how a Tower keeps its credential.
//
// # WHY THIS FILE EXISTS
//
// It did not, and that was a production-fatal bug. `internal/towercore/enroll/renew.go` was
// written, reviewed and tested in full - challenge, replay-resistant nonce, identity-key
// proof against the key already on record, CSR reissue, lease carried forward under a CAS -
// and then connected to nothing. There was no route. A Tower's certificate and its lease are
// both 24 hours by default, so:
//
//	EVERY TOWER STOPPED WORKING ONE DAY AFTER ENROLLMENT, permanently, and the operator's
//	only recourse was to enrol again from scratch - through quarantine, needing an
//	administrator, having done nothing wrong.
//
// It is the same class as the Tower going dark after thirty minutes, and worse: that one
// recovered on restart. `make reach` could not see it, because deadcode does not report an
// exported method whose receiver type is instantiated in production - see the note in
// scripts/reachability.sh, added with this fix.
//
// # WHY RENEWAL IS AUTHENTICATED BY THE TOWER AND NOT THE OPERATOR
//
// Enrollment is an account decision and is signed by the operator. Renewal is not: it spends
// no token, consumes no quota, creates no Tower, and changes no identity, owner or lifecycle
// state. It re-proves possession of a key already on record.
//
// Requiring an operator would be actively worse than pointless. Certificates are short-lived,
// so renewal happens on a schedule forever; a human asked to re-authenticate their fleet
// every day acquires exactly the habit a phishing mail needs. The whole point of a short
// certificate is that renewing it is boring.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/enroll"
)

// towerRenewChallenge issues the nonce a renewal is signed over.
func (b *broker) towerRenewChallenge(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID string `json:"tower_id"`
	}
	if json.Unmarshal(body, &req) != nil || req.TowerID == "" {
		jsonErr(w, http.StatusBadRequest, "tower_id required")
		return
	}
	// THE TOWER'S OWN SIGNED REQUEST, with the key already on record. Without this, anyone
	// who learned a Tower ID could ask for its renewal nonce.
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "renewing requires the Tower's own signed request")
		return
	}
	ch, err := ts.enroller.RenewChallenge(req.TowerID)
	if err != nil {
		// Uniform: a revoked Tower and an unknown one must look alike to whoever is probing.
		jsonErr(w, http.StatusBadRequest, "that Tower cannot renew")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nonce":      ch.Nonce,
		"expires_at": ch.Expires.Unix(),
		// The exact bytes to sign, so the client never reconstructs the framing itself and
		// cannot get it subtly wrong.
		"signing_input": base64.StdEncoding.EncodeToString(ch.SigningInput()),
	})
}

// towerRenew reissues the certificate.
//
// The OLD certificate is not revoked. Overlap is the point of renewing early - revoking here
// would cut the live connection the renewal arrived on - and the old one lapses on its own
// schedule, which is what short lifetimes are for.
func (b *broker) towerRenew(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID     string `json:"tower_id"`
		Nonce       string `json:"nonce"`
		IdentityKey string `json:"identity_key"` // base64 raw ed25519
		Signature   string `json:"signature"`    // base64
		CSR         string `json:"csr"`          // base64 DER
	}
	if json.Unmarshal(body, &req) != nil || req.TowerID == "" {
		jsonErr(w, http.StatusBadRequest, "malformed renewal request")
		return
	}
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "renewing requires the Tower's own signed request")
		return
	}
	identity, err1 := base64.StdEncoding.DecodeString(req.IdentityKey)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature)
	csr, err3 := base64.StdEncoding.DecodeString(req.CSR)
	if err1 != nil || err2 != nil || err3 != nil {
		jsonErr(w, http.StatusBadRequest, "malformed renewal request")
		return
	}

	res, err := ts.enroller.Renew(enroll.RenewRequest{
		TowerID: req.TowerID, Nonce: req.Nonce, IdentityKey: identity,
		Signature: sig, CSR: csr, Now: time.Now(),
	})
	if err != nil {
		if errors.Is(err, enroll.ErrUnavailable) {
			jsonErr(w, http.StatusServiceUnavailable, "renewal is temporarily unavailable - retry shortly")
			return
		}
		// Recorded here, not handed to whoever is probing.
		log.Printf("tower: renewal refused for %s: %v", req.TowerID, err)
		jsonErr(w, http.StatusBadRequest, "that renewal is not valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tower_id":      res.TowerID,
		"certificate":   base64.StdEncoding.EncodeToString(res.Certificate.Raw),
		"ca":            base64.StdEncoding.EncodeToString(ts.ca.Root().Raw),
		"state":         string(res.Tower.State),
		"lease_expires": res.Tower.LeaseExpires.Unix(),
		"not_after":     res.Certificate.NotAfter.Unix(),
	})
}

// towerCertRevoke revokes a Tower's certificate NOW - the admin kill switch for a compromised
// or misbehaving Tower whose lease has not yet lapsed.
//
// Contract: features/tower/public_enrollment.feature.
//
// It revokes the serial in the CA (persisted first, so a restart cannot resurrect it) AND
// suspends the Tower, so the refusal takes effect whether the auth path checks the serial or
// the lifecycle state. A revoked serial is what towerCaller now rejects on the Tower's very
// next request, without waiting for the lease.
func (b *broker) towerCertRevoke(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	if ts.ca == nil {
		jsonErr(w, http.StatusServiceUnavailable, "certificate authority is not available")
		return
	}
	var req struct {
		TowerID string `json:"tower_id"`
	}
	if json.Unmarshal(readTowerBody(r), &req) != nil || req.TowerID == "" {
		jsonErr(w, http.StatusBadRequest, "tower_id required")
		return
	}
	tw, ok := ts.registry.Get(req.TowerID)
	if !ok {
		jsonErr(w, http.StatusNotFound, "no such Tower")
		return
	}
	if tw.CertSerial == "" {
		jsonErr(w, http.StatusConflict, "this Tower holds no certificate to revoke")
		return
	}
	serial, ok := new(big.Int).SetString(tw.CertSerial, 10)
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "this Tower's certificate serial is unreadable")
		return
	}
	// REVOKED FIRST, and the failure is fatal to the request: a revocation reported as done
	// but not recorded would be undone by the next restart, and the admin would have no
	// reason to look again.
	if err := ts.ca.Revoke(serial); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "the revocation could not be recorded and has NOT taken effect")
		return
	}
	// Suspend it too, so the effect does not rest on the serial check alone - defence in depth,
	// and it takes the fleet off at once rather than aging out. A Tower not in a state that can
	// be suspended (already terminal) is fine; the serial revocation stands regardless.
	if err := ts.registry.Transition(req.TowerID, admit.StateSuspended); err != nil {
		log.Printf("tower %s: certificate revoked; suspend transition declined (%v) - serial revocation stands", req.TowerID, err)
	}
	ts.inv.Forget(req.TowerID)
	b.forgetRoutable(req.TowerID)
	log.Printf("tower %s: certificate %s revoked by an administrator", req.TowerID, tw.CertSerial)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true, "serial": tw.CertSerial})
}
