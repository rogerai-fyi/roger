package towerhub

// THE SERVE LOOP'S SLOW BRANCHES, DRIVEN BY HAND.
//
// ServeLoop has three places where it waits on something other than the hub's answer: the
// backoff after a hard poll failure, the pacing floor after an EMPTY poll, and the report it
// makes when a result it served could not be handed back. Until this file the last of those
// (serve.go, the ErrResultUndelivered wrap) and the client branch beneath it (CompleteResult's
// transport-error return) were reached in ONE coverage run of ten - and only when some other
// test's context cancel happened to land while a worker was mid-/complete, which is the
// scheduler's decision, not a test's. The coverage gate measured this package at exactly its
// 90% floor nine times and 90.3% once on an identical tree.
//
// TestAServedResultThatCouldNotBeReturnedIsDistinguishable asserted the wrap by calling
// report() itself, which proves the error type composes and nothing about the loop. The tests
// here drive the loop. The seam is the one every Client already has: its http.Client's
// Transport, decorated over the REAL transport so the poll goes to the real hub and only the
// one request the test is about is interfered with. Nothing is faked and nothing is timed.

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

// errHubLinkCut is what the socket says when the hub went away between the poll and the
// completion - the reason a caller is owed inside ErrResultUndelivered.
var errHubLinkCut = errors.New("the link to the hub dropped")

// cutTransport passes every request to the real transport except the ones whose path it is
// told to cut, which fail before a byte is sent. It arranges "the hub was reachable for the
// poll and gone for the complete", which no amount of goroutine timing can arrange on purpose.
type cutTransport struct {
	inner http.RoundTripper
	cut   string
}

func (c *cutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == c.cut {
		return nil, errHubLinkCut
	}
	return c.inner.RoundTrip(req)
}

// A RESULT THE NODE SERVED AND COULD NOT RETURN IS REPORTED AS EXACTLY THAT: wrapped in
// ErrResultUndelivered, with the transport's own reason inside it, and the worker goes on
// polling rather than exiting. The GPU time was spent; the operator is owed the sentence.
func TestServeLoopNamesAServedResultTheHubWouldNotTake(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())

	node := id.client(base, 5*time.Second)
	node.HTTP.Transport = &cutTransport{inner: http.DefaultTransport, cut: PathComplete}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := make(chan error, 8)
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- ServeLoop(ctx, node, "st-1", echoExec{}, func(err error) {
			select {
			case reported <- err:
			default:
			}
		})
	}()

	// The consumer's submit is held by the hub until the node completes - which it never
	// will through a cut link - so it rides on the test's context and is released at the end.
	consumer := &Client{BaseURL: base}
	go func() { _, _ = consumer.SubmitJob(ctx, []byte("att-1|st-1"), []byte("sealed-req")) }()

	select {
	case err := <-reported:
		require.ErrorIs(t, err, ErrResultUndelivered,
			"a failed completion must be told apart from a failed poll: this one cost the node work")
		require.ErrorIs(t, err, errHubLinkCut, "the transport's own reason rides inside the wrapper")
	case <-time.After(5 * time.Second):
		t.Fatal("the worker served a job and never reported that it could not return the result")
	}

	// The worker is still a worker: cancelling it is what ends it, not the failed return.
	cancel()
	select {
	case err := <-loopDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeLoop did not return after cancel")
	}
}

// emptyPollClock stamps every EMPTY answer the hub gives (a 204, which is the only kind a
// fresh client's first poll does not produce - that one is the 401 it learns the epoch from)
// and runs a hook inside the client's window, after the answer arrived and before the loop
// sees it.
type emptyPollClock struct {
	inner   http.RoundTripper
	mu      sync.Mutex
	empties []time.Time
	onEmpty func(n int)
}

func (p *emptyPollClock) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := p.inner.RoundTrip(req)
	if err != nil || req.URL.Path != PathPoll || resp.StatusCode != http.StatusNoContent {
		return resp, err
	}
	p.mu.Lock()
	p.empties = append(p.empties, time.Now())
	n := len(p.empties)
	p.mu.Unlock()
	if p.onEmpty != nil {
		p.onEmpty(n)
	}
	return resp, nil
}

func (p *emptyPollClock) stamps() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.empties...)
}

// fastEmptyHub is a hub whose long poll gives up almost at once - the "misbehaving tower that
// returns 204 immediately" the pacing floor exists for.
func fastEmptyHub(t *testing.T) (*Server, string) {
	t.Helper()
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		SubmitTTL: 3 * time.Second, PollTTL: time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(PathPoll, s.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

// pacer is the test's hand on ServeLoop's empty-poll pacing: it records every wait the loop
// asks for and answers each at once. The assertion is then on the loop's own arithmetic - the
// remainder of the floor it computed from its own start stamp - rather than on wall-clock
// arrival times at a transport, which a slow first poll (the 401 learn and re-sign) on a loaded
// runner could squeeze under any slack.
type pacer struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (p *pacer) after(d time.Duration) <-chan time.Time {
	p.mu.Lock()
	p.waits = append(p.waits, d)
	p.mu.Unlock()
	fired := make(chan time.Time, 1)
	fired <- time.Time{}
	return fired
}

func (p *pacer) recorded() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Duration(nil), p.waits...)
}

// A HUB THAT ANSWERS EMPTY AT ONCE CANNOT SPIN THE WORKER: after every empty answer the loop
// asks its pacer to wait out the REMAINDER of emptyPollFloor - more than nothing, because the
// poll took time, and less than the whole floor, because that time is subtracted. A worker that
// skipped the wait, or waited a fixed floor regardless, fails here without a clock in sight.
func TestAnEmptyPollIsPacedAtTheFloor(t *testing.T) {
	id := newTestNode(t)
	s, base := fastEmptyHub(t)
	s.RegisterNode("st-1", id.auth())
	p := &pacer{}
	prev := emptyPollPace
	emptyPollPace = p.after
	clock := &emptyPollClock{inner: http.DefaultTransport}
	node := id.client(base, 5*time.Second)
	node.HTTP.Transport = clock
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()
	defer func() {
		// The real pacer comes back only once the loop has exited, so nothing reads the
		// variable while it is written.
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("ServeLoop did not return after cancel")
		}
		emptyPollPace = prev
	}()

	require.Eventually(t, func() bool { return len(clock.stamps()) >= 3 },
		5*time.Second, 5*time.Millisecond, "the worker stopped polling an empty hub")
	waits := p.recorded()
	require.GreaterOrEqual(t, len(waits), 2, "every empty answer must be followed by a pacing wait")
	for i, w := range waits {
		require.Greater(t, w, time.Duration(0), "wait %d: the loop must pace an empty poll", i)
		require.Less(t, w, emptyPollFloor, "wait %d: the poll's own duration is subtracted from the floor", i)
	}
}

// A CANCEL THAT LANDS DURING THE FLOOR WAIT ENDS THE WORKER THERE. The hook cancels inside the
// first empty answer's window, so when the loop reaches the floor the context is already done
// and the floor timer has not started: the only ready case is the cancel. No second poll is
// ever made.
func TestACancelDuringTheFloorWaitEndsTheWorker(t *testing.T) {
	id := newTestNode(t)
	s, base := fastEmptyHub(t)
	s.RegisterNode("st-1", id.auth())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &emptyPollClock{inner: http.DefaultTransport, onEmpty: func(int) { cancel() }}
	node := id.client(base, 5*time.Second)
	node.HTTP.Transport = clock

	done := make(chan error, 1)
	go func() { done <- ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeLoop did not return after a cancel during its floor wait")
	}
	require.Len(t, clock.stamps(), 1, "a worker cancelled in its floor wait must not poll again")
}
