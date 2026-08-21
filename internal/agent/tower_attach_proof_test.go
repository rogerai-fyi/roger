package agent

// tower_attach_proof_test.go is the CONTRACT between the two halves of the attach possession
// proof: what this node emits, and what Roger Core verifies.
//
// It exists because protocol.AttachProof is deliberately ONE type used by both sides, which is
// the right shape (a second copy of a canonical form is how a signing scheme grows a hole) and
// also the shape whose failure mode no in-package test can see: the signer and the verifier
// agree with each other by construction, so a signer that leaves out a field agrees with a
// verifier that leaves out the same field, and both stay green. What can still drift is what the
// CLIENT puts in the struct - the timestamp it names, the account key it names, the body it
// hashes - and that is what this pins, against the real AttachTower and the real verifier.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// AttachTower co-signs its attach with the Station's assertion key, over a statement Core can
// rebuild from the request alone.
func TestAttachTowerProvesItHoldsTheAssertionKey(t *testing.T) {
	var (
		verified   bool
		reason     string
		claimedKey string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// EXACTLY WHAT THE BROKER DOES (cmd/rogerai-broker/toweredgeattach.go): the keys and the
		// station id come out of the BODY, the caller key and timestamp out of the headers that
		// authenticated the request, and the proof is checked against the key being claimed.
		var req struct {
			StationID    string `json:"station_id"`
			AssertionKey string `json:"assertion_key"`
			SessionKey   string `json:"session_key"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			reason = "unreadable body"
		}
		ts, err := strconv.ParseInt(r.Header.Get(protocol.HeaderTS), 10, 64)
		if err != nil {
			reason = "no usable timestamp"
		}
		switch {
		case r.Header.Get(protocol.HeaderAttachProof) == "":
			reason = "the node sent no possession proof at all"
		case !(protocol.AttachProof{
			Network:      link.PublicNetwork,
			CallerPubkey: r.Header.Get(protocol.HeaderPubkey),
			TS:           ts,
			StationID:    req.StationID,
			AssertionKey: req.AssertionKey,
			SessionKey:   req.SessionKey,
			Body:         body,
		}).Verify(r.Header.Get(protocol.HeaderAttachProof)):
			reason = "the proof did not verify against the assertion key in the body"
		default:
			verified, claimedKey = true, req.AssertionKey
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"station_id": req.StationID, "tower_id": "tw-1",
			"endpoint": "203.0.113.9:8443", "hub_token": "t", "state": "active",
			"tower_key_hash": "00",
		})
	}))
	t.Cleanup(srv.Close)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := AttachTower(Config{
		Broker: srv.URL, NodeID: "brave-otter-m", Model: "m", Modality: "chat",
	}, priv, t.TempDir())
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !verified {
		t.Fatalf("Core could not verify this node's attach: %s", reason)
	}

	// AND IT PROVED THE KEY IT ACTUALLY SERVES UNDER. A proof over some other keypair the node
	// happened to hold would verify perfectly and mean nothing: what has to be bound is the
	// persistent Station identity on disk, the key its receipts are verified against and its
	// earnings are paid to.
	if got, want := claimedKey, hex.EncodeToString(st.AssertionPub()); got != want {
		t.Fatalf("the attach claimed %q, but this Station serves under %q", got, want)
	}
}

// The mirror image, and the reason the check is worth having at the client end too: a proof
// naming a DIFFERENT caller key does not verify. If AttachTower ever signed over something other
// than the account key that authenticates the request - a stale value, a device key where the
// account key belongs - the proof would still be a valid signature and Core would still refuse
// it, and this is the test that would say so before a release did.
func TestAnAttachProofNamingTheWrongCallerIsRefused(t *testing.T) {
	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		hdr = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"station_id": "st-x", "tower_id": "tw-1", "endpoint": "203.0.113.9:8443",
			"hub_token": "t", "state": "active", "tower_key_hash": "00",
		})
	}))
	t.Cleanup(srv.Close)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AttachTower(Config{
		Broker: srv.URL, NodeID: "brave-otter-m", Model: "m", Modality: "chat",
	}, priv, t.TempDir()); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var req struct {
		StationID    string `json:"station_id"`
		AssertionKey string `json:"assertion_key"`
		SessionKey   string `json:"session_key"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	ts, err := strconv.ParseInt(hdr.Get(protocol.HeaderTS), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// NOT VACUOUS: the proof this test then fails to transfer is a real one that verifies for
	// the caller the node named. Without this line the assertion below would hold just as well
	// against a node that sent no proof at all.
	honest := protocol.AttachProof{
		Network:      link.PublicNetwork,
		CallerPubkey: hdr.Get(protocol.HeaderPubkey),
		TS:           ts,
		StationID:    req.StationID,
		AssertionKey: req.AssertionKey,
		SessionKey:   req.SessionKey,
		Body:         body,
	}
	if !honest.Verify(hdr.Get(protocol.HeaderAttachProof)) {
		t.Fatal("the node's own proof did not verify for the caller it named")
	}

	stranger, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if (protocol.AttachProof{
		Network:      link.PublicNetwork,
		CallerPubkey: hex.EncodeToString(stranger),
		TS:           ts,
		StationID:    req.StationID,
		AssertionKey: req.AssertionKey,
		SessionKey:   req.SessionKey,
		Body:         body,
	}).Verify(hdr.Get(protocol.HeaderAttachProof)) {
		t.Fatal("this node's proof verified for a caller key it never named - it is transferable, " +
			"which makes it a bearer token rather than a proof")
	}
}
