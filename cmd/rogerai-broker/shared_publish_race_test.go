package main

// shared_publish_race_test.go - the shared store is wired by the boot retry goroutine
// (bootShared -> retryShared -> wireShared) while the background loops runServe starts are
// already running. Those loops must never read the broker's shared-store state while the
// retry goroutine is still writing it. Run under -race: a loop that reads b.shared with no
// happens-before edge to wireShared's write is a data race the detector reports.

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"rogerai.fm/roger/v6/internal/store"
)

func TestSharedStoreIsPublishedSafelyToLoopsStartedBeforeReadiness(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // reserve an address nothing answers on
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+addr)
	saved := sharedRetrySleep
	sharedRetrySleep = func(time.Duration) { time.Sleep(time.Millisecond) }
	t.Cleanup(func() { sharedRetrySleep = saved })

	b := relayBroker(store.NewMem())
	asMainBuilds(b) // no shared store yet + the production limiters wireShared attaches to
	if b.grantRL == nil {
		b.grantRL = loadRateLimiter() // buildBroker always has one
	}
	b.bootShared()
	t.Cleanup(b.stopSharedRetry)
	if b.sharedPhase.Load() != sharedConnecting {
		t.Fatalf("phase %d, want connecting", b.sharedPhase.Load())
	}

	// The background loops runServe starts after buildBroker returns, before readiness.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b.alertCheckOnce(time.Now()) // the alert checker reads the shared store on every tick
		}
	}()

	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	deadline := time.Now().Add(10 * time.Second)
	for b.sharedPhase.Load() != sharedConnected {
		if time.Now().After(deadline) {
			t.Fatal("the retry never connected")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the loop run against the wired state
	close(stop)
	wg.Wait()
}
