package main

// towerfaultattribution_test.go is the spec for WHOSE FAULT a finding is.
//
// Contract: features/tower/edge_dispatch.feature, features/tower/inventory_and_routing.feature.
//
// The founder's ruling: harm lands on the party responsible, and an honest operator does not
// lose standing for somebody else's failure. Suspending a Tower takes every node behind it off
// the fabric, so the evidence that triggers one has to be evidence about the TOWER - and the
// ledger it is read from could not previously say which Station an outcome even concerned.
//
// Everything here drives the real handlers and reads the real ledger. The assertions are
// deliberately paired: for every finding this moves OFF a Tower there is a neighbouring one it
// leaves ON, because a change that only ever exonerates is not an attribution rule, it is an
// amnesty - and the party it would exonerate is the one that gets to choose which shape a
// failure arrives in.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

// towerWindow reads back what the durable ledger holds for one Tower, split by Station.
func towerWindow(t *testing.T, b *broker, towerID string) map[string]reputation.Tally {
	t.Helper()
	byStation, err := b.tower.outcomes.TallyByStation(towerID, time.Now().Add(-reputationWindow))
	require.NoError(t, err)
	return byStation
}

// A transcript that VERIFIES under the Station's own attachment key and then contradicts the
// digests that same key already signed into the receipt is the Station having signed two
// incompatible accounts of one attempt. The Tower holds neither signature and cannot make one,
// so nothing it did or failed to do produces this - and a single one of them used to suspend it.
//
// This test replaces TestAMismatchedTranscriptFailsAndSuspends, whose own comment said the
// mismatch "is attributed to the Station and suspends the Tower". Those are opposite sentences:
// the spec line it was quoting ("a transcript that does not match is attributed to the Station
// and not to the consumer") was implemented by suspending a third party.
func TestAMismatchedTranscriptIsTheStationsFaultAndNotItsTowers(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// Core wanted a transcript for these digests; the Station signs a DIFFERENT response.
	wantAudit(t, b, tw.id, "st-1", "att-1", []byte("the prompt"), []byte("the real answer"))
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-1",
		[]byte("the prompt"), []byte("a substituted answer"))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["matched"], "the finding itself must still be made")

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State,
		"one Station's contradicted transcript suspended its Tower, taking every honest node behind it off the fabric")

	// AND THE EVIDENCE NAMES THE MACHINE. Without the station dimension this row was
	// indistinguishable from a finding about the Tower itself, which is how it came to be one.
	byStation := towerWindow(t, b, tw.id)
	require.Equal(t, 1, byStation["st-1"].StationFault,
		"the finding is not recorded against the Station that produced it: %+v", byStation)
	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-reputationWindow))
	require.NoError(t, err)
	require.Zero(t, tally.AuditMismatch,
		"the Tower is still carrying an audit mismatch it had no part in")
}

// AND THE TOWER CANNOT ESCAPE THROUGH THE SAME DOOR. The plaintext bytes ride BESIDE the
// transcript, unsigned, in fields of the Tower's own submission - so a Tower that corrupts them
// makes an honest Station's transcript fail to verify against them. If a mismatch were the
// Station's by default, that would be a free way to launder any tampering into somebody else's
// record. The transcript's own digests still agree with the receipt here, which is exactly what
// tells the two cases apart.
func TestCorruptedPlaintextBesideAGoodTranscriptStaysTheTowers(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("the prompt"), []byte("the real answer")
	wantAudit(t, b, tw.id, "st-1", "att-2", req, resp)
	// The Station signs the TRUE bytes - its transcript matches the receipt's digests exactly.
	obj, _, _ := signedTranscript(t, stationPriv, "att-2", req, resp)
	// The Tower forwards it with a response body of its own choosing.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-2", "available": true,
		"transcript": obj,
		"request":    base64.StdEncoding.EncodeToString(req),
		"response":   base64.StdEncoding.EncodeToString([]byte("bytes the tower made up")),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["matched"])

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State,
		"a Tower substituted the plaintext beside a Station's honest transcript and paid nothing for it")
	byStation := towerWindow(t, b, tw.id)
	require.Zero(t, byStation["st-1"].StationFault,
		"the Station whose transcript was correct was blamed for the Tower's substitution")
}

// A FORWARDED EXCUSE IS THE TOWER'S. "That Station did not keep it" arrives over the Tower's
// signature with nothing in it Core can check. It is the cheapest lie a Tower can tell, so it
// must not also be its cheapest way out of ever being measured - a Tower that could answer
// `available:false` to every want would resolve every audit and never carry a finding again.
// The Station is still named on the row, because an operator answering for this deserves to be
// told which machine it was about.
func TestAForwardedCannotProduceStaysWithTheTowerAndNamesTheStation(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	require.True(t, auditSampled("att-s0"))
	wantAudit(t, b, tw.id, "st-1", "att-s0", []byte("q"), []byte("a"))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-s0", "available": false,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State,
		"a Tower talked its way out of an audit by claiming its Station had lost the transcript")
	byStation := towerWindow(t, b, tw.id)
	require.Equal(t, 1, byStation["st-1"].AuditMismatch,
		"the row does not say which Station the excuse was about: %+v", byStation)
	require.Zero(t, byStation["st-1"].StationFault)
}

// A SESSION KEY NOTHING CAN BE SEALED TO IS THE STATION'S OWN, and it is the one canary failure
// that is, because it happens before Core has dialed the Tower at all. Nothing proves possession
// of the X25519 half at attach, so thirty-two zero bytes is admitted - and every probe of that
// Station then failed at Core's own SealTo and was recorded as the TOWER not carrying work.
// Attaching is self-serve, so that was a denial primitive: attach a Station that cannot serve to
// somebody else's Tower and spend its reputation forever, for the price of one attach.
func TestAStationKeyNothingCanSealToIsTheStationsFaultNotItsTowers(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "seal-op", "203.0.113.21:8443")

	node := signedInOperator(t, b, "seal-node")
	body, _ := selfAttachBodyFor(t, b, node)
	// A key of the right length and shape that no envelope can be sealed to. The attach door
	// takes it today: the possession proof only has the ASSERTION key vouch for this one.
	body["session_key"] = strings.Repeat("00", 32)
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.attach(t, srv, body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	outcome := b.RunCanary(tw.id)
	require.Equal(t, reputation.StationFault, outcome,
		"a Station whose own advertised key nothing can seal to was recorded as its Tower failing to carry work")

	byStation := towerWindow(t, b, tw.id)
	require.Equal(t, 1, byStation[attached.StationID].StationFault)
	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-reputationWindow))
	require.NoError(t, err)
	require.Zero(t, tally.CanaryFail, "the Tower is still paying for a key it never saw")
	got, _ := b.tower.registry.Get(tw.id)
	require.NotEqual(t, admit.StateSuspended, got.State)
}

// sweepCanaries runs `probes` rounds of the REAL rotation against a Tower, serving a pass for
// every Station except those in `dead`, and records each verdict on both ledgers exactly as
// RunCanary does - so the selection is scored against the evidence it produces, which is the
// only way a feedback loop between the two can show up at all.
func sweepCanaries(t *testing.T, b *broker, towerID string, probes int, dead map[string]bool) reputation.Tally {
	t.Helper()
	for i := 0; i < probes; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok, "probe %d found no Station to canary", i)
		outcome := reputation.CanaryPass
		if dead[row.StationID] {
			outcome = reputation.CanaryFail
		}
		b.recordOutcome(towerID, row.StationID, fmt.Sprintf("canary-%d", i), outcome)
		b.recordEdgeCanary(row.StationID, outcome)
	}
	tally, err := b.tower.outcomes.Tally(towerID, time.Now().Add(-reputationWindow))
	require.NoError(t, err)
	return tally
}

// DEAD STATIONS DO NOT SPEND SOMEBODY ELSE'S TOWER'S REPUTATION.
//
// This is the amplification the founder's ruling is really about, and it is a denial primitive
// rather than a mis-blamed probe. Probe budget is spent per Station and the verdict is read per
// Tower, so a Station that can never answer soaked the rotation forever and its Tower paid for
// every failure - and attaching is SELF-SERVE. Three attachments that do not serve, made by
// anyone with an account, put a Tower carrying two honest machines at a sixty percent canary
// fail rate against a forty percent quarantine bar. The operator suspended has done nothing, can
// see nothing wrong on their own side, and cannot make it stop.
//
// The verdict is read from the same policy production uses, not from a threshold restated here.
func TestDeadStationsDoNotSpendTheirTowersReputation(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 5)
	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 5)
	// Two honest machines and three attachments that never answer.
	dead := map[string]bool{}
	for _, r := range rows[2:] {
		dead[r.StationID] = true
	}

	tally := sweepCanaries(t, b, towerID, 150, dead)
	rate, known := tally.CanaryFailRate()
	require.True(t, known)
	require.Less(t, rate, reputation.DefaultPolicy().MaxCanaryFailRate,
		"three dead attachments drove an honest Tower's canary fail rate to %.2f against a %.2f bar",
		rate, reputation.DefaultPolicy().MaxCanaryFailRate)
	require.Equal(t, reputation.Clean, reputation.DefaultPolicy().Evaluate(tally, reputation.Tally{}),
		"anyone who can attach can take somebody else's Tower off the fabric: %+v", tally)
	// And the dead ones were still probed - a Tower is not allowed to hide a Station by
	// starving it of probes, and a machine that comes back has to be able to prove it.
	byStation := towerWindow(t, b, towerID)
	for id := range dead {
		require.NotZero(t, byStation[id].CanaryFail, "station %s stopped being probed entirely", id)
	}
}

// AND A TOWER THAT CARRIES NOTHING IS STILL TAKEN OFF. The slower clock for a Station that has
// answered nothing must not become a way for a Tower to stop being measured: when everything
// behind it is failing, everything behind it is damped equally, the rotation still spreads, and
// the fail rate is still the whole sweep.
//
// This is the half that stops the exemption being a laundry. A Tower cannot manufacture a
// canary PASS - it would need a Station receipt over bytes Core sealed to an ephemeral key of
// its own - so it cannot buy its way out of this by any means except carrying work.
func TestATowerThatCarriesNothingIsStillQuarantined(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 4)
	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	dead := map[string]bool{}
	for _, r := range rows {
		dead[r.StationID] = true
	}

	tally := sweepCanaries(t, b, towerID, 40, dead)
	require.Equal(t, reputation.Quarantine, reputation.DefaultPolicy().Evaluate(tally, reputation.Tally{}),
		"a Tower failing every probe behind it escaped the verdict: %+v", tally)
	// And it kept probing all four rather than freezing on whichever one it damped first.
	byStation := towerWindow(t, b, towerID)
	require.Len(t, byStation, 4, "the rotation stopped covering the fleet it had demoted: %+v", byStation)
}

// A STATION ON THE SLOW CLOCK IS STILL REACHED. Damping is a longer interval, never an
// exclusion, precisely so the evidence that could clear a Station can always arrive: an
// exclusion would freeze its record at its worst moment and there would be no way back.
func TestADampedStationIsStillProbedAndOnePassRestoresIt(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID := canaryFleet(t, b, srv, 2)
	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	sick := rows[0].StationID

	// Enough failures on the durable ledger to put it on the slow clock.
	for i := 0; i < edgeCanaryFailBar; i++ {
		b.recordOutcome(towerID, sick, fmt.Sprintf("sick-%d", i), reputation.CanaryFail)
		b.recordEdgeCanary(sick, reputation.CanaryFail)
	}
	byStation := towerWindow(t, b, towerID)
	require.True(t, canaryTroubled(byStation[sick]), "the premise is gone: it is not damped")

	reached := false
	for i := 0; i < 400 && !reached; i++ {
		_, row, ok := b.canaryTargetFor(towerID)
		require.True(t, ok)
		reached = row.StationID == sick
		b.recordEdgeCanary(row.StationID, reputation.CanaryPass)
	}
	require.True(t, reached,
		"a damped Station was never probed again, so nothing could ever clear it")

	// And ONE pass takes it off the slow clock - the demotion is a current reading, not a
	// sentence, and an operator whose machine was briefly down is not held to yesterday.
	b.recordOutcome(towerID, sick, "sick-recovered", reputation.CanaryPass)
	byStation = towerWindow(t, b, towerID)
	require.False(t, canaryTroubled(byStation[sick]),
		"a Station that answered a probe is still on the slow clock")
}
