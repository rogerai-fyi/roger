package main

// toweredgeattach_contract_test.go is THE REAL NODE AGAINST THE REAL HANDLER, and it exists
// because the test that claimed to be that was only half of it.
//
// internal/agent/tower_attach_proof_test.go calls itself "the contract between the halves", and
// for the NODE's side it is one: it drives the real agent.AttachTower and the real
// protocol.AttachProof.Verify. But CORE's half - which value the handler pulls from where - is
// hand-copied into that test's httptest handler, and it cannot be otherwise from inside
// internal/agent, because the handler lives in package main here and nothing may import it.
//
// That copy is exactly why a real defect stayed invisible. The handler put the UNTRIMMED
// req.StationID into the proof statement while validating and binding the TRIMMED one; the
// test's copy did the same thing by accident; a real node sends a clean id, so the two agreed
// and the suite was green. Two independent restatements of one wiring agreeing with each other
// is not a contract - it is the same mistake made twice.
//
// So the pairing is pinned HERE, where both halves are the production ones: agent.AttachTower
// builds and signs the request, the broker's own route table routes it, towerEdgeAttach reads
// it, and every assertion is about the ROW that results or the identity on the node's disk.
// Nothing in this file restates a field name that either half also decides.
//
// The Core-side half of the same question - that a loosely spelled field is canonicalized before
// it is signed rather than after - is in toweredgeattach_identity_test.go, which can send bodies
// no honest node would.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/attach"
)

// A REAL `roger share` ATTACHES TO A REAL CORE, and what Core records is the identity the node
// holds on disk - every field of it, resolved by the production code on both ends.
func TestARealNodeAttachesToTheRealHandler(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	// The node's account, and the `roger share` registration the M0 join requires - the state a
	// machine is genuinely in by the time it attaches.
	op := signedInOperator(t, b, "contract-node-op")
	nodeID := registerShareNode(t, b, op)

	dir := t.TempDir()
	st, at, err := agent.AttachTower(agent.Config{
		Broker: srv.URL, NodeID: nodeID, Model: "m", Modality: "text",
	}, op.priv, dir)
	require.NoError(t, err, "the real node could not attach to the real handler")

	// WHAT THE NODE WAS TOLD. The endpoint and the relay fingerprint are checked by AttachTower
	// itself, which refuses without them, so what is left to assert here is the placement.
	require.Equal(t, tw.id, at.TowerID)
	require.Equal(t, "203.0.113.9:8443", at.Endpoint)

	// WHAT CORE RECORDED. This is the assertion the hand-copied handler could not make at all:
	// it verified a proof and answered, but no row ever existed for it to be wrong about.
	row, found, err := b.tower.stations.Station(at.StationID)
	require.NoError(t, err)
	require.True(t, found, "the attach answered 200 and recorded nothing")
	require.Equal(t, st.StationID, row.StationID, "Core bound an id the node does not answer to")
	require.Equal(t, hex.EncodeToString(st.AssertionPub()), row.AssertionKey)
	require.Equal(t, hex.EncodeToString(st.SessionPub()), row.SessionKey)
	require.Equal(t, ownerPubkeyOf(t, b, "contract-node-op"), row.Owner,
		"the payee is the ACCOUNT that signed, not the Station")
	require.Equal(t, nodeID, row.NodeID, "the verified join rides onto the attachment")
	require.Equal(t, attach.StateActive, row.State)

	// AND THE IDENTITY IS THE DERIVED ONE, arrived at INDEPENDENTLY by both halves: the node
	// stamped it into station.json from its own key at Init, and Core recomputed it from the key
	// in the body and refuses anything else. If either side changes how that is derived they
	// stop agreeing right here, rather than in a support thread.
	require.Equal(t, protocol.DeriveStationID(st.AssertionPub()), row.StationID)

	// RE-ATTACH IS THE SAME STATION, which is what a restart does and what makes the persistent
	// on-disk identity worth having: same directory, same keys, same id, answered idempotently
	// rather than minting a second Station.
	st2, at2, err := agent.AttachTower(agent.Config{
		Broker: srv.URL, NodeID: nodeID, Model: "m", Modality: "text",
	}, op.priv, dir)
	require.NoError(t, err)
	require.Equal(t, st.StationID, st2.StationID)
	require.Equal(t, at.StationID, at2.StationID)
}

// A DIRECTORY MINTED BEFORE DERIVED IDS STILL ATTACHES, end to end, which is the whole migration
// story and the reason station.Open repairs rather than refuses.
//
// A Station directory from before this rule holds a random id beside keys that are perfectly
// good. Core will not accept that id - it is not the one the key mints - so if Open passed it
// through, the machine would be locked out by the very change that exists to stop machines being
// locked out. Open restamps it, says so in a warning the serving loop prints, and the attach
// goes through under the right identity. Nothing in the field is in this position (self-attach
// is absent from tag v5.7.1); development directories are.
func TestAStationDirectoryFromBeforeDerivedIdsStillAttaches(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	op := signedInOperator(t, b, "legacy-op")
	nodeID := registerShareNode(t, b, op)

	// Mint a Station the way the node does, then rewrite its id to a random one - which is
	// precisely what the old station.Init wrote.
	dir := t.TempDir()
	st0, _, err := agent.AttachTower(agent.Config{
		Broker: srv.URL, NodeID: nodeID, Model: "m", Modality: "text",
	}, op.priv, dir)
	require.NoError(t, err)
	derived := st0.StationID

	stateFile := filepath.Join(dir, "tower-station", "station.json")
	rawState, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(rawState, &onDisk))
	onDisk["station_id"] = "st-legacyrandom00000000aa"
	rewritten, err := json.Marshal(onDisk)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stateFile, rewritten, 0o600))

	st, at, err := agent.AttachTower(agent.Config{
		Broker: srv.URL, NodeID: nodeID, Model: "m", Modality: "text",
	}, op.priv, dir)
	require.NoError(t, err, "a directory minted before derived ids could not attach")
	require.Equal(t, derived, st.StationID, "Open did not restamp the legacy id")
	require.Equal(t, derived, at.StationID)
	require.NotEmpty(t, st.Warnings, "the restamp must be said out loud, not done quietly")
	require.Contains(t, st.Warnings[len(st.Warnings)-1], "st-legacyrandom00000000aa",
		"the warning names the id that was replaced, so an operator can match it to their records")
}
