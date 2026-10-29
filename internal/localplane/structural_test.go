package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The no-egress source-scan gate. The consumer handler must make no outbound call: it reads a
// request and writes a reply, and the listener that hands it connections lives in
// internal/localplane/serve, not here. A runtime check could be bypassed; reading the source cannot - if
// any non-test file in this package acquires the ability to dial, listen, resolve, or exec,
// this fails, and a new outbound path needs its own approved spec first.

import (
	"testing"

	"rogerai.fm/roger/v7/internal/structural"
)

func TestConsumerHandlerMakesNoOutboundCall(t *testing.T) {
	// No dial, resolve or exec, and no listener either: the listener belongs to
	// internal/localplane/serve.
	if n := structural.ScanSource(t, ".", structural.NoOutbound, structural.NoListen); n < 2 {
		t.Fatalf("the scan read %d files; it must actually have read the package", n)
	}
}
