package main

// toweredgecert.go issues a Station's edge TLS certificate: the last thing that makes the
// edge path real end to end.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # THE GAP THIS CLOSES
//
// `roger-station csr` mints the Station's TLS key and emits a certificate request, and
// nothing signed it. So a Station could not actually serve consumers over TLS in production -
// the edge tests issued the leaf from the CA root by hand. This is where Core provisions it,
// under a domain Core controls, so an unmodified client trusts it.
//
// # WHO IS TRUSTED FOR WHAT
//
// The operator submits the CSR, signed by their account, and Core issues only for THEIR
// Station and only for the name that Station is entitled to - st-<id> under the relay domain.
// The Station cannot pick its own name (it could otherwise request a certificate for another
// Station), and the CSR's signature is what proves possession of the key the certificate will
// bind. Three checks, and the certificate is worth exactly what the weakest of them is: the
// account owns the Station, the requested name is the derived one, and the key is the CSR's.

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// towerStationEdgeCert handles POST /tower/station/edge-cert.
func (b *broker) towerStationEdgeCert(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body := readTowerBody(r)

	if _, ok := b.towerOperator(r, body); !ok {
		jsonErr(w, http.StatusUnauthorized, "requesting an edge certificate needs a signed-in account")
		return
	}
	ownerPubkey := r.Header.Get("X-Roger-Pubkey")
	if _, found, oerr := b.db.OwnerByPubkey(ownerPubkey); oerr != nil || !found {
		jsonErr(w, http.StatusUnauthorized, "requesting an edge certificate needs a signed-in account")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	if ts.ca == nil {
		jsonErr(w, http.StatusServiceUnavailable, "certificate issuance is not available")
		return
	}

	var req struct {
		StationID string `json:"station_id"`
		CSR       string `json:"csr"` // base64 DER
	}
	if json.Unmarshal(body, &req) != nil || req.StationID == "" || req.CSR == "" {
		jsonErr(w, http.StatusBadRequest, "an edge certificate request names its Station and carries a CSR")
		return
	}

	// THE STATION MUST BE THIS ACCOUNT'S. Answered the same for a Station that does not exist
	// and one behind another account, so this cannot enumerate other people's Stations.
	at, found, err := ts.stations.Station(req.StationID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the Station registry - try again")
		return
	}
	if !found || at.Owner != ownerPubkey {
		jsonErr(w, http.StatusNotFound, "no such Station on this account")
		return
	}

	csrDER, err := base64.StdEncoding.DecodeString(req.CSR)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "the CSR is not valid base64")
		return
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "the CSR could not be read")
		return
	}
	// POSSESSION OF THE KEY, proved by the CSR's own signature. Without this a caller could
	// request a certificate over a key they do not hold - and a certificate whose key is
	// somebody else's is a certificate that authenticates the wrong party.
	if err := csr.CheckSignature(); err != nil {
		jsonErr(w, http.StatusBadRequest, "the CSR is not signed by the key it carries")
		return
	}

	// THE NAME IS CORE'S TO CHOOSE, derived from the Station id under a domain Core controls -
	// never taken from the CSR, which a Station fills in for itself. A CSR that asked for a
	// different name is simply issued the right one; the request does not get to pick.
	relayName := req.StationID + "." + relayDomain()

	leaf, err := ts.ca.IssueEdgeCert(relayName, csr.PublicKey)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not issue the certificate - try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"station_id":  req.StationID,
		"relay_name":  relayName,
		"certificate": base64.StdEncoding.EncodeToString(leaf.Raw),
		"ca":          base64.StdEncoding.EncodeToString(ts.ca.Root().Raw),
		"not_after":   leaf.NotAfter.Unix(),
		"note": "this certificate is for the name " + relayName + " under Roger Core's relay " +
			"domain; install it on the Station and serve with --edge. The private key stays on " +
			"the Station: this issuance never saw it.",
	})
}
