package main

// toweredgeattach_identity_test.go is the second round on the attach possession proof: what the
// proof did NOT cover once it existed.
//
// The co-signature closed "I bind a key I do not hold". An adversarial review of it then found
// three ways the identity a Station ends up with is still not the identity it proved, and every
// one of them was demonstrated against the running handler rather than argued:
//
//  1. THE PROOF BINDS THE STATION ID AND PROVES NOTHING ABOUT IT. It is signed by the claimant's
//     OWN assertion key, so "I claim somebody else's id with keys that are honestly mine" was a
//     valid proof, and the id was reachable: ReapTerminal DELETES a terminal attachment thirty
//     days after a revoke, and the id it frees is public - the relay_name in every authorize
//     answer that Station served, the leftmost label of its relay DNS name, and in the placement
//     logs. Squatting it is a permanent denial, because the rightful machine keeps that id on
//     disk with no re-mint path.
//  2. THE ID SIGNED WAS NOT THE ID BOUND. The shape gate validated the TRIMMED station id and
//     the mint used the trimmed form, while the proof statement named the RAW field - so a proof
//     over "\n\n\nst-x\n" verified and "st-x" was bound.
//  3. ONE KEY COULD BECOME TWO STATIONS. Verify hex-decodes, which is case-insensitive; every
//     uniqueness path compares the string. So the same real keypair attached twice, once in
//     lowercase and once in uppercase.
//
// All three are one defect: the value that was CHECKED was not the value that was USED. The
// handler now canonicalizes each identity field exactly once, before anything reads it, and
// derives the Station id from the assertion key so that naming an identity and proving it are
// the same act.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// derivedRefusal is the sentence a Station id that is not the one its key mints gets. Asserted
// by text, like squatRefusal, because 400 is also what the shape gate answers with and "refused
// somehow" is the kind of green this repo has now found a dozen of.
const derivedRefusal = "not the identity this assertion key mints"

// A REAPED STATION ID CANNOT BE TAKEN BY SOMEBODY ELSE, which is the whole of finding 1.
//
// The sequence is the attacker's, exactly: the victim revokes, the reaper deletes the terminal
// row thirty days later, and the id - which has been public the entire time - is free. Before
// derivation, the squatter's own keys plus the victim's id answered 200, and the victim's
// machine, which keeps that id on disk forever, was then refused its own identity with "this
// Station ID is already bound to another assertion key" on every re-attach, indefinitely, with
// no recovery short of destroying the Station directory or a human at Core.
func TestAReapedStationIdCannotBeSquatted(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	// The victim's Station, on air under the id its assertion key mints.
	victim := signedInOperator(t, b, "victim")
	victimBody, victimKey := selfAttachBodyFor(t, b, victim)
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := victim.attach(t, srv, victimBody, &attached)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, protocol.DeriveStationID(victimKey), attached.StationID,
		"a Station's id is the one its assertion key mints, not a random one")

	// The owner retires it, and thirty days later the reaper deletes the row outright. This is
	// the step that opens the window: while ANY row exists, in any state including terminal,
	// checkBindings protects the id. Once it is deleted there is nothing left to protect it -
	// which is why an ownership lookup cannot be the fix, because after this there is nothing
	// for the RIGHTFUL owner to be looked up against either.
	moved, err := b.tower.stations.Revoke(attached.StationID)
	require.NoError(t, err)
	require.True(t, moved)
	reaped, err := b.tower.stationStore.ReapTerminal(time.Now().Add(terminalAttachmentHorizon))
	require.NoError(t, err)
	require.Equal(t, int64(1), reaped, "the reaper must actually have deleted the row")
	_, stillThere, err := b.tower.stations.Station(attached.StationID)
	require.NoError(t, err)
	require.False(t, stillThere, "the id is genuinely free, which is what makes this reachable")

	// The squatter has everything a successful attach needs - their own account, their own
	// node id, their own keys, a live tower - and the victim's Station id, which they read off
	// any authorize answer that Station ever served.
	squatter := signedInOperator(t, b, "squatter")
	squatBody, _ := selfAttachBodyFor(t, b, squatter)
	squatBody["station_id"] = attached.StationID
	var out map[string]any
	code, raw = squatter.attach(t, srv, squatBody, &out)
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, derivedRefusal,
		"refused because the id is not this key's, not for some other reason")

	// NOTHING WAS RECORDED under that id, and the victim can come home. Same keys, same id, and
	// the row it lands on is a brand new one - the reap really did delete the old attachment,
	// so this is a fresh admission of an identity that was gone, not a revival.
	_, taken, err := b.tower.stations.Station(attached.StationID)
	require.NoError(t, err)
	require.False(t, taken, "a refused squat binds nothing")

	back, _ := selfAttachBodyFor(t, b, victim)
	back["assertion_key"] = victimBody["assertion_key"]
	back["session_key"] = victimBody["session_key"]
	back["station_id"] = attached.StationID
	var recovered struct {
		StationID string `json:"station_id"`
	}
	code, raw = victim.attach(t, srv, back, &recovered)
	require.Equal(t, http.StatusOK, code,
		"the machine that holds the key was refused its own reaped id: %s", raw)
	require.Equal(t, attached.StationID, recovered.StationID)
}

// AND THE OTHER HALF OF FINDING 1: an id is not a name a caller gets to pick.
//
// This is the ordinary case rather than the reaped one, and it is here because it is the check
// that makes the reaped case unreachable rather than merely guarded. There is no lookup in it:
// a Station id is a function of the assertion key, so an id that does not match is refused
// before anything is read from the store at all.
func TestAStationIdMustBeTheOneItsAssertionKeyMints(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	body, apub := selfAttachBodyFor(t, b, node)
	body["station_id"] = "st-anameipicked"
	var out map[string]any
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, derivedRefusal)
	require.Contains(t, raw, protocol.DeriveStationID(apub),
		"the refusal names the id this key does mint, so an operator can act on it")

	// The same body with the derived id attaches, which is what makes the assertion above about
	// the id rather than about the rest of the request.
	body["station_id"] = protocol.DeriveStationID(apub)
	var okOut struct {
		StationID string `json:"station_id"`
	}
	code, raw = node.attach(t, srv, body, &okOut)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, protocol.DeriveStationID(apub), okOut.StationID)
}

// THE STATION ID SIGNED IS THE STATION ID BOUND - finding 2, which is the reason the proof names
// the id at all ("a reader of a log line can see what was proved").
//
// strings.TrimSpace strips \n \r \t \v \f U+0085 U+00A0, none of which attach.ValidStationID
// allows, and the handler used to validate the trimmed id, mint the trimmed id, and sign the
// RAW one. Both halves below were the wrong way round before the fix: the padded spelling
// verified and bound something else, and the canonical spelling was refused.
func TestTheStationIdInTheProofIsTheStationIdBound(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	body, apub := selfAttachBodyFor(t, b, node)
	canonical := protocol.DeriveStationID(apub)
	padded := "\n\n\n" + canonical + "\n"
	body["station_id"] = padded
	priv, known := assertionKeyOf(hexOf(apub))
	require.True(t, known)

	// 1. A PROOF OVER THE PADDED SPELLING IS REFUSED. This is the exact request that used to
	//    answer 200 and bind the trimmed id: the statement said one thing and the store recorded
	//    another, so the id in the proof was decoration.
	var out map[string]any
	code, raw := node.attachSigned(t, srv, body,
		func(callerPub string, ts int64, b []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				StationID: padded, AssertionKey: hexOf(apub),
				SessionKey: body["session_key"].(string), Body: b,
			}.Sign(priv)
		}, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal,
		"the padded spelling is not what Core verifies against, so this must fail as a proof")

	// 2. A PROOF OVER THE CANONICAL SPELLING IS ACCEPTED, and the row carries exactly the id
	//    that was signed. Core canonicalizes the field once, before the proof, so the caller
	//    signs what Core binds even when the body is spelled loosely.
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw = node.attachSigned(t, srv, body,
		func(callerPub string, ts int64, b []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				StationID: canonical, AssertionKey: hexOf(apub),
				SessionKey: body["session_key"].(string), Body: b,
			}.Sign(priv)
		}, &attached)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, canonical, attached.StationID)

	at, found, err := b.tower.stations.Station(canonical)
	require.NoError(t, err)
	require.True(t, found, "the Station is stored under the id that was signed")
	require.Equal(t, canonical, at.StationID)
}

// ONE KEY IS ONE STATION, WHATEVER CASE ITS HEX IS SPELLED IN - finding 3.
//
// protocol.AttachProof.Verify hex-decodes, and hex decoding is case-insensitive. Every
// uniqueness path compares STRINGS: memStore.ByAssertionKey, PGStore.byLiveKey, checkBindings,
// and the lost-response retry. So one real keypair, presented as lowercase and then as
// uppercase, produced TWO Stations from one signer - which is exactly what stationattach.go
// says cannot happen ("two Stations signing offers with one key are one signer wearing two
// identities"). It was never an attacker's primitive, since the private half is needed both
// times; it is an invariant that was false, and the possession proof would have inherited it.
//
// THE STORES ARE NOT WHAT WAS FIXED, and TestParityKeyLookupsAreExactStrings pins that: both
// remain exact-string, on Postgres as well, so this handler's normalization is the only thing
// holding the invariant up.
func TestOneAssertionKeyCannotBecomeTwoStationsByChangingItsHexCase(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	body, apub := selfAttachBodyFor(t, b, node)
	lower := hexOf(apub)
	priv, known := assertionKeyOf(lower)
	require.True(t, known)
	var first struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.attach(t, srv, body, &first)
	require.Equal(t, http.StatusOK, code, raw)

	// The same keypair again, spelled in uppercase, from a second machine of the same operator.
	upperA, upperS := strings.ToUpper(lower), strings.ToUpper(body["session_key"].(string))
	second, _ := selfAttachBodyFor(t, b, node)
	second["assertion_key"], second["session_key"] = upperA, upperS

	// 1. SIGNED OVER THE UPPERCASE SPELLING, which is what a caller doing this by hand would do
	//    and what answered 200 before. Core canonicalizes before it verifies, so the statement
	//    it rebuilds names the lowercase form and this proof is simply not over it.
	var out map[string]any
	code, raw = node.attachSigned(t, srv, second,
		func(callerPub string, ts int64, b []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: upperA, SessionKey: upperS, Body: b,
			}.Sign(priv)
		}, &out)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, squatRefusal)

	// 2. SIGNED OVER THE CANONICAL SPELLING, so the proof verifies and the request reaches the
	//    uniqueness reads it always should have reached. THIS is the assertion that matters: the
	//    key resolves to the Station it is already bound to, and the answer is that same
	//    registration rather than a second identity. Before the fix, ByAssertionKey was asked
	//    about the uppercase string, found nothing, and minted one.
	var again struct {
		StationID string `json:"station_id"`
		Note      string `json:"note"`
	}
	code, raw = node.attachSigned(t, srv, second,
		func(callerPub string, ts int64, b []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: lower, SessionKey: body["session_key"].(string), Body: b,
			}.Sign(priv)
		}, &again)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, first.StationID, again.StationID,
		"the same key in a different case minted a second Station")
	require.Contains(t, again.Note, "already attached")

	// 3. AND FROM A DIFFERENT ACCOUNT, which is the shape the invariant is actually about - two
	//    Stations signing offers with one key would be one signer wearing two identities. A
	//    stranger holding the same keypair (there is no such party in reality, which is why this
	//    was never an attacker primitive) is refused rather than issued a second row.
	stranger := signedInOperator(t, b, "stranger-op")
	strangerBody, _ := selfAttachBodyFor(t, b, stranger)
	strangerBody["assertion_key"], strangerBody["session_key"] = upperA, upperS
	code, raw = stranger.attachSigned(t, srv, strangerBody,
		func(callerPub string, ts int64, b []byte) string {
			return protocol.AttachProof{
				Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
				AssertionKey: lower, SessionKey: body["session_key"].(string), Body: b,
			}.Sign(priv)
		}, &out)
	require.Equal(t, http.StatusConflict, code, raw)

	// AND THERE IS EXACTLY ONE STATION. Before the fix a second row existed under the uppercase
	// string, invisible to every lookup the lowercase half makes - which is what "one signer
	// wearing two identities" means in practice.
	_, found, err := b.tower.stations.ByAssertionKey(upperA)
	require.NoError(t, err)
	require.False(t, found, "a second Station was bound under the same key spelled differently")
	at, found, err := b.tower.stations.ByAssertionKey(lower)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first.StationID, at.StationID)
}
