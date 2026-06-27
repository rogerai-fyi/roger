package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRunServe covers main()'s extracted glue: it binds a :0 listener, lets runServe wire
// the env-overridden fee/seed/seed-limit, the in-memory store, the broker, the routes and
// the background sweeps, serves a real request over the listener, then closes stop to
// trigger a clean shutdown (Serve returns http.ErrServerClosed). The env overrides exercise
// the ROGERAI_FEE / ROGERAI_SEED_CREDITS / ROGERAI_SEED_LIMIT parse branches.
func TestRunServe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	t.Setenv("ROGERAI_MULTI_INSTANCE", "")
	t.Setenv("DATABASE_URL", "")       // in-memory store branch
	t.Setenv("BROKER_PRIVATE_KEY", "") // ephemeral key (REQUIRE not set -> no fatal)
	t.Setenv("ROGERAI_REQUIRE_BROKER_KEY", "")
	t.Setenv("ROGERAI_FEE", "0.25")         // override parse branch
	t.Setenv("ROGERAI_SEED_CREDITS", "42")  // override parse branch
	t.Setenv("ROGERAI_SEED_LIMIT", "7")     // override parse branch
	t.Setenv("ROGERAI_PROBE_INTERVAL", "0") // keep the prober daemon off in the test

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	stop := make(chan struct{})
	serveErr := make(chan error, 1)
	go func() { serveErr <- runServe(ln, 0.30, 100, time.Hour, stop) }()

	// Poll /health over the real listener until the server is up.
	base := "http://" + ln.Addr().String()
	var body string
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, gerr := http.Get(base + "/health")
		if gerr == nil {
			bb, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = strings.TrimSpace(string(bb))
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up on %s (last err=%v)", base, gerr)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if body != "ok" {
		t.Fatalf("/health = %q, want ok", body)
	}

	close(stop) // triggers srv.Close()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("runServe returned %v, want nil or ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServe did not return after stop was closed")
	}
}
