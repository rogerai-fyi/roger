package main

// towerstationstrike_test.go is the spec for the CONSEQUENCE of an edge Station being caught
// contradicting itself.
//
// Contract: features/safety/banning.feature, features/safety/appeals.feature,
// features/tower/edge_dispatch.feature.
//
// The attribution change before this one moved a Station's own failures off its Tower, which
// was right, and left them with nothing attached to them, which its own author flagged: a
// Station that inflates while its Tower relays produced a ledger row nobody acts on. The
// founder authorised closing that, and what closes it is the owner-strike ladder the classic
// fabric has always used for this exact offence - not a new station-suspension state.
//
// The assertions here are paired the same way the attribution tests are, because a
// consequence that only ever fires is as wrong as one that never does. For every strike this
// records there is a neighbouring case that must record NONE, and the case that must record
// none is always one a hostile TOWER can produce - staying silent, claiming a Station lost
// the transcript, or corrupting the loose plaintext it supplies itself. If any of those
// struck an operator, this change would have built the denial primitive the last one removed.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/audit"
)

// strikeLadderBroker is a tower broker with the PRODUCTION strike thresholds set.
//
// It is not a convenience. testBrokerWithDB leaves every threshold at the int zero value, and
// a zero strikeBanAt with a zero strikeCorroborateKinds means the FIRST strike of any kind
// bans the account - so a test that did not set these would assert against a ladder that
// exists nowhere and would report a ban as evidence of proportionality.
func strikeLadderBroker(t *testing.T) (*broker, *httptest.Server) {
	t.Helper()
	b, srv := towerTestBroker(t)
	b.strikeWarnAt, b.strikeBanAt = defaultStrikeWarnAt, defaultStrikeBanAt
	b.strikeCorroborateKinds, b.strikeDecayDays = defaultStrikeCorroborateKinds, defaultStrikeDecayDays
	return b, srv
}

// strikesOf reads an owner's durable strike evidence back.
func strikesOf(t *testing.T, b *broker, account string) []store.Strike {
	t.Helper()
	rows, err := b.db.StrikesByOwner(account, 0)
	require.NoError(t, err)
	return rows
}

// A VERIFYING TRANSCRIPT THAT CONTRADICTS THE RECEIPT'S DIGESTS STRIKES THE STATION'S OWNER.
//
// The Station signed two incompatible accounts of one attempt under the key on its own
// attachment. Nothing the Tower can do produces that - it holds neither signature - which is
// why this finding was moved off the Tower in the first place, and why it is safe to give it
// teeth here.
//
// AND IT LANDS ON THE ATTACHMENT'S OWNER, which is the part that had to be proved rather than
// assumed. Two Stations sit behind ONE Tower under two different accounts; only the one that
// signed the contradiction is struck. A resolution through the node join instead would have
// struck nobody here (these attachments carry no node id) and, where one exists, would answer
// a different question - whoever registered that node id, who need not be this Station's owner.
func TestAContradictedTranscriptStrikesTheStationsOwnerAndNobodyElse(t *testing.T) {
	b, srv := strikeLadderBroker(t)
	tw := enrolledTower(t, b, "tower-operator")
	liar := attachStation(t, b, "st-liar", tw.id, "acct-liar")
	attachStation(t, b, "st-honest", tw.id, "acct-honest")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// Core wanted a transcript for these digests; the Station signs a DIFFERENT response.
	wantAudit(t, b, tw.id, "st-liar", "att-contradiction", []byte("the prompt"), []byte("the real answer"))
	obj, reqB64, respB64 := signedTranscript(t, liar, "att-contradiction",
		[]byte("the prompt"), []byte("a substituted answer"))
	code := postTranscript(t, tw, srv, map[string]any{
		"tower_id": tw.id, "attempt_id": "att-contradiction", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.Equal(t, http.StatusOK, code)

	rows := strikesOf(t, b, "acct-liar")
	require.Len(t, rows, 1, "a Station proved it signed two accounts of one attempt and its owner carried no consequence at all")
	require.Equal(t, store.StrikeStationMisreport, rows[0].Kind)

	require.Empty(t, strikesOf(t, b, "acct-honest"),
		"an innocent Station's owner behind the same Tower was struck for its neighbour")
	require.Empty(t, strikesOf(t, b, "tower-operator"),
		"the Tower's operator was struck for a Station's signature")
	require.Empty(t, strikesOf(t, b, "st-liar"),
		"the strike landed on the STATION id rather than on the account that owns it")

	// PROPORTIONATE. One contradiction is evidence, not a sentence: the earnings are held and
	// the ladder is entered, and the durable ban stays behind the count AND the corroboration
	// guard. An honest operator whose Station frames prompts differently from the way it bills
	// them trips this arm every time, and must not be bannable on that alone.
	banned, _, err := b.db.IsOwnerBanned("acct-liar")
	require.NoError(t, err)
	require.False(t, banned, "one proven contradiction durably banned an account on its own evidence")
}

// AND THE EVIDENCE IS RE-DERIVABLE, which is the difference between a finding and an
// accusation. An operator appealing this is disputing that their own machine signed both of
// these statements; the blob has to carry enough for a human to check that, not just enough
// to justify the row.
func TestTheStationStrikeCarriesTheEvidenceAnAppealWouldNeed(t *testing.T) {
	b, srv := strikeLadderBroker(t)
	tw := enrolledTower(t, b, "tower-operator")
	priv := attachStation(t, b, "st-usage", tw.id, "acct-usage")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("the prompt"), []byte("the real answer")
	// The receipt claimed MORE output bytes than the Station's own transcript proves it sent.
	// The digests agree, the plaintext hashes to them, and the length is therefore a fact.
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-usage", StationID: "st-usage",
		RequestDigest: digestLike(req), ResponseDigest: digestLike(resp),
		UsageIn: int64(len(req)), UsageOut: int64(len(resp)) + 4096,
		Deadline: time.Now().Add(time.Hour),
	}))
	obj, reqB64, respB64 := signedTranscript(t, priv, "att-usage", req, resp)
	code := postTranscript(t, tw, srv, map[string]any{
		"tower_id": tw.id, "attempt_id": "att-usage", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.Equal(t, http.StatusOK, code)

	rows := strikesOf(t, b, "acct-usage")
	require.Len(t, rows, 1, "a usage claim contradicted by the bytes the Station signed for cost its owner nothing")
	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(rows[0].Evidence), &ev))
	at, found, err := b.tower.stations.Station("st-usage")
	require.NoError(t, err)
	require.True(t, found)
	// WHAT WAS SIGNED, BY WHICH KEY, AND WHAT IT CONTRADICTED.
	require.Equal(t, at.AssertionKey, ev["station_assertion_key"],
		"the blob does not say which key signed, so the finding can only be taken on trust")
	require.Equal(t, "st-usage", ev["station_id"])
	require.Equal(t, tw.id, ev["tower_id"])
	require.Equal(t, "att-usage", ev["attempt_id"])
	require.EqualValues(t, len(resp)+4096, ev["receipt_usage_out"], "the claim is missing")
	require.EqualValues(t, len(resp), ev["proven_bytes_out"], "what disproved it is missing")
	require.Equal(t, digestLike(resp), ev["receipt_response_digest"],
		"the digest tying the proven bytes to the receipt is missing, so the length proves nothing")
}

// ONE OFFENCE, ONE STRIKE, HOWEVER MANY TIMES THE EVIDENCE IS RE-READ.
//
// The courier re-forwards on a fifteen-second spool, a peer instance can be handed the same
// submission, and an audit can be re-examined. Without a stable idempotency key a single
// contradiction would climb the ladder on its own and reach a ban by retry, which is the
// account being punished for OUR plumbing rather than for its own signature.
func TestOneContradictionIsOneStrikeHoweverOftenItIsReExamined(t *testing.T) {
	b, srv := strikeLadderBroker(t)
	tw := enrolledTower(t, b, "tower-operator")
	priv := attachStation(t, b, "st-retry", tw.id, "acct-retry")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	obj, reqB64, respB64 := signedTranscript(t, priv, "att-retry",
		[]byte("the prompt"), []byte("a substituted answer"))
	body := map[string]any{
		"tower_id": tw.id, "attempt_id": "att-retry", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	}
	// Re-wanting between submissions is what makes this a real re-drive rather than a test of
	// the wanted list: the handler resolves the want on the way out, so a second POST would
	// otherwise short-circuit and prove nothing about the idempotency key at all.
	for i := 0; i < 4; i++ {
		wantAudit(t, b, tw.id, "st-retry", "att-retry", []byte("the prompt"), []byte("the real answer"))
		require.Equal(t, http.StatusOK, postTranscript(t, tw, srv, body))
	}
	require.Len(t, strikesOf(t, b, "acct-retry"), 1,
		"one contradiction stacked a strike per re-examination and would reach a ban by retry")
	banned, _, err := b.db.IsOwnerBanned("acct-retry")
	require.NoError(t, err)
	require.False(t, banned)
}

// AND THE THINGS A HOSTILE TOWER CAN ACTUALLY DO MUST STRIKE NOBODY.
//
// This is the guard on the over-correction, and it is the whole reason the trigger is the
// stationFault arm rather than "the audit failed". A Tower can stay silent, it can claim its
// Station lost the transcript, and it can corrupt the plaintext it forwards beside an honest
// transcript - and it can do all three at will, against any Station it carries, without
// holding a single one of that Station's keys. If any of them reached an operator's strike
// ledger, this change would have handed a black-holing Tower a way to freeze the earnings of
// every machine behind it.
func TestNothingATowerCanDoOnItsOwnStrikesAStationsOwner(t *testing.T) {
	b, srv := strikeLadderBroker(t)
	req, resp := []byte("the prompt"), []byte("the real answer")
	// One Tower per behaviour, because each of these is quarantine-grade evidence about the
	// TOWER and the first one suspends it - after which the route stops answering and the rest
	// would pass for the wrong reason. All three Stations belong to the SAME account, which is
	// the account the assertion is about.
	newLeg := func(station string) (linkTower, ed25519.PrivateKey) {
		tw := enrolledTower(t, b, "tower-operator")
		priv := attachStation(t, b, station, tw.id, "acct-victim")
		require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
		return tw, priv
	}

	// 1. The Tower substitutes the loose plaintext beside a transcript that is entirely honest.
	corrupt, priv := newLeg("st-corrupt")
	wantAudit(t, b, corrupt.id, "st-corrupt", "att-corrupt", req, resp)
	obj, _, _ := signedTranscript(t, priv, "att-corrupt", req, resp)
	require.Equal(t, http.StatusOK, postTranscript(t, corrupt, srv, map[string]any{
		"tower_id": corrupt.id, "attempt_id": "att-corrupt", "available": true,
		"transcript": obj,
		"request":    base64.StdEncoding.EncodeToString(req),
		"response":   base64.StdEncoding.EncodeToString([]byte("bytes the tower made up")),
	}))

	// 2. The Tower says the Station did not keep it, on a SAMPLED attempt - the strongest
	//    version of the excuse, the one that is a finding rather than a soft miss.
	excuse, _ := newLeg("st-excuse")
	require.True(t, auditSampled("att-s0"))
	wantAudit(t, b, excuse.id, "st-excuse", "att-s0", req, resp)
	b.markAuditProven("st-excuse")
	require.Equal(t, http.StatusOK, postTranscript(t, excuse, srv, map[string]any{
		"tower_id": excuse.id, "attempt_id": "att-s0", "available": false,
	}))

	// 3. The Tower stays silent until the deadline passes.
	silent, _ := newLeg("st-silent")
	// A SAMPLED attempt, and a Station already held to the standard: the softened cases (an
	// off-sample want, a Station that has never answered one) are deliberately not findings at
	// all, so using one would prove nothing about attribution.
	require.True(t, auditSampled("att-silent-5"))
	b.markAuditProven("st-silent")
	wantAudit(t, b, silent.id, "st-silent", "att-silent-5", req, resp)
	b.sweepAuditOverdue(time.Now().Add(2 * time.Hour))

	require.Empty(t, strikesOf(t, b, "acct-victim"),
		"a Tower froze an honest operator's earnings by relaying badly - the denial primitive the attribution change removed, rebuilt on the strike ladder")
	// And the Tower answers for all three, which is the half that stops the exemption being an
	// amnesty: if these had been moved off the Tower as well, the cheapest lie would also be
	// the cheapest escape.
	for _, tw := range []linkTower{corrupt, excuse, silent} {
		got, _ := b.tower.registry.Get(tw.id)
		require.Equal(t, admit.StateSuspended, got.State,
			"a Tower paid nothing for substituting plaintext, refusing an audit or going silent")
	}
}

// postTranscript posts one transcript submission as the TOWER - through linkTower.call, so it
// is the production route with a real Tower signature - and returns the status.
func postTranscript(t *testing.T, tw linkTower, srv *httptest.Server, body map[string]any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	code, _ := tw.call(t, srv, "/tower/audit/transcript", raw, nil)
	return code
}
