package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
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
// free cap (CountActiveBands vs BandQuota). It returns the band plus the secret full
// frequency CODE shown ONCE (the caller reveals it once; band.CodeDisplay is the masked,
// non-recoverable display that is PERSISTED). On a cap hit it returns a non-empty error
// message string the caller surfaces as a 403. The code is generated with crypto/rand;
// only sha256(canonical tail) + the MASKED display are stored - never the full code.
func (b *broker) mintBandForNode(owner store.Owner, nodeID string) (store.Band, string, string) {
	now := time.Now()
	active, err := b.db.CountActiveBands(owner.Pubkey, now)
	if err != nil {
		return store.Band{}, "", "could not check your band quota"
	}
	if active >= store.BandQuota(owner.Pubkey) {
		return store.Band{}, "", b.quotaRefusal(owner, now)
	}
	code, display, tail := protocol.NewBandCode()
	band := store.Band{
		ID: newBandID(), CodeHash: protocol.BandCodeHash(tail), CodeDisplay: display,
		Owner: owner.Pubkey, NodeID: nodeID, CreatedAt: now.Unix(),
	}
	if err := b.db.CreateBand(band); err != nil {
		return store.Band{}, "", "could not create the private band"
	}
	return band, code, ""
}

// quotaRefusal explains WHY a mint was refused by naming the band that is in the way.
//
// THE INCIDENT (2026-08-07): the old copy was "private band limit reached (free plan
// allows 1) - revoke an existing band first". Every word of that is true and none of it is
// usable: it names an action no client could perform at the time, and it never says WHICH
// band is holding the slot. The founder's blocking band turned out to be on a model on a
// different machine entirely - a fact no surface could have told them.
//
// So: name the node holding the slot, and lead with MOVE (which keeps the frequency code
// alive) rather than revoke (which burns it). It deliberately never mentions buying more
// bands: there is no purchase path, and inventing one in an error message would be a lie.
// Only the caller's OWN bands are ever named, so this can never leak another owner's node.
func (b *broker) quotaRefusal(owner store.Owner, now time.Time) string {
	const base = "private band limit reached (free plan allows 1)"
	held, err := b.db.BandsByOwner(owner.Pubkey)
	if err != nil {
		return base + " - move or revoke your existing band first"
	}
	for _, bd := range held {
		if !bd.Active(now) {
			continue // a revoked or expired band is not what is blocking them
		}
		return base + " - yours is on " + bd.NodeID +
			". Move it to this model to keep the same frequency code, or revoke it first"
	}
	return base + " - move or revoke your existing band first"
}

// remaskExistingBands runs the one-time, IDEMPOTENT band-display re-mask migration at
// startup. FIX #2 stopped NEW mints from persisting the secret in CodeDisplay, but bands
// minted BEFORE it still hold a recoverable "freq · TAIL" display on disk - so
// CanonicalBandTail(CodeDisplay)/BandCodeHash(CodeDisplay) resolve the band straight out of
// stored state. This rewrites every existing band's CodeDisplay to the masked,
// non-recoverable cosmetic form in place. The CodeHash (the resolve lookup key) is left
// UNCHANGED, so the owner's one-time full code still tunes in; ONLY the display changes.
// Idempotent (an already-masked row is skipped), so it is safe to run on every boot.
// Failure is non-fatal (logged): the broker still boots; the migration retries next start.
//
// NOTE: after this runs, an owner can NO LONGER re-view the code via bandView - that is
// intended (shown-once model). The full code is shown only at mint; if lost, the owner
// revokes the band and re-mints. CodeDisplay is purely cosmetic and deliberately
// non-recoverable.
func (b *broker) remaskExistingBands() {
	n, err := b.db.RemaskBandDisplays()
	if err != nil {
		log.Printf("band re-mask migration failed: %v (existing band displays left as-is; will retry next start)", err)
		return
	}
	if n > 0 {
		log.Printf("band re-mask migration: scrubbed the recoverable tail from %d existing band display(s)", n)
	}
}

// bands handles GET /bands (owner-auth: list the caller-owner's private bands). The
// secret code is NEVER returned here (only the cosmetic display + id/status) - it is
// shown once at mint. Mirrors grantList's owner-scoping.
func (b *broker) bands(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	// A READ, so a browser session cookie is accepted alongside the signed key - the
	// website has no signing key, which is why this list was empty for every owner.
	// Revoke/move (bandsByID) stay on requireOwner: a cookie can read, never mutate.
	owner, ok := b.requireOwnerRead(r)
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
	// The path is "/bands/{id}" or "/bands/{id}/{action}". Splitting on the FIRST slash is
	// safe because a band id is "band_<hex>" and can never contain one; anything past the
	// second segment is not a route we serve.
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/bands/"), "/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || id == "resolve" || strings.Contains(action, "/") {
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	// PROVE POSSESSION OF THE KEY, do not merely accept its name.
	//
	// requireOwner resolves an owner from the X-Roger-Pubkey header and verifies NO
	// signature, so on its own it treats a PUBLIC key as a bearer credential: anyone who
	// learns an owner's pubkey could burn their band's code or repoint it at a model they
	// control. Both mutations here are destructive - a revoke can never be undone, and a
	// move silently redirects everyone already tuned in.
	//
	// identityOf verifies the Ed25519 signature over method + path + body, and rejects a
	// request that offers a signature which does not verify. The clients have been signing
	// all along (internal/client/rc.go RevokeBand/MoveBand both use signedDo), so this
	// closes a gap between what the design documents and what the code enforced rather than
	// changing any caller's contract.
	//
	// The body is read HERE because the signature covers it, and moveBand is handed the
	// same bytes - re-reading r.Body after this point yields nothing.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if _, authed, iok := b.identityOf(r, body); !iok || !authed {
		jsonErr(w, http.StatusForbidden, "managing private bands requires a signed request - run `roger login`")
		return
	}
	owner, ok := b.requireOwner(r)
	if !ok {
		jsonErr(w, http.StatusForbidden, "managing private bands requires a GitHub-linked owner - run `roger login`")
		return
	}
	// The action segment carries the two operations that are neither a plain revoke nor a
	// patch. They get their own paths rather than a flag on PATCH because a rotate RETURNS
	// A SECRET: folding it into the patch would make the response shape depend on the
	// request body, and a caller that logs a band view would eventually log a code.
	switch action {
	case "":
	case "rotate":
		b.rotateBand(w, r, owner, id)
		return
	case "forget":
		b.forgetBand(w, r, owner, id)
		return
	default:
		jsonErr(w, http.StatusNotFound, "no such band action")
		return
	}
	if r.Method == http.MethodPatch {
		b.moveBand(w, r, owner, id, body)
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE, PATCH")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// REVOKE DELETES THE ROW (founder 2026-08-21: "why is there even a dead one, shouldn't
	// it be deleted or something").
	//
	// It used to be a tombstone, justified as "the row is kept precisely so the burnt code
	// stays burnt". That justification does not survive reading bandOffers: it returns the
	// SAME uniform negative for `!found` as for `!band.Active(now)`, so a deleted row and a
	// revoked row are byte-identical to anyone tuning a code. The tombstone bought exactly
	// nothing at the only place it could have mattered - and cost the owner a row in their
	// band list that nothing in the product could ever remove.
	//
	// Nothing else depended on it either: CountActiveBands already skipped revoked rows, and
	// the re-register path in tunnel.go only ever reuses an UNREVOKED band, so a node whose
	// tombstone is gone mints a fresh band exactly as it did when the tombstone was there.
	//
	// The one thing a tombstone could genuinely have supported - answering "what happened to
	// my band?" during support - is a LOG's job, so it is logged here. A log entry is
	// durable, timestamped and out of the owner's way; a row they cannot delete is not.
	//
	// Bands revoked BEFORE this change are still rows in the wild, so POST /bands/{id}/forget
	// stays as the way to clear them.
	revoked, err := b.db.SetBandRevoked(id, owner.Pubkey, true)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !revoked {
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	log.Printf("band %s revoked by owner %s", id, owner.Login)
	// The delete is best-effort AFTER the revoke commits, and that order is deliberate: the
	// revoke is what makes the code stop working, so it must never be at risk of being
	// rolled back by a failed cleanup. A row that survives the delete is a stale list entry
	// the owner can clear with `f` - not a live code.
	if _, err := b.db.ForgetBand(id, owner.Pubkey); err != nil {
		log.Printf("band %s revoked but its row could not be removed: %v", id, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true})
}

// moveBandReq is the PATCH /bands/{id} body. Pointer fields distinguish an omitted member
// from an explicit empty label (which clears it). node_id and label are committed together:
// an occupied destination cannot leave a new label behind on the old binding.
type moveBandReq struct {
	NodeID *string `json:"node_id"`
	Label  *string `json:"label"`
}

// moveBand handles PATCH /bands/{id}: repointing a band, assigning its human label, or
// doing both atomically.
//
// This is what makes a band a DURABLE IDENTITY rather than a side effect of one model.
// Because a node id is "<station>-<model>", a band was previously welded to the model it
// was minted for: the only way to serve a different model privately was to revoke and
// re-mint, which rotates the secret and cuts off everyone already tuned in. A move keeps
// the code, the hash and the display, so nobody has to be re-told anything.
//
// It deliberately does NOT require the destination node to exist or be on air. The band
// binds when that model next registers privately: tunnel.go's register path reuses an
// existing unrevoked band owned by the same owner instead of minting, so the move is
// picked up with no new code and no quota consumed.
//
// A band belonging to another owner answers exactly like one that does not exist, so this
// endpoint can never be used to enumerate other people's band ids.
func (b *broker) moveBand(w http.ResponseWriter, r *http.Request, owner store.Owner, id string, body []byte) {
	var req moveBandReq
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NodeID == nil && req.Label == nil {
		jsonErr(w, http.StatusBadRequest, "provide node_id, label, or both")
		return
	}
	patch := store.BandPatch{}
	if req.NodeID != nil {
		// An empty node_id would unbind the band from every node, leaving a live code that
		// resolves to nothing and no way to re-bind it. Refuse rather than strand it.
		nodeID := strings.TrimSpace(*req.NodeID)
		if nodeID == "" {
			jsonErr(w, http.StatusBadRequest, "node_id is required - name the model's node to move this band to")
			return
		}
		patch.NodeID = &nodeID
	}
	if req.Label != nil {
		label := strings.TrimSpace(*req.Label)
		if utf8.RuneCountInString(label) > 64 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
			jsonErr(w, http.StatusBadRequest, "label must be 64 characters or fewer and contain no control characters")
			return
		}
		patch.Label = &label
	}
	updated, ok, err := b.db.UpdateBand(id, owner.Pubkey, patch)
	switch {
	case errors.Is(err, store.ErrBandNodeOccupied):
		jsonErr(w, http.StatusConflict, "that model already carries its own private band - move or revoke that one first")
		return
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	case !ok:
		// Unknown, revoked, or another owner's band - all indistinguishable on purpose.
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	log.Printf("band %s updated by owner %s", id, owner.Login)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "moved": req.NodeID != nil, "node_id": updated.NodeID, "label": updated.Label,
	})
}

// bandView is the public (secret-free) JSON shape of a band. NEVER includes the code
// hash or the secret code. CodeDisplay is the MASKED cosmetic display ("147.520 MHz ·
// ••••-••••") - non-recoverable, so it cannot reconstruct the band; the secret full code
// is shown ONLY once at mint and is not retrievable here (lost => revoke + re-mint).
// rotateBand handles POST /bands/{id}/rotate: a fresh secret for an EXISTING band.
//
// WHY THIS EXISTS. The only way to change a band's code was revoke + go private again,
// which mints a DIFFERENT band - new id, new dial, and a quota slot re-taken after the old
// one was surrendered. As an answer to "my code leaked" that is two steps with a window in
// between where the operator owns no band, and if the second step fails they have destroyed
// their band and gained nothing. It also throws away the band's identity: the dial and the
// label are how an owner recognises their own band, and rotating a key should not rename
// the thing it belongs to.
//
// Keeping the cosmetic frequency is safe by construction, not by convenience: the frequency
// is never folded into the key (protocol/band.go) and CanonicalBandTail discards it before
// hashing, so a rotation that reuses it still replaces 100% of the key material.
//
// WHAT IT COSTS THE OPERATOR, stated plainly because the response has to carry it: the old
// code stops resolving immediately, so everyone already tuned in IS cut off. That is the
// point of rotating, and it is the one way this differs from a move.
//
// The new code is returned ONCE, exactly like a mint. It is never persisted - only
// sha256(tail) and the masked display are - so it can never be shown again.
func (b *broker) rotateBand(w http.ResponseWriter, r *http.Request, owner store.Owner, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// The existing band is read first so the new code can keep ITS cosmetic frequency.
	// Scoped to this owner: a band belonging to someone else answers exactly like one that
	// does not exist, so this can never be used to enumerate other people's band ids.
	list, err := b.db.BandsByOwner(owner.Pubkey)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	var cur store.Band
	found := false
	for _, bd := range list {
		if bd.ID == id {
			cur, found = bd, true
			break
		}
	}
	if !found {
		jsonErr(w, http.StatusNotFound, "no such band")
		return
	}
	if cur.Revoked {
		// Revoke is final and gave the quota slot back. Rotating would resurrect a burnt
		// band under a working code, so name the remedy that pays the quota instead.
		jsonErr(w, http.StatusConflict, "that band is revoked - its code is burnt. Go private again to mint a new band")
		return
	}
	code, display, tail := protocol.RotateBandCode(cur.CodeDisplay)
	updated, ok, err := b.db.RotateBandCode(id, owner.Pubkey, protocol.BandCodeHash(tail), display)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !ok {
		// The row changed under us (a concurrent revoke is the realistic case). Report it
		// as the conflict it is rather than a 500.
		jsonErr(w, http.StatusConflict, "that band changed while rotating - re-read your bands and try again")
		return
	}
	log.Printf("band %s code rotated by owner %s (node %s)", id, owner.Login, updated.NodeID)
	view := bandView(updated, time.Now())
	// The one-time secret. Named "code" to match the mint response, so a caller that
	// already knows to show-once-and-forget a mint needs no new rule.
	view["code"] = code
	view["rotated"] = true
	writeJSON(w, http.StatusOK, view)
}

// forgetBand handles POST /bands/{id}/forget: delete a REVOKED band row for good.
//
// Revoking left the row behind forever with nothing able to remove it, so an operator who
// rotated or re-minted a few times accumulated a permanent list of dead entries they could
// neither tune nor clear - burying the one live band among them. History nobody can delete
// is clutter, not an audit trail.
//
// A LIVE band is refused: deleting it would drop its code out of the resolve index while
// every consumer holding that code carries on believing it works, and would free a quota
// slot with no confirm anywhere. Revoke first, then forget - the destructive half keeps its
// own gate.
func (b *broker) forgetBand(w http.ResponseWriter, r *http.Request, owner store.Owner, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ok, err := b.db.ForgetBand(id, owner.Pubkey)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !ok {
		// One message for "no such band" and "that band is still live", because the
		// remedy differs and the caller cannot tell which it hit otherwise. Naming both is
		// safe: an id that is not yours already answers as not-found above.
		jsonErr(w, http.StatusConflict, "only a revoked band can be forgotten - revoke it first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "forgotten": true})
}

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
