package main

// single_delivery_dispatch_test.go enforces features/multinode/single_delivery_dispatch.feature
// (audit BLOCKER #1): a job dispatched to a node in multi-instance mode must be served by
// EXACTLY ONE of the node's pollers. Production runs each node with Parallel=4 poll loops, and
// each multi-instance poller opens its own Redis SUBSCRIBE on the node's bus channel; the
// dispatcher PUBLISHes (fan-out), so today one job is delivered to all 4 pollers -> 4x serve
// (and, for streams, 4 interleaved copies). Real miniredis bus, no mocks. RED before the
// single-delivery claim is added to agentPoll.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func TestMultiInstanceSingleDeliveryDispatch(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, brokerPriv, store.NewMem(), mr)

	nodePub, _, _ := ed25519.GenerateKey(nil)
	const token = "tok-n1"
	miRegisterNode(b, "n1", hex.EncodeToString(nodePub), token,
		[]protocol.ModelOffer{{Model: "m", Modality: protocol.ModalityChat}})

	// A node runs Parallel=4 concurrent poll loops; each opens its own bus subscription.
	const pollers = 4
	got := make(chan bool, pollers)
	var wg sync.WaitGroup
	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := pollOnce(t, b, "n1", token)
			got <- ok
		}()
	}
	// Let all pollers open their SUBSCRIBE before the dispatch PUBLISH (else a late poller
	// misses the message and blocks the full poll window).
	time.Sleep(200 * time.Millisecond)

	resCh, cancel, err := b.busDispatchJob(context.Background(), "n1", protocol.Job{ID: "job-1", Body: []byte(`{}`)})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	_ = resCh
	defer cancel()

	wg.Wait()
	close(got)
	served := 0
	for ok := range got {
		if ok {
			served++
		}
	}
	if served != 1 {
		t.Fatalf("one dispatched job was delivered to %d of the node's %d pollers, want exactly 1 (fan-out duplicate serve)", served, pollers)
	}
}
