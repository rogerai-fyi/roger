package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// Private bands ("frequency codes"): an owner hides a node from the public market
// and hands out a secret frequency code so only people who have the code can find
// and route to it. A band is "a grant for discovery visibility" - it mirrors the
// grant patterns (owner-scoped, hash-only secret, shown once). See BANDS-DESIGN.

// newBandID mints a fresh "band_<rand>" DB id (NOT the secret code).
func newBandID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "band_" + hex.EncodeToString(b)
}

// mintBandForNode mints a private band bound to nodeID for owner, enforcing the
// free cap (CountActiveBands vs BandQuota). It returns the band plus the secret
// frequency code shown ONCE (empty display means "already had a band" is handled by
// the caller via BandByNode before calling this). On a cap hit it returns a non-nil
// error message string the caller surfaces as a 403. The code is generated with
// crypto/rand; only sha256(canonical tail) + the cosmetic display are stored.
func (b *broker) mintBandForNode(owner store.Owner, nodeID string) (store.Band, string, string) {
	now := time.Now()
	active, err := b.db.CountActiveBands(owner.Pubkey, now)
	if err != nil {
		return store.Band{}, "", "could not check your band quota"
	}
	if active >= store.BandQuota(owner.Pubkey) {
		return store.Band{}, "", "private band limit reached (free plan allows 1) - revoke an existing band first"
	}
	display, tail := protocol.NewBandCode()
	band := store.Band{
		ID: newBandID(), CodeHash: protocol.BandCodeHash(tail), CodeDisplay: display,
		Owner: owner.Pubkey, NodeID: nodeID, CreatedAt: now.Unix(),
	}
	if err := b.db.CreateBand(band); err != nil {
		return store.Band{}, "", "could not create the private band"
	}
	return band, display, ""
}

// bands handles GET /bands (owner-auth: list the caller-owner's private bands). The
// secret code is NEVER returned here (only the cosmetic display + id/status) - it is
// shown once at mint. Mirrors grantList's owner-scoping.
func (b *broker) bands(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	owner, ok := b.requireOwner(r)
	if !ok {
		jsonErr(w, http.StatusForbidden, "managing private bands requires a GitHub-linked owner - run `roger login`")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := b.db.BandsByOwner(owner.Pubkey)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(list))
	for _, bd := range list {
		out = append(out, bandView(bd, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bands": out})
}

// bandsByID handles DELETE /bands/{id} (owner-scoped revoke). /bands/resolve is
// routed to bandResolve directly (a more specific mux pattern), so this only ever
// sees a band id here.
func (b *broker) bandsByID(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/bands/"), "/")
	if id == "" || id == "resolve" {
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	owner, ok := b.requireOwner(r)
	if !ok {
		jsonErr(w, http.StatusForbidden, "managing private bands requires a GitHub-linked owner - run `roger login`")
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	revoked, err := b.db.SetBandRevoked(id, owner.Pubkey, true)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !revoked {
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true})
}

// bandView is the public (secret-free) JSON shape of a band. NEVER includes the
// code hash or the secret code; CodeDisplay is cosmetic and safe to show the owner.
func bandView(bd store.Band, now time.Time) map[string]any {
	status := "active"
	if bd.Revoked {
		status = "revoked"
	} else if bd.Expired(now) {
		status = "expired"
	}
	return map[string]any{
		"id": bd.ID, "display": bd.CodeDisplay, "label": bd.Label,
		"node_id": bd.NodeID, "models": bd.Models,
		"expires_at": bd.ExpiresAt, "revoked": bd.Revoked, "status": status,
		"created_at": bd.CreatedAt,
	}
}

// bandResolveReq is the POST /bands/resolve body: a frequency code (in any form the
// user typed it - cosmetic part / spaces / dashes are tolerated).
type bandResolveReq struct {
	Freq string `json:"freq"`
}

// bandResolve handles POST /bands/resolve - PUBLIC (no login, signed-ok): given a
// frequency code, return the band's node offers so a client can tune in. It is
// CONSTANT-WORK + UNIFORM-ERROR by design: we ALWAYS canonicalize+hash+look up, and
// on ANY miss (unknown / revoked / expired / node offline) we return the IDENTICAL
// 404 {"offers":[]} that a valid-but-offline band returns. That removes the
// enumeration oracle - there is no status/timing/shape difference an attacker could
// use to tell "wrong code" from "right code, nobody home", so 40-bit codes can't be
// probed by watching responses. We NEVER log the raw code (only band_id/display).
func (b *broker) bandResolve(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	cors(w)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
	var req bandResolveReq
	_ = json.Unmarshal(body, &req)

	// Constant-work: always hash + look up, even for an empty/garbage input (which
	// hashes the empty tail and never matches). The uniform "no station" reply is the
	// single exit for every negative case below.
	band, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(req.Freq))
	now := time.Now()
	offers, ok := b.bandOffers(band, found, now)
	if !ok {
		// UNIFORM negative: same status + same shape for wrong / revoked / expired /
		// offline. No oracle. Do not name the band or log the code.
		writeJSON(w, http.StatusNotFound, map[string]any{"offers": []offerView{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"offers": offers,
		"band":   map[string]any{"display": band.CodeDisplay, "node_id": band.NodeID},
	})
}

// bandOffers returns the live offers for a resolved band (filtered to the band's
// model allow-list) and ok=true ONLY when the band is valid, live, AND its node is
// currently on air with at least one matching offer. Every other case returns
// ok=false so the caller emits the single uniform negative reply (no oracle). The
// hash lookup having "found" a row is treated identically to "not found" on any
// failure past that point.
func (b *broker) bandOffers(band store.Band, found bool, now time.Time) ([]offerView, bool) {
	if !found || !band.Active(now) {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n, ok := b.nodes[band.NodeID]
	if !ok {
		return nil, false
	}
	if b.isBanned(band.NodeID) { // metricsMu, separate from b.mu held here
		return nil, false
	}
	if time.Since(b.lastSeen[band.NodeID]) >= nodeTTL {
		return nil, false // valid band, but the station is off air -> uniform negative
	}
	// A private band carries the SAME real per-offer metrics as the public /discover
	// path - signal/terms, success(+seen), verified, ttft, ctx(+estimated), hw,
	// in-flight - via the shared enrichOffersForNode (b.mu held here). The band's
	// model allow-list is applied as the deny filter; demand-probe scheduling is OFF
	// (this is a tune-in/liveness read, kept cheap, not a market browse).
	out := b.enrichOffersForNode(nil, n, now, band.ModelDenied, false)
	if len(out) == 0 {
		return nil, false // band's models are not currently offered -> uniform negative
	}
	// Same $-tier signal as the public feed: a private band has no public peers to its
	// own offer set, so it is graded against the same-model external reference (the
	// internal-median fallback needs >=3 online peers, which a single band cannot reach).
	b.assignPriceTiers(out)
	return out, true
}

// resolveFreqAllow resolves an X-Roger-Freq header on a relay request to the set of
// nodes the request may reach. It uses the SAME constant-work lookup as
// bandResolve (always hash, uniform on miss). On a valid live band it returns
// {node:true}; on any miss it returns an empty (non-nil) set, which the caller
// treats as "no station on that frequency" with the same uniform message. The
// matched band (for a model-allow check) is returned too. A missing header returns
// (nil, zero band) so the caller routes the public market path unchanged.
func (b *broker) resolveFreqAllow(freq string, now time.Time) (allow map[string]bool, band store.Band, present bool) {
	if freq == "" {
		return nil, store.Band{}, false
	}
	bnd, found, _ := b.db.BandByCodeHash(protocol.BandCodeHash(freq))
	// Reuse bandOffers' liveness gate for the uniform decision (ignore the offers,
	// we only need the on-air verdict), so resolve and relay agree exactly.
	_, ok := b.bandOffers(bnd, found, now)
	if !ok {
		return map[string]bool{}, store.Band{}, true // present-but-no-station (uniform)
	}
	return map[string]bool{bnd.NodeID: true}, bnd, true
}
