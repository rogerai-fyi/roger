package main

// toweredgeattach_squat_test.go is the ASSERTION-KEY SQUAT, and the proof it is closed.
//
// # THE ATTACK, IN ONE SENTENCE
//
// /tower/edge/attach took the assertion key out of the request body and never asked the caller
// to prove they held its private half, so anyone who had merely SEEN a Station's assertion
// PUBLIC key could bind it to a Station of their own and lock the rightful owner out of their
// own identity.
//
// Seeing it costs nothing. On an unpinned hub link that key is in the clear in the
// X-Roger-Pubkey header of every poll - one every twenty-five seconds, for the life of the
// serving process - so every party on that path holds every Station's. The window is not only
// "before the owner first attaches" either: the uniqueness indexes are partial and terminal
// states release their keys deliberately, so a revocation frees a key the world already knows.
//
// It is denial and never theft - the squatter has no private half, so the Station they took can
// never poll, serve, sign a receipt or be paid - but the denial renews itself. The squat refuses
// the owner's own attach on key uniqueness; their node re-attaches on the backoff built for a
// relay having a bad day; every retry meets the same refusal until somebody intervenes. One
// request buys an indefinite outage. See docs/relay-selection-design.md 5.6.
//
// # WHAT THESE TESTS HAVE TO AVOID
//
// The trap in writing them is a red that comes from somewhere else. An attacker replaying a
// victim's body verbatim is ALREADY refused, by the M0 join (the node id in that body is not
// registered to them) - so a squat test built the lazy way passes without the possession proof
// existing at all. Every test below therefore gives the attacker a live tower, a real account,
// a node id of their own, and everything else a successful attach needs, and each one asserts
// the SPECIFIC refusal rather than merely a non-200. TestASquatterCanAttachTheirOwnKeys is the
// control: the same rig, the same attacker, their own keys, 200.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/link"
)

// squatRefusal is the one sentence a failed possession proof gets. Asserted by text rather than
// by status code alone because 403 is also what the M0 join answers with, and "refused for some
// reason" is exactly the kind of green that let eleven tests in this repo pass for the wrong one.
const squatRefusal = "not co-signed by the assertion key"

// freshKeypair is a Station's assertion keypair, minted the way a real node's station.Init does
// and NOT registered in the test keyring - the squatter must never be able to reach for the
// private half through a helper.
func freshKeypair(t *testing.T) (pubHex string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return hexOf(pub), priv
}

// THE CONTROL. Everything the squat tests need in order to mean anything: this attacker's
// account, tower, node id and body are all in working order, so a refusal below is about the
// keys and nothing else.
func TestASquatterCanAttachTheirOwnKeys(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	squatter := signedInOperator(t, b, "squatter")
	body, _ := selfAttachBodyFor(t, b, squatter)

	var out map[string]any
	code, raw := squatter.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)
}

// THE ATTACK ITSELF. The squatter has watched a hub link and holds the victim's assertion PUBLIC
// key. They have everything else a legitimate attach needs. They cannot produce the one thing
// the endpoint now demands.
func TestSelfAttachRefusesAnAssertionKeyTheCallerCannotSignFor(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	// The victim's Station identity, minted on the victim's machine and never yet attached -
	// the pre-first-attach half of the window. The squatter has its public half and nothing else.
	victimAssertion, victimAssertionPriv := freshKeypair(t)
	victimSession, _ := freshKeypair(t)

	squatter := signedInOperator(t, b, "squatter")
	squatBody, _ := selfAttachBodyFor(t, b, squatter)
	squatBody["assertion_key"] = victimAssertion
	squatBody["session_key"] = victimSession

	var out map[string]any

	// 1. NO PROOF AT ALL, which is what an attacker running yesterday's client sends. There is no
	//    tolerance branch: self-attach has never shipped in a tagged release, so accepting an
	//    absent header would be a downgrade for the convenience of a population that is empty.
	code, raw := squatter.attachSigned(t, srv, squatBody,
		func(string, int64, []byte) string { return "" }, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal, "refused for possession, not for anything else")

	// 2. A PROOF BY A KEY THE SQUATTER ACTUALLY HOLDS. This is the interesting one: the header is
	//    present, well-formed, over the right statement, and a genuine Ed25519 signature. It is
	//    simply not by the key the body names, and the verifier takes its public key FROM the
	//    claim rather than from the signer - which is the whole property.
	_, ownPriv := freshKeypair(t)
	code, raw = squatter.attachSigned(t, srv, squatBody,
		func(callerPub string, ts int64, body []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: victimAssertion, SessionKey: victimSession, Body: body,
			}.Sign(ownPriv)
		}, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal)

	// NOTHING WAS RECORDED. A refusal that still bound the key would be the same outage.
	_, found, err := b.tower.stations.ByAssertionKey(victimAssertion)
	require.NoError(t, err)
	require.False(t, found, "a refused squat binds nothing")

	// AND THE VICTIM STILL OWNS THEIR IDENTITY, which is the property the operator cares about.
	// They attach with the same keys, from their own account, and it simply works.
	victim := signedInOperator(t, b, "victim")
	victimBody, _ := selfAttachBodyFor(t, b, victim)
	victimBody["assertion_key"] = victimAssertion
	victimBody["session_key"] = victimSession
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw = victim.attachSigned(t, srv, victimBody,
		func(callerPub string, ts int64, body []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: victimAssertion, SessionKey: victimSession, Body: body,
			}.Sign(victimAssertionPriv)
		}, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	at, found, err := b.tower.stations.ByAssertionKey(victimAssertion)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, attached.StationID, at.StationID)
	require.Equal(t, ownerPubkeyOf(t, b, "victim"), at.Owner, "bound to the account that proved it")
}

// THE SAME WINDOW, REOPENED BY REVOCATION - which is the half the design record first missed. A
// key that has served is a key the whole path has seen, and terminal states release it on
// purpose so a retired Station does not hold its keys hostage forever. Possession is what makes
// the release safe.
func TestARevokedStationsFreedKeyStillCannotBeSquatted(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	victim := signedInOperator(t, b, "victim")
	body, apub := selfAttachBodyFor(t, b, victim)
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := victim.attach(t, srv, body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	// The owner retires it. The key is free again - and public, having been on the wire.
	moved, err := b.tower.stations.Revoke(attached.StationID)
	require.NoError(t, err)
	require.True(t, moved)
	_, held, err := b.tower.stations.ByAssertionKey(hexOf(apub))
	require.NoError(t, err)
	require.False(t, held, "a terminal state releases the key, which is what opens the window")

	squatter := signedInOperator(t, b, "squatter")
	squatBody, _ := selfAttachBodyFor(t, b, squatter)
	squatBody["assertion_key"] = hexOf(apub)
	var out map[string]any
	code, raw = squatter.attachSigned(t, srv, squatBody,
		func(string, int64, []byte) string { return "" }, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal)
}

// A PROOF IS NOT A BEARER TOKEN, which is the failure this scheme was most at risk of being. A
// signature over the public key alone would be liftable off the wire once and replayable by
// anybody forever - a check that exists and proves nothing. The statement names the ACCOUNT KEY
// that signs the request it rides on, so a captured proof can only ever be presented by the
// party that can also produce that account's signature.
func TestASelfAttachProofCannotBeLiftedOntoAnotherCallersRequest(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	victim := signedInOperator(t, b, "victim")
	victimBody, apub := selfAttachBodyFor(t, b, victim)
	victimPriv, ok := assertionKeyOf(hexOf(apub))
	require.True(t, ok)

	// The proof the victim's own node emits, captured verbatim off the wire together with the
	// body it covers. An on-path observer has both.
	var captured string
	code, raw := victim.attachSigned(t, srv, victimBody,
		func(callerPub string, ts int64, body []byte) string {
			captured = protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				StationID: "", AssertionKey: hexOf(apub),
				SessionKey: victimBody["session_key"].(string), Body: body,
			}.Sign(victimPriv)
			return captured
		}, nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, captured)

	// The squatter re-sends the victim's EXACT body - same keys, same node id, same everything -
	// with the victim's genuine proof, under their own account signature. Nothing about the
	// statement has changed except who is presenting it, and that is enough.
	squatter := signedInOperator(t, b, "squatter")
	var out map[string]any
	code, raw = squatter.attachSigned(t, srv, victimBody,
		func(string, int64, []byte) string { return captured }, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal,
		"refused for POSSESSION - the M0 join would also have refused this body, and the check "+
			"under test is the one that must fire first, so the assertion is on the sentence")
}

// AND IT COVERS THE WHOLE REQUEST, not merely the keys. The body digest is in the statement, so
// a proof cannot be moved onto an attach that differs in any byte - a different price, a
// different model, a different node id.
func TestASelfAttachProofDoesNotSurviveAChangedBody(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	node := signedInOperator(t, b, "node-op")
	body, apub := selfAttachBodyFor(t, b, node)
	priv, ok := assertionKeyOf(hexOf(apub))
	require.True(t, ok)

	// Signed over the offer the node meant to make...
	signedOver := map[string]any{}
	for k, v := range body {
		signedOver[k] = v
	}
	// ...and sent with a different one. Same account, same keys, same instant.
	body["price_in_micros"] = int64(500_000)
	body["price_out_micros"] = int64(500_000)

	var out map[string]any
	code, raw := node.attachSigned(t, srv, body,
		func(callerPub string, ts int64, _ []byte) string {
			asSigned, err := json.Marshal(signedOver)
			require.NoError(t, err)
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: hexOf(apub), SessionKey: body["session_key"].(string),
				Body: asSigned,
			}.Sign(priv)
		}, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal)
}

// THE COMPOSITION THIS WHOLE CHANGE EXISTS TO CLOSE MUST NOT COME BACK THROUGH THE FIX.
//
// The severity of the squat came from what it cost the victim afterwards: their node's refused
// re-attaches used to eat their twenty-five open invitations against a one-hour TTL, turning an
// honest "these keys are attached" into "this account has too many open attachments in flight" -
// a message that names neither the cause nor the cure. So the new refusal has to be free of the
// same currency. It runs before the invitation is minted, before PutAuthorizationCapped and
// before Admit, and this asserts that PAST the cap: thirty refusals - five more than the
// twenty-five that used to lock an account out - leave the squatter able to attach and the
// victim able to attach, which they would not be if a refusal spent anything durable. Thirty
// rather than three hundred because the per-account rate bucket (burst 40) is a real limit too,
// and a test that tripped it would be measuring the limiter instead of the invitation.
func TestARefusedPossessionProofSpendsNoInvitation(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	victimAssertion, victimPriv := freshKeypair(t)
	victimSession, _ := freshKeypair(t)

	squatter := signedInOperator(t, b, "squatter")
	squatBody, _ := selfAttachBodyFor(t, b, squatter)
	squatBody["assertion_key"] = victimAssertion
	squatBody["session_key"] = victimSession

	// Past maxOpenInvitesPerOwner (25), which is the count that used to lock an account out.
	for i := 0; i < maxOpenInvitesPerOwner+5; i++ {
		var out map[string]any
		code, raw := squatter.attachSigned(t, srv, squatBody,
			func(string, int64, []byte) string { return "" }, &out)
		require.Equal(t, http.StatusForbidden, code, raw)
	}

	// The squatter's OWN cap is untouched: they can still attach the keys they really hold. (The
	// bucket a refusal does spend is the caller's own rate limit, which is the point - an
	// attacker can only ever spend their own.)
	ownBody, _ := selfAttachBodyFor(t, b, squatter)
	var out map[string]any
	code, raw := squatter.attach(t, srv, ownBody, &out)
	require.Equal(t, http.StatusOK, code, raw)

	// And the victim - the account the squat was aimed at - attaches with the keys that were
	// being squatted, first try.
	victim := signedInOperator(t, b, "victim")
	victimBody, _ := selfAttachBodyFor(t, b, victim)
	victimBody["assertion_key"] = victimAssertion
	victimBody["session_key"] = victimSession
	code, raw = victim.attachSigned(t, srv, victimBody,
		func(callerPub string, ts int64, body []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: victimAssertion, SessionKey: victimSession, Body: body,
			}.Sign(victimPriv)
		}, &out)
	require.Equal(t, http.StatusOK, code, raw)
}

// A MALFORMED KEY IS STILL A 400, and the ordering that makes it so is deliberate: shape before
// crypto. Answering "that is not a key" with "you did not prove you hold it" would send an
// operator with a typo hunting a signing bug.
func TestSelfAttachStillChecksShapeBeforePossession(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	var out map[string]any
	code, raw := node.attachSigned(t, srv, map[string]any{
		"assertion_key": "zz", "session_key": "zz", "model": "m", "modality": "text",
	}, func(string, int64, []byte) string { return "" }, &out)
	require.Equal(t, http.StatusBadRequest, code, raw)

	body, _ := selfAttachBodyFor(t, b, node)
	body["station_id"] = "st-../wildcard"
	code, raw = node.attachSigned(t, srv, body,
		func(string, int64, []byte) string { return "" }, &out)
	require.Equal(t, http.StatusBadRequest, code, raw,
		"the Station id's alphabet is what keeps the proof statement free of separators")
}
