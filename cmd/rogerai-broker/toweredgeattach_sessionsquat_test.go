package main

// toweredgeattach_sessionsquat_test.go is the SESSION-KEY SQUAT, and the proof that the thing
// it was aimed at is gone rather than merely defended.
//
// It is the sibling of toweredgeattach_squat_test.go and the last of the three values
// /tower/edge/attach names. The assertion key is proved by a co-signature and the Station id is
// proved by derivation; the secure-session key is X25519 and cannot sign, so nothing in the
// possession proof says the caller holds its private half - the assertion key merely VOUCHES
// for it.
//
// # WHAT THAT USED TO BUY AN ATTACKER
//
// A uniqueness rule in checkBindings ("that secure-session key is already bound to another
// Station"), the same rule again under memStore.Admit's mutex, and two partial unique indexes in
// Postgres holding the durable half of it. So an attacker with their own account,
// their own assertion keypair and their own registered node id could attach while naming the
// VICTIM's session public key - 200 - and the victim, whose two keys are entirely their own,
// then met 409 on every attach for as long as the squat stood. station.InitOrOpen keeps that
// key on disk with no re-mint path, so it was the same self-renewing, indefinite denial the
// assertion-key squat was, for the price of one request.
//
// And the input was self-serve: /tower/edge/authorize hands station_session_key to ANY signed-in
// funded consumer that asks for that Station's model. For a niche model with one provider that is
// one call to obtain the key that locks its only node out.
//
// # WHAT WAS DONE, AND WHY IT IS A DELETION RATHER THAN A DEFENCE
//
// The uniqueness rule was removed. It was protecting nothing - see
// TestSharingASessionKeyTerminatesNobodysChannel, which is the falsification of the sentence
// that used to sit above it in checkBindings - while providing the entire lockout primitive.
// docs/relay-selection-design.md 5.6 carries the whole argument.
//
// These tests are the pair that makes that claim checkable: the denial is gone, and nothing a
// duplicate session key could reach is reachable through it.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// THE ATTACK, RUN IN FULL, AGAINST THE REAL ROUTE TABLE - and nobody is denied.
//
// The attacker gets everything a successful attach needs and everything the assertion-key squat
// tests are careful to give them: a live tower, a real signed-in account, a registered and
// heartbeating node id of their own, and an assertion keypair they genuinely hold. The one
// dishonest field is the session key, which is the victim's.
//
// Before the fix this test failed on its last third: the squat answered 200 and the victim's own
// attach answered 409 "that secure-session key is already bound to another Station".
func TestNamingAnotherStationsSessionKeyDeniesNobody(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	// The victim's Station identity, minted on the victim's machine. Its session public key is
	// the one field of it an attacker can simply ask Core for.
	victim := signedInOperator(t, b, "victim")
	victimBody, victimAssertion := selfAttachBodyFor(t, b, victim)
	victimSession := victimBody["session_key"].(string)

	// The squat. Their own assertion key, so the possession proof verifies; their own node id, so
	// the M0 join passes; the victim's session key, which nothing asks them to prove.
	squatter := signedInOperator(t, b, "squatter")
	squatBody, squatAssertion := selfAttachBodyFor(t, b, squatter)
	squatBody["session_key"] = victimSession
	var squatted struct {
		StationID string `json:"station_id"`
	}
	code, raw := squatter.attach(t, srv, squatBody, &squatted)
	require.Equal(t, http.StatusOK, code, raw,
		"the squat is not refused and is not meant to be - naming a key you cannot open is a "+
			"wound you inflict on yourself, and Core cannot tell the two apart at attach time")
	require.Equal(t, protocol.DeriveStationID(squatAssertion), squatted.StationID,
		"the squatter still gets the identity THEIR OWN key mints, never the victim's")

	// THE PROPERTY. The victim attaches with two keys that are entirely their own, from their own
	// account, on their first try - which is what the uniqueness rule used to make impossible.
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw = victim.attach(t, srv, victimBody, &attached)
	require.Equal(t, http.StatusOK, code, raw,
		"the victim was denied their own identity by a key somebody else merely named")
	require.Equal(t, protocol.DeriveStationID(victimAssertion), attached.StationID)

	// BOTH ROWS STAND. A refusal that had merely moved - the victim winning and the squatter
	// being evicted - would be the same denial with the parties swapped.
	victimRow, found, err := b.tower.stations.Station(attached.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hexOf(victimAssertion), victimRow.AssertionKey)
	require.Equal(t, victimSession, victimRow.SessionKey)
	require.Equal(t, ownerPubkeyOf(t, b, "victim"), victimRow.Owner)

	squatRow, found, err := b.tower.stations.Station(squatted.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, victimSession, squatRow.SessionKey, "the duplicate is real, not normalized away")
	require.Equal(t, ownerPubkeyOf(t, b, "squatter"), squatRow.Owner)
}

// AND THE SENTENCE THE REMOVED RULE GAVE FOR ITSELF IS FALSE. It read: "A secure-session key
// belonging to another Station would let one machine terminate another's end-to-end channel."
//
// It cannot, because NOTHING ROUTES BY THE SESSION KEY. A consumer is placed onto a Station, and
// the key it seals to is read out of THAT Station's row - so two rows carrying one key are two
// destinations, not one. The squatter receives ciphertext sealed to a private half they do not
// hold, cannot serve it, and cannot forward it either: the grant names their relay, and a
// receipt for it has to be signed by THEIR assertion key, which the victim does not have.
//
// This test pins the half a change could silently break. Two Stations share a session key and
// serve different models; each authorize answer names ITS OWN relay while carrying the shared
// key honestly. It goes red the day placement, dispatch or the authorize projection starts
// resolving a destination from the session key - and it could not have been written at all
// before the fix, because the second of these two Stations could not exist.
func TestSharingASessionKeyTerminatesNobodysChannel(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	victim := signedInOperator(t, b, "victim")
	victimBody, _ := selfAttachBodyFor(t, b, victim)
	victimBody["model"] = "victim-model"
	shared := victimBody["session_key"].(string)
	var victimStation struct {
		StationID string `json:"station_id"`
	}
	code, raw := victim.attach(t, srv, victimBody, &victimStation)
	require.Equal(t, http.StatusOK, code, raw)

	squatter := signedInOperator(t, b, "squatter")
	squatBody, _ := selfAttachBodyFor(t, b, squatter)
	squatBody["model"] = "squatter-model"
	squatBody["session_key"] = shared
	var squatStation struct {
		StationID string `json:"station_id"`
	}
	code, raw = squatter.attach(t, srv, squatBody, &squatStation)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEqual(t, victimStation.StationID, squatStation.StationID)

	consumer := signedInConsumer(t, b)
	for _, tc := range []struct {
		model   string
		station string
	}{
		{"victim-model", victimStation.StationID},
		{"squatter-model", squatStation.StationID},
	} {
		code, out := consumerCall(t, srv, consumer, "/tower/edge/authorize",
			map[string]any{"model": tc.model, "consumer_env_key": testEnvKeyHex(t)})
		require.Equal(t, http.StatusOK, code, out)
		require.Equal(t, tc.station+"."+relayDomain(), out["relay_name"],
			"placement resolved a destination from the model, and the shared key did not move it")
		require.Equal(t, shared, out["station_session_key"],
			"both rows honestly carry the key their operator named")
	}
}
