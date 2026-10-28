package agent

import (
	"testing"

	"rogerai.fm/roger/v7/internal/coreclient"
	"rogerai.fm/roger/v7/internal/protocol"
)

// TestMarketRegistrationMarksItself pins features/packaging/one_binary.feature section 4: a
// market share registering with the broker is a Core client, so a standalone Tower's serve
// refuses to start beside one.
func TestMarketRegistrationMarksItself(t *testing.T) {
	coreclient.Reset()
	defer coreclient.Reset()
	_, _ = register("http://127.0.0.1:0", protocol.NodeRegistration{NodeID: "n"})
	if len(coreclient.Noted()) == 0 {
		t.Fatal("registering with the broker recorded no Core client")
	}
}
