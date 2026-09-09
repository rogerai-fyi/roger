package client

import (
	"testing"
	"time"
)

// TestProxyResponseHeaderTimeoutPinned: the broker's streaming relay holds its SSE headers
// back for at most streamCommitGrace (cmd/rogerai-broker/cooling.go, 20s) while it waits for
// a first upstream verdict; that grace is sized against THIS timeout. Changing either side
// without the other strands slow-first-token streams at the local proxy.
func TestProxyResponseHeaderTimeoutPinned(t *testing.T) {
	if proxyResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("proxyResponseHeaderTimeout = %s; the broker's streamCommitGrace (20s) is sized against 30s - move both together", proxyResponseHeaderTimeout)
	}
}
