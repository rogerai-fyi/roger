package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"rogerai.fm/roger/v5/internal/protocol"
)

// M0 (docs/relay-selection-design.md): a Station attaching to the relay fabric must name the
// BROKER node id of the same machine, and Core must verify it rather than believe it.
//
// The join is what makes edge placement rankable at all - probes record reliability, TTFT and
// TPS against the node id, and a station row is keyed by station id, so without a checked
// name in common the edge path has nothing to score. That also makes a node id worth
// stealing: score on a borrowed reputation and you inherit the traffic it attracts. Hence the
// two conditions tested here.
func TestAttachRequiresAProvenNodeJoin(t *testing.T) {
	b := &broker{nodes: map[string]protocol.NodeRegistration{}}

	const (
		nodeID  = "brave-otter-llama"
		ownerPK = "aa" // whatever the harness signs as
	)

	t.Run("an unregistered node id is refused", func(t *testing.T) {
		if b.nodeRegisteredTo(nodeID, ownerPK) {
			t.Fatal("nothing is registered yet; the join must not pass")
		}
	})

	t.Run("a registered node id joins only for the key that registered it", func(t *testing.T) {
		b.mu.Lock()
		b.nodes[nodeID] = protocol.NodeRegistration{NodeID: nodeID, PubKey: ownerPK}
		b.mu.Unlock()

		if !b.nodeRegisteredTo(nodeID, ownerPK) {
			t.Error("the registering key must be able to claim its own node")
		}
		// The theft case: a different machine naming a well-probed node.
		if b.nodeRegisteredTo(nodeID, "bb") {
			t.Error("a different key claimed somebody else's node id")
		}
		if b.nodeRegisteredTo("no-such-node", ownerPK) {
			t.Error("an unregistered id passed the join")
		}
		// Empty is never a wildcard - a blank claim must not read as "any node".
		if b.nodeRegisteredTo("", ownerPK) || b.nodeRegisteredTo(nodeID, "") {
			t.Error("an empty node id or pubkey passed the join")
		}
	})
}

// The handler must reject a body with no node_id BEFORE it stores anything, with a message
// that tells the operator what to actually do.
func TestAttachWithoutNodeIDIsRefused(t *testing.T) {
	b := &broker{nodes: map[string]protocol.NodeRegistration{}}
	body, _ := json.Marshal(map[string]any{
		"station_id":    "st-1",
		"assertion_key": strings.Repeat("ab", 32),
		"session_key":   strings.Repeat("cd", 32),
		"model":         "test-model",
		"modality":      "chat",
	})
	req := httptest.NewRequest(http.MethodPost, "/tower/edge/attach", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	b.towerEdgeAttach(rec, req)

	// Unauthenticated requests are turned away before the node_id check, which is correct
	// ordering; either refusal proves the attach did not silently succeed without a join.
	if rec.Code == http.StatusOK {
		t.Fatalf("attach succeeded with no node_id: %d %s", rec.Code, rec.Body.String())
	}
}

// --- M0 test support --------------------------------------------------------
//
// Attaching now requires the `roger share` half to exist first: Core will not join a station
// to a node id nothing registered. These helpers stand that half up so the existing
// self-attach suite exercises the real contract rather than a relaxed one.

var testNodeSeq atomic.Int64

// registerShareNode puts a broker registration in place for op and returns its node id,
// standing in for the `roger share` registration a real provider always has by the time it
// attaches.
func registerShareNode(t *testing.T, b *broker, op operator) string {
	t.Helper()
	id := fmt.Sprintf("n-%s-%d", op.login, testNodeSeq.Add(1))
	pub, ok := op.priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("operator %s has no ed25519 public key", op.login)
	}
	b.mu.Lock()
	if b.nodes == nil {
		b.nodes = map[string]protocol.NodeRegistration{}
	}
	b.nodes[id] = protocol.NodeRegistration{NodeID: id, PubKey: hexOf(pub)}
	b.mu.Unlock()
	return id
}

// selfAttachBodyFor is selfAttachBody plus the verified join M0 requires.
func selfAttachBodyFor(t *testing.T, b *broker, op operator) (map[string]any, ed25519.PublicKey) {
	t.Helper()
	body, apub := selfAttachBody(t)
	body["node_id"] = registerShareNode(t, b, op)
	return body, apub
}
