package main

// The grace timer (lw.commit) and a bridged attempt's keepalive commit the SSE headers on their own
// goroutines while the relay is still setting attempt headers on the same map. Under `go test -race`
// (make test) the detector flagged it in four BDD suites; outside the detector two writers on one
// map can end the broker with "concurrent map writes". Every header edit goes through the writer's
// lock, and a stopped keepalive has finished before the relay touches the headers again.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLazySSEHeaderEditsAndTheGraceCommitNeverRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		lw := &lazySSE{w: rec, flusher: rec}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); lw.commit() }() // the grace timer firing
		lw.editHeader(func(h http.Header) {
			h.Set("X-RogerAI-Attempt-Id", "a1")
			h.Set("X-RogerAI-Price", "in=0;out=0;locked_until=0")
			h.Del("X-RogerAI-Relay")
		})
		wg.Wait()
	}
}

func TestAStoppedKeepaliveHasFinishedWritingHeaders(t *testing.T) {
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		lw := &lazySSE{w: rec, flusher: rec}
		stop := lw.keepalive(time.Microsecond)
		time.Sleep(50 * time.Microsecond)
		stop()
		// After stop returns the keepalive goroutine is gone: an unlocked read of the recorder is safe.
		_ = rec.Header().Get("Content-Type")
		require.NotPanics(t, func() { stop() }, "stop is idempotent")
	}
}
