package main

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
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
//
// SIGNED, AND OTHERWISE COMPLETE. The first version of this test posted an UNSIGNED body and
// asserted only "not 200" - which it got from towerOperator turning away an unauthenticated
// caller, several gates before anything looked at a node id. It would have passed with the
// entire M0 check deleted. So this one drives the real call: a signed-in operator, a live
// edge tower ready to host, a body that is valid in every other respect, and the ONLY thing
// missing is the join. That makes the 400 attributable, which is the whole point of a test
// about one gate.
func TestAttachWithoutNodeIDIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	body, _ := selfAttachBody(t) // deliberately NOT selfAttachBodyFor: no node_id
	if _, present := body["node_id"]; present {
		t.Fatal("this test is about a body with no node_id")
	}
	var out apiError
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	if code != http.StatusBadRequest {
		t.Fatalf("attach without a node_id answered %d, want 400: %s", code, raw)
	}
	if msg := out.Error.Message; !strings.Contains(msg, "node_id is required") ||
		!strings.Contains(msg, "roger share") {
		t.Errorf("the refusal must name the field and the command that fixes it, got %q (%s)", msg, raw)
	}

	// And nothing was recorded on the way out: a refusal that half-attaches is not a refusal.
	if ats, err := b.tower.stations.ByTower(tw.id); err == nil && len(ats) > 0 {
		t.Errorf("a refused attach left %d station(s) attached", len(ats))
	}
}

// The join is not satisfied by a node id that exists - it must be THIS caller's. A signed,
// otherwise-perfect attach naming somebody else's registration is the borrowed-reputation
// case, and it is refused with 403 rather than 400: the field is present and well-formed, the
// claim in it is not the caller's to make.
func TestAttachWithAnotherNodesIDIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	victim := signedInOperator(t, b, "victim-op")
	victimNode := registerShareNode(t, b, victim)

	thief := signedInOperator(t, b, "thief-op")
	body, _ := selfAttachBody(t)
	body["node_id"] = victimNode
	var out apiError
	code, raw := thief.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	if code != http.StatusForbidden {
		t.Fatalf("attach naming another node answered %d, want 403: %s", code, raw)
	}
	if msg := out.Error.Message; !strings.Contains(msg, "not registered to this key") {
		t.Errorf("the refusal must say whose key the node belongs to, got %q (%s)", msg, raw)
	}
}

// apiError is the broker's refusal envelope - {"error":{"message":...}} - so a test can
// assert on the sentence an operator actually reads rather than on a status code alone.
type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
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
