package agent

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// swapHeartbeatInterval lowers the heartbeat cadence for a test and returns a restore
// func; production uses ~10s, but the tests need fast beats to observe transitions.
func swapHeartbeatInterval(d time.Duration) func() {
	prev := heartbeatInterval.Load()
	heartbeatInterval.Store(int64(d))
	return func() { heartbeatInterval.Store(prev) }
}

// waitLink polls the session's link state until it reaches want or times out.
func waitLink(t *testing.T, s *Session, want LinkState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Link() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("link state = %v, want %v", s.Link(), want)
}

// TestHeartbeatDrivesTruthfulLink verifies the provider's on-air status is TRUTHFUL:
// it flips to ON AIR only while the broker accepts heartbeats (200), to RECONNECTING
// when the broker rejects them (401), and back to ON AIR when accepted again. The
// status reflects the BROKER actually acknowledging the node, never a blind on-air.
func TestHeartbeatDrivesTruthfulLink(t *testing.T) {
	var reject atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/heartbeat":
			if reject.Load() {
				w.WriteHeader(http.StatusUnauthorized) // broker not acknowledging
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/nodes/register":
			w.WriteHeader(http.StatusOK) // a re-register attempt during reject also fails to clear it
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	defer swapHeartbeatInterval(20 * time.Millisecond)()

	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok"}
	rr := newReregistrar(srv.URL, reg, priv)
	sess := &Session{stop: make(chan struct{})}
	sess.setLink(LinkConnecting)
	go heartbeatUntil(srv.URL, "n1", rr, sess)
	defer close(sess.stop)

	// Accepted heartbeats -> genuinely ON AIR.
	waitLink(t, sess, LinkOnAir)

	// Broker starts rejecting (e.g. forgot us after a restart) -> RECONNECTING, never
	// a false ON AIR while customers can't reach us.
	reject.Store(true)
	waitLink(t, sess, LinkReconnecting)

	// Broker accepts again -> back to ON AIR.
	reject.Store(false)
	waitLink(t, sess, LinkOnAir)
}

// TestHeartbeatUnreachableIsReconnecting: an unreachable broker (no server) must show
// RECONNECTING, not on-air - the operator never sees on-air when they are not routable.
func TestHeartbeatUnreachableIsReconnecting(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok"}
	// A closed port: every heartbeat errors at the transport layer.
	const dead = "http://127.0.0.1:1"
	rr := newReregistrar(dead, reg, priv)
	sess := &Session{stop: make(chan struct{})}
	sess.setLink(LinkConnecting)
	go heartbeatUntil(dead, "n1", rr, sess)
	defer close(sess.stop)
	waitLink(t, sess, LinkReconnecting)
}
