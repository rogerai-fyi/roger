package agent

// cancel_backoff_test.go: a cancel poll that keeps failing backs off (slice-6 audit 2026-10-06):
// while the broker cannot deliver cancels (a shared-store outage answers 503), every node polling
// at a fixed short interval would hot-loop the broker.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCancelPollBacksOffOnFailures(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "cancel delivery unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	prevRetry, prevBack := cancelRetry, cancelBackoff
	cancelRetry, cancelBackoff = 10*time.Millisecond, 160*time.Millisecond
	defer func() { cancelRetry, cancelBackoff = prevRetry, prevBack }()

	r := &reregistrar{token: "tok"}
	r.cond = sync.NewCond(&r.mu)
	sess := &Session{stop: make(chan struct{}), rereg: r}
	done := make(chan struct{})
	go func() { cancelLoop(Config{Broker: srv.URL, NodeID: "n1"}, sess); close(done) }()
	time.Sleep(time.Second)
	close(sess.stop)
	<-done
	// A fixed 10 ms retry makes ~100 calls in a second; doubling to a 160 ms cap makes ~10.
	require.Less(t, calls.Load(), int64(20), "the poll backs off while the broker keeps failing")
	require.Greater(t, calls.Load(), int64(2), "and keeps trying")
}
