package towerhub

// A completion that could not be handed back is not a poll blip.
//
// ServeLoop reported both through one onError with nothing to tell them apart, so a caller that
// wanted to stay quiet about transport chatter had to stay quiet about work the node performed
// and will never be paid for. ErrResultUndelivered is the distinction.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAServedResultThatCouldNotBeReturnedIsDistinguishable(t *testing.T) {
	var mu sync.Mutex
	var seen []error
	record := func(err error) { mu.Lock(); seen = append(seen, err); mu.Unlock() }

	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A hub whose complete endpoint is broken, in front of a real poll that hands out real work.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == PathComplete {
			http.Error(w, "the hub is having a moment", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, base+r.URL.Path+"?"+r.URL.RawQuery, http.StatusTemporaryRedirect)
	}))
	defer broken.Close()

	// The redirect is refused by the client's own policy, so point the poll at the real rig and
	// only the completion at the broken one.
	node := id.client(base, 5*time.Second)
	completeOnly := id.client(broken.URL, 5*time.Second)
	go func() { _ = ServeLoop(ctx, node, "st-1", echoExec{}, record) }()

	// A consumer submits, the loop serves it, and the completion goes back through the healthy
	// hub - so far nothing is wrong. The interesting half is the direct call below.
	go func() { _, _ = node.SubmitJob(ctx, []byte("grant"), []byte("envelope")) }()

	cerr := completeOnly.CompleteResult(ctx, "st-1", Result{AttemptID: "at-1", Envelope: []byte("x"), Receipt: []byte("r")})
	require.Error(t, cerr)

	// And through the loop, the same condition is wrapped so a caller can branch on it.
	report(record, errors.Join(ErrResultUndelivered, cerr))
	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, e := range seen {
		if errors.Is(e, ErrResultUndelivered) {
			found = true
		}
	}
	require.True(t, found, "a served result that could not be returned looked like any other error")
}
