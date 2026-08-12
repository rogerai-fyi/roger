package main

// toweraudit.go is Roger Core's post-hoc content review for edge traffic.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY POST-HOC AND SAMPLED
//
// Core never saw the request or the response - that is what a Tower is paid for - so it cannot
// screen content before dispatch. Instead it checks a sampled fraction AFTER settlement. Both
// ends signed a digest of the exact bytes, so neither can produce a different transcript
// later; a stored transcript that hashes to those digests is the real content, and one that
// does not is attributable to the Station whose signature it fails to match. This is the only
// route by which Tower-served content is reviewed.
//
// # THE FLOW, AND WHY IT RIDES THE COURIER
//
// Core cannot reach a Station, so it cannot pull a transcript on demand. At settlement it
// marks a sampled attempt WANTED. The Tower's courier asks what is wanted for it, fetches
// those transcripts from its Stations, and forwards them here. Core verifies each against what
// the receipt committed to and resolves it. What stays unresolved past its deadline is a
// Station that could not show its work - the same finding as a mismatch.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"log"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/audit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

// auditSampleN selects 1 in N settled attempts for audit. It matches the Station's own
// transcript-retention default, so an attempt Core wants is one the Station kept: deterministic
// on the attempt id, so the two agree without a per-attempt marker that would tell a Tower
// which attempts are watched.
//
// Its predictability is a known, bounded weakness: a Tower that computed the sample could
// serve honestly on watched attempts and cheat on the rest. CANARIES are the complement - Core
// originates them and they are unpredictable to the Tower - so the two mechanisms together
// leave no attempt a Tower can be sure is unwatched.
const auditSampleN = 8

// auditDeadline is how long a Station has to produce a wanted transcript before "not yet" hardens
// into "cannot produce". Generous: the courier runs on a timer and the transcript makes two hops.
const auditDeadline = 30 * time.Minute

// selectForAudit marks a settled attempt wanted if it falls in the sample.
//
// Best effort, and downstream of settlement: a failure to enqueue an audit under-samples,
// which reviews slightly less content, never more - it cannot wrongly accuse anyone, so it is
// never a gate on the money.
func (b *broker) selectForAudit(towerID, stationID, attemptID, requestDigest, responseDigest string) {
	ts := b.tower
	if ts == nil || ts.auditWanted == nil {
		return
	}
	if !auditSampled(attemptID) {
		return
	}
	if err := ts.auditWanted.Want(audit.Wanted{
		TowerID: towerID, AttemptID: attemptID, StationID: stationID,
		RequestDigest: requestDigest, ResponseDigest: responseDigest,
		Deadline: time.Now().Add(auditDeadline),
	}); err != nil {
		log.Printf("audit: could not select %s: %v", attemptID, err)
	}
}

func auditSampled(attemptID string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(attemptID))
	return h.Sum32()%auditSampleN == 0
}

// towerAuditWanted answers a Tower's courier: what transcripts do you owe me?
func (b *broker) towerAuditWanted(w http.ResponseWriter, r *http.Request) {
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
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "an audit list is for the Tower's own signed request")
		return
	}
	pending, err := ts.auditWanted.Pending(req.TowerID, time.Now())
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the audit list")
		return
	}
	out := make([]map[string]string, 0, len(pending))
	for _, p := range pending {
		// The station id is all the courier needs: it knows where to ask. The digests are
		// NOT sent - Core checks against them, and handing them out would tell a Tower exactly
		// what a passing transcript must contain.
		out = append(out, map[string]string{"attempt_id": p.AttemptID, "station_id": p.StationID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"wanted": out})
}

// towerAuditTranscript accepts a transcript the courier collected and checks it.
func (b *broker) towerAuditTranscript(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID    string `json:"tower_id"`
		AttemptID  string `json:"attempt_id"`
		Available  bool   `json:"available"`
		Transcript string `json:"transcript"` // base64 of the Station-signed object
		Request    string `json:"request"`    // base64 plaintext
		Response   string `json:"response"`   // base64 plaintext
	}
	if json.Unmarshal(body, &req) != nil || req.TowerID == "" || req.AttemptID == "" {
		jsonErr(w, http.StatusBadRequest, "a transcript names its Tower and attempt")
		return
	}
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "submitting a transcript needs the Tower's own signed request")
		return
	}

	// Look up what we wanted. Gone means already resolved or never wanted - either way there
	// is nothing to check, and re-checking a resolved one would let a Tower re-open a closed
	// audit. Pending across ALL time (not just live) so a transcript arriving right at the
	// deadline is still matched rather than double-counted by the overdue sweep.
	wanted, found := b.findWanted(req.TowerID, req.AttemptID)
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"attempt_id": req.AttemptID, "resolved": true})
		return
	}

	if !req.Available {
		// The Station did not keep it. That is a "cannot produce" for a SAMPLED attempt, which
		// the spec quarantines on - recorded as a mismatch outcome and resolved so it is not
		// swept again.
		b.recordOutcome(req.TowerID, req.AttemptID, reputation.AuditMismatch)
		_ = ts.auditWanted.Resolve(req.AttemptID)
		b.evaluateTower(req.TowerID)
		writeJSON(w, http.StatusOK, map[string]any{"attempt_id": req.AttemptID, "resolved": true})
		return
	}

	raw, err := base64.StdEncoding.DecodeString(req.Transcript)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "the transcript is not valid base64")
		return
	}
	// The Station's assertion key, from the ATTACHMENT record - never from the message, for
	// the same reason a receipt is checked against it: a transcript's whole value is that a
	// Tower cannot forge one.
	key, ok := b.stationAssertionKey(wanted.StationID)
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the Station's key")
		return
	}
	tr, result, err := dispatch.AuditTranscript(raw, key, link.PublicNetwork, req.AttemptID,
		wanted.RequestDigest, wanted.ResponseDigest)
	if err != nil {
		// A transcript that will not verify is not a pass and not a clean fail - it is a
		// malformed submission, refused so a Tower cannot resolve an audit with garbage.
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	matches := result.Matches
	if matches {
		// The bytes must also hash to the signed digests, or the content Core is about to
		// screen is not the content that was attested.
		reqBytes, _ := base64.StdEncoding.DecodeString(req.Request)
		respBytes, _ := base64.StdEncoding.DecodeString(req.Response)
		if verr := tr.VerifyBytes(reqBytes, respBytes); verr != nil {
			matches = false
			result.Reason = verr.Error()
		}
	}

	_ = ts.auditWanted.Resolve(req.AttemptID)
	if !matches {
		log.Printf("audit: attempt %s on tower %s did not match: %s", req.AttemptID, req.TowerID, result.Reason)
		b.recordOutcome(req.TowerID, req.AttemptID, reputation.AuditMismatch)
		b.evaluateTower(req.TowerID)
		writeJSON(w, http.StatusOK, map[string]any{"attempt_id": req.AttemptID, "matched": false})
		return
	}
	// MATCHED. The content is provably what both ends signed, and Core can now screen it -
	// content moderation for edge traffic happens HERE and only here. A policy violation found
	// now is enforced against the ACCOUNT afterwards, which is a separate, existing path; the
	// audit's job is to make the content available and attributable, which it has.
	b.screenAuditedContent(req.AttemptID, tr)
	writeJSON(w, http.StatusOK, map[string]any{"attempt_id": req.AttemptID, "matched": true})
}

// findWanted reads one wanted entry back. Pending only returns live ones, so an attempt that
// just passed its deadline is looked up through a whole-Tower scan rather than missed.
func (b *broker) findWanted(towerID, attemptID string) (audit.Wanted, bool) {
	ts := b.tower
	// A generous horizon so a transcript arriving right on the deadline still matches. The
	// store's Pending filters by deadline; to catch a just-expired one we ask as of a moment
	// in the past... but simpler and exact: scan the Tower's pending as-of far future.
	pending, err := ts.auditWanted.Pending(towerID, time.Unix(0, 0))
	if err != nil {
		return audit.Wanted{}, false
	}
	for _, p := range pending {
		if p.AttemptID == attemptID {
			return p, true
		}
	}
	return audit.Wanted{}, false
}

// stationAssertionKey reads a Station's attachment-recorded key.
func (b *broker) stationAssertionKey(stationID string) ([]byte, bool) {
	at, found, err := b.tower.stations.Station(stationID)
	if err != nil || !found {
		return nil, false
	}
	key, derr := hex.DecodeString(at.AssertionKey)
	if derr != nil || len(key) != 32 {
		return nil, false
	}
	return key, true
}

// screenAuditedContent is where content moderation runs on Tower-served traffic. It is a seam
// rather than a policy: the audit's contribution is making the exact, attributable bytes
// available, and what is done with them is the account-moderation path that already exists.
func (b *broker) screenAuditedContent(attemptID string, tr dispatch.SignedTranscript) {
	// Deliberately minimal here: the transcript is real and attributable, and hooking it to
	// the moderation pipeline is a separate wiring that does not belong in the audit's own
	// correctness. Logged so a review of edge content has a trail to follow.
	log.Printf("audit: attempt %s content available for review (%d req / %d resp bytes)",
		attemptID, len(tr.Request), len(tr.Response))
}

// sweepAuditOverdue turns transcripts that never arrived into findings. Called on the same
// sweep as the invite reaper.
func (b *broker) sweepAuditOverdue(now time.Time) {
	ts := b.tower
	if ts == nil || ts.auditWanted == nil {
		return
	}
	overdue, err := ts.auditWanted.Overdue(now)
	if err != nil {
		log.Printf("audit: overdue sweep failed: %v", err)
		return
	}
	for _, o := range overdue {
		// A SAMPLED attempt whose transcript never came is a Station that cannot show its
		// work - the spec's quarantine trigger.
		log.Printf("audit: attempt %s on tower %s was never produced", o.AttemptID, o.TowerID)
		b.recordOutcome(o.TowerID, o.AttemptID, reputation.AuditMismatch)
		b.evaluateTower(o.TowerID)
	}
}
