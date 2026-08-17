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
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/audit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
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
func (b *broker) selectForAudit(towerID, stationID, attemptID, requestDigest, responseDigest string, usageIn, usageOut, wireIn, wireOut int64) {
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
		UsageIn: usageIn, UsageOut: usageOut, WireIn: wireIn, WireOut: wireOut,
		Deadline: time.Now().Add(auditDeadline),
	}); err != nil {
		log.Printf("audit: could not select %s: %v", attemptID, err)
	}
}

// forceAudit marks an attempt wanted regardless of the sample - used when settlement already
// found something worth a closer look, like a disputed digest. The Station keeps everything
// recent, so a forced audit lands on a transcript it should still hold.
func (b *broker) forceAudit(towerID, stationID, attemptID, requestDigest, responseDigest string, usageIn, usageOut, wireIn, wireOut int64) {
	ts := b.tower
	if ts == nil || ts.auditWanted == nil {
		return
	}
	if err := ts.auditWanted.Want(audit.Wanted{
		TowerID: towerID, AttemptID: attemptID, StationID: stationID,
		RequestDigest: requestDigest, ResponseDigest: responseDigest,
		UsageIn: usageIn, UsageOut: usageOut, WireIn: wireIn, WireOut: wireOut,
		Deadline: time.Now().Add(auditDeadline),
	}); err != nil {
		log.Printf("audit: could not force-select %s: %v", attemptID, err)
	}
}

// auditLenientStation reports whether a "cannot produce" from this Station should be a SOFT
// miss rather than the quarantine-grade finding it is for everybody else.
//
// The leniency exists for one reason and RETIRES ITSELF once that reason is gone. A hub node
// could not answer audits at all before the transcript plane shipped - the classic courier
// collects from dialable Station endpoints, and a polling node has none - so holding one to
// the standard would have quarantined honest towers for a feature that did not exist. But a
// blanket exemption is a permanent hole, and removing it on a flag day would punish whoever
// upgrades last.
//
// So the test is BEHAVIOUR, not a version or a claim: a hub node that has ever ANSWERED an
// audit has proven it retains transcripts and will produce them, and from that moment its
// misses mean what everyone else's mean. A node that has never answered stays lenient - and
// stays visible, because answering nothing is itself the pattern the audit exists to find.
func (b *broker) auditLenientStation(stationID string) bool {
	ts := b.tower
	if ts == nil || ts.stations == nil {
		return false
	}
	at, found, err := ts.stations.Station(stationID)
	if err != nil || !found || at.HubToken == "" {
		return false // classic attachment: always held to the standard
	}
	return at.AuditProvenAt.IsZero()
}

// markAuditProven records that this Station produced a transcript - the fact that retires its
// leniency. Best effort: failing to record it only keeps a node lenient a while longer, which
// is the safe direction.
func (b *broker) markAuditProven(stationID string) {
	ts := b.tower
	if ts == nil || ts.stations == nil || stationID == "" {
		return
	}
	if first, err := ts.stations.MarkAuditProven(stationID, time.Now()); err != nil {
		log.Printf("audit: could not record that %s answered an audit: %v", stationID, err)
	} else if first {
		log.Printf("audit: station %s answered its first audit - it is now held to the "+
			"same standard as every other station", stationID)
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
		TowerID   string `json:"tower_id"`
		AttemptID string `json:"attempt_id"`
		Available bool   `json:"available"`
		// SealedBundle is the HUB path's shape: the whole payload sealed to Core's envelope
		// key (AAD = attempt id), so the relaying tower reads none of it.
		SealedBundle string `json:"sealed_bundle"`
		Transcript   string `json:"transcript"` // base64 of the Station-signed object (classic path)
		Request      string `json:"request"`    // base64 plaintext (classic path)
		Response     string `json:"response"`   // base64 plaintext (classic path)
	}
	if json.Unmarshal(body, &req) != nil || req.TowerID == "" || req.AttemptID == "" {
		jsonErr(w, http.StatusBadRequest, "a transcript names its Tower and attempt")
		return
	}
	if req.SealedBundle != "" {
		// Open the sealed bundle into the classic fields; everything below is path-agnostic.
		sealedRaw, derr := base64.StdEncoding.DecodeString(req.SealedBundle)
		if derr != nil {
			jsonErr(w, http.StatusBadRequest, "the sealed bundle is not valid base64")
			return
		}
		parsed, perr := envelope.Parse(sealedRaw)
		if perr != nil {
			jsonErr(w, http.StatusBadRequest, "the sealed bundle is not a sealed envelope")
			return
		}
		bundle, oerr := envelope.OpenWith(ts.envelopeKey, parsed, req.AttemptID)
		if oerr != nil {
			// Sealed to the wrong key or for another attempt: refused WITHOUT resolving the
			// want, exactly like a transcript that fails verification.
			jsonErr(w, http.StatusBadRequest, "the sealed bundle does not open for this attempt")
			return
		}
		var inner struct {
			Transcript string `json:"transcript"`
			Request    string `json:"request"`
			Response   string `json:"response"`
		}
		if json.Unmarshal(bundle, &inner) != nil {
			jsonErr(w, http.StatusBadRequest, "the sealed bundle is not a transcript bundle")
			return
		}
		req.Transcript, req.Request, req.Response = inner.Transcript, inner.Request, inner.Response
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
		// The Station did not keep it. For a SAMPLED attempt that is the spec's quarantine
		// trigger: the deterministic sample is the retention CONTRACT, so "cannot produce"
		// there is a Station refusing to show work it promised to hold. An OFF-SAMPLE want -
		// an adaptive or forced selection - carries no such promise: the Station's retention
		// samples by the same deterministic rule, so an honest, busy Station may simply not
		// have it (a review found one-strike-quarantining that punished exactly the honest).
		// Off-sample misses are logged as a soft signal, not recorded as a mismatch.
		if auditSampled(req.AttemptID) && !b.auditLenientStation(wanted.StationID) {
			b.recordOutcome(req.TowerID, req.AttemptID, reputation.AuditMismatch)
			b.evaluateTower(req.TowerID)
		} else {
			log.Printf("audit: attempt %s on tower %s not retained (off-sample, or a hub node that has never answered one) - soft miss, no finding", req.AttemptID, req.TowerID)
		}
		_ = ts.auditWanted.Resolve(req.AttemptID)
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
		} else if int64(len(reqBytes)) != wanted.UsageIn || int64(len(respBytes)) != wanted.UsageOut {
			// USAGE MUST EQUAL THE BYTES THE STATION SIGNED FOR. This is the post-hoc backstop for
			// the one figure Core cannot verify at settlement: on an unacknowledged attempt the
			// billable usage is the Station's own number, bounded only by the grant ceiling. Here
			// Core has the actual bytes (they hash to the signed digest), so it re-derives the true
			// length and holds the receipt's claim to it. A Station that billed more (or fewer)
			// bytes than it signed for has misreported usage - attributable, because it signed both
			// the receipt's usage and the transcript's bytes - and is treated as an audit mismatch.
			matches = false
			result.Reason = fmt.Sprintf("usage misreported: receipt claimed in=%d out=%d, transcript bytes in=%d out=%d",
				wanted.UsageIn, wanted.UsageOut, len(reqBytes), len(respBytes))
		} else {
			// WIRE ARBITRATION (P8). The transcript just PROVED the true plaintext lengths
			// (they hash to the digests both ends signed). Sealed bytes are always at least
			// the plaintext they carry, so a Tower-attested wire count BELOW the proven
			// length is a physical impossibility: the Tower lied low - the exact move that
			// would have underpaid this node had the wire moved money. It never does (the
			// count is evidence only), and here the lie becomes attributable TO THE TOWER:
			// the Station's transcript passes, and the Tower eats the disputed outcome its
			// false attestation caused.
			if (wanted.WireIn > 0 && wanted.WireIn < int64(len(reqBytes))) ||
				(wanted.WireOut > 0 && wanted.WireOut < int64(len(respBytes))) {
				log.Printf("audit: TOWER %s attested an impossible wire count for %s (wire %d/%d < proven plaintext %d/%d) - the tower, not the station, is the liar here",
					req.TowerID, req.AttemptID, wanted.WireIn, wanted.WireOut, len(reqBytes), len(respBytes))
				b.recordOutcome(req.TowerID, req.AttemptID+"#wire", reputation.CanaryFail)
				b.evaluateTower(req.TowerID)
			}
		}
	}

	// PROVEN: this Station produced a transcript that verified against the receipt's digests
	// under its own attachment key. Whatever the content verdict below, the CAPABILITY is
	// demonstrated, and its leniency ends here.
	b.markAuditProven(wanted.StationID)
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
		// work - the spec's quarantine trigger. An off-sample (adaptive/forced) want carries
		// no retention promise, so its silence is a soft signal, not a finding.
		if !auditSampled(o.AttemptID) || b.auditLenientStation(o.StationID) {
			log.Printf("audit: attempt %s on tower %s never produced (off-sample or hub node) - soft miss, no finding", o.AttemptID, o.TowerID)
			continue
		}
		log.Printf("audit: attempt %s on tower %s was never produced", o.AttemptID, o.TowerID)
		b.recordOutcome(o.TowerID, o.AttemptID, reputation.AuditMismatch)
		b.evaluateTower(o.TowerID)
	}
}

// --- the adaptive layer -----------------------------------------------------
//
// Contract: features/tower/edge_dispatch.feature ("The audit rate adapts to the evidence").
//
// The deterministic baseline (auditSampleN) keeps Core's wants inside the Station's long-term
// transcript retention. The adaptive layer selects RECENT attempts - which a Station holds
// regardless of the sample (the forceAudit precedent) - with a probability that starts high
// for a freshly attached Station, ramps on a Tower's recent disputes/uncorroborated rate, and
// decays toward zero as corroborated history accumulates. Like the baseline it is best
// effort and downstream of settlement: it can under-sample, never gate money.

const (
	// adaptiveNewStationWindow is how long a fresh attachment is treated as unproven.
	adaptiveNewStationWindow = 24 * time.Hour
	// adaptiveNewStationP is the extra selection probability during that window: every other
	// settlement of a brand-new Station gets looked at.
	adaptiveNewStationP = 0.5
	// adaptiveReputationWindow is the recent-history window the anomaly rate is read from.
	adaptiveReputationWindow = 24 * time.Hour
	// adaptiveAnomalyGain scales the Tower's recent STRONG-evidence rate - disputes, audit
	// mismatches, canary failures - into extra selection probability: a fully-anomalous
	// Tower is audited on every settlement.
	adaptiveAnomalyGain = 1.0
	// adaptiveUncorrGain scales the tower's uncorroborated EXCESS over the fleet baseline.
	// Uncorroborated is the ORDINARY outcome (third-party clients never ack; acks race
	// receipts), so the absolute rate must not drive the audit rate - a review found gain 1.0
	// on it converged honest fleets on audit-everything, a privacy and load regression. What
	// is anomalous is being MORE uncorroborated than everyone else.
	adaptiveUncorrGain = 0.25
)

// adaptiveAuditP computes the elevated selection probability for one settlement. attachedAt
// is the Station's attachment time, passed in because the settle handler already holds the
// record (no second store round-trip per settlement).
func (b *broker) adaptiveAuditP(towerID string, attachedAt, now time.Time) float64 {
	ts := b.tower
	if ts == nil {
		return 0
	}
	p := 0.0
	if !attachedAt.IsZero() && now.Sub(attachedAt) < adaptiveNewStationWindow {
		p += adaptiveNewStationP
	}
	if ts.outcomes != nil {
		since := now.Add(-adaptiveReputationWindow)
		if tally, err := ts.outcomes.Tally(towerID, since); err == nil && tally.Total > 0 {
			// STRONG evidence ramps directly: disputes, audit mismatches, canary failures.
			// (A review found the earlier numerator EXCLUDED mismatches and canary fails, so
			// the strongest evidence lowered the rate by growing only the denominator.)
			strong := float64(tally.Disputed+tally.AuditMismatch+tally.CanaryFail) / float64(tally.Total)
			p += adaptiveAnomalyGain * strong
			// Uncorroborated ramps only on the EXCESS over the fleet's own rate, over settled
			// outcomes - the same relative discipline evaluateTower applies.
			settledHere := tally.Corroborated + tally.Uncorroborated + tally.Disputed
			if fleet, ferr := ts.outcomes.FleetTally(since); ferr == nil && settledHere > 0 {
				settledFleet := fleet.Corroborated + fleet.Uncorroborated + fleet.Disputed
				towerRate := float64(tally.Uncorroborated) / float64(settledHere)
				fleetRate := 0.0
				if settledFleet > 0 {
					fleetRate = float64(fleet.Uncorroborated) / float64(settledFleet)
				}
				if excess := towerRate - fleetRate; excess > 0 {
					p += adaptiveUncorrGain * excess
				}
			}
		}
	}
	if p > 1 {
		p = 1
	}
	return p
}

// adaptiveAudit rolls the elevated selection for a just-settled attempt and, on a hit, marks
// it wanted exactly as the baseline does. The coin is crypto/rand (a tower must not be able
// to predict it the way it can predict the deterministic sample); a failed read of
// randomness simply skips - under-sampling, never blocking.
func (b *broker) adaptiveAudit(towerID, stationID, attemptID, requestDigest, responseDigest string, usageIn, usageOut, wireIn, wireOut int64, attachedAt time.Time) {
	p := b.adaptiveAuditP(towerID, attachedAt, time.Now())
	if p <= 0 {
		return
	}
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return
	}
	roll := float64(binary.BigEndian.Uint64(buf[:])>>11) / float64(1<<53)
	if roll >= p {
		return
	}
	b.forceAudit(towerID, stationID, attemptID, requestDigest, responseDigest, usageIn, usageOut, wireIn, wireOut)
}
