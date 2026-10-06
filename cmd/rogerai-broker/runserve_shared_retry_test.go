package main

import (
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunServeStopsTheSharedRetryOnShutdown (audit fix 2026-10-04): a broker that is not ready
// because its configured shared store is unreachable keeps retrying the connection; when the
// broker shuts down, that retry loop stops (stopSharedRetry), so it never outlives the server.
func TestRunServeStopsTheSharedRetryOnShutdown(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := dead.Addr().String()
	_ = dead.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+addr)
	t.Setenv("ROGERAI_MULTI_INSTANCE", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("BROKER_PRIVATE_KEY", "")
	t.Setenv("ROGERAI_REQUIRE_BROKER_KEY", "")
	t.Setenv("ROGERAI_PROBE_INTERVAL", "0")

	var tries atomic.Int64
	saved := sharedRetrySleep
	sharedRetrySleep = func(time.Duration) { tries.Add(1); time.Sleep(5 * time.Millisecond) }
	t.Cleanup(func() { sharedRetrySleep = saved })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runServe(ln, 0.30, 100, time.Hour, stop) }()

	base := "http://" + ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, gerr := http.Get(base + "/health")
		if gerr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", gerr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for tries.Load() < 3 { // the retry loop is running
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after stop")
	}
	time.Sleep(3 * time.Second) // let a dial already in flight finish (a failed dial retries for ~1-2 s)
	after := tries.Load()
	time.Sleep(4 * time.Second)
	if got := tries.Load(); got != after {
		t.Fatalf("the shared-store retry kept running after shutdown (%d -> %d attempts)", after, got)
	}
}
