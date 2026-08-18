package towerhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// echoExec is a stand-in serving node: it "serves" by prefixing the sealed request and returns a
// fixed receipt. It never inspects content meaningfully - it is the seam a real station.Executor
// fills. A grant/envelope containing "fail" returns a failure with no receipt.
type echoExec struct{}

func (echoExec) Serve(_ context.Context, grant, envelope []byte) ([]byte, []byte, string) {
	if strings.Contains(string(envelope), "fail") {
		return nil, nil, "the model did not answer"
	}
	return append([]byte("served:"), envelope...), []byte("receipt:" + string(grant)), ""
}

func serveTestRig(t *testing.T) (*Server, string) {
	t.Helper()
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID,
		SubmitTTL: 3 * time.Second, PollTTL: 300 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(PathSubmit, s.Submit)
	mux.HandleFunc(PathPoll, s.Poll)
	mux.HandleFunc(PathComplete, s.Complete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

// The whole Topology-2 data path over HTTP, end to end: a node worker (ServeLoop) polls the
// tower and serves; a consumer submits a sealed job and gets the sealed result + receipt back.
func TestServeLoopServesAConsumerSubmissionEndToEnd(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node := id.client(base, 5*time.Second)
	go func() { _ = ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()

	consumer := &Client{BaseURL: base}
	sctx, scancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer scancel()
	res, err := consumer.SubmitJob(sctx, []byte("att-1|st-1"), []byte("sealed-req"))
	require.NoError(t, err)
	require.Equal(t, []byte("served:sealed-req"), res.Envelope, "the node served the consumer's sealed request")
	require.Equal(t, []byte("receipt:att-1|st-1"), res.Receipt, "the node's receipt came back to the consumer")
}

// A node's serving failure comes back to the consumer as a failure with no receipt - a failure
// must never carry a settleable receipt.
func TestServeLoopPropagatesAServingFailure(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node := id.client(base, 5*time.Second)
	go func() { _ = ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()

	consumer := &Client{BaseURL: base}
	sctx, scancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer scancel()
	res, err := consumer.SubmitJob(sctx, []byte("att-1|st-1"), []byte("please fail"))
	require.NoError(t, err)
	require.Empty(t, res.Receipt, "a failed serve carries no receipt")
	require.Equal(t, "the model did not answer", res.Failure)
}

// The consumer's SubmitJob surfaces a non-2xx as an HTTPError with its status, so a caller can
// branch (e.g. 403 unauthorized grant, 404 unserved Station).
func TestSubmitJobSurfacesHTTPStatus(t *testing.T) {
	_, base := serveTestRig(t)
	consumer := &Client{BaseURL: base}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := consumer.SubmitJob(ctx, []byte("bad-grant"), []byte("x"))
	var he *HTTPError
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusForbidden, he.Status)

	_, err = consumer.SubmitJob(ctx, []byte("att-1|st-unserved"), []byte("x"))
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusNotFound, he.Status)
}

// PollJob returns ok=false (not an error) on an idle long-poll, so a node worker just loops.
func TestPollJobReturnsFalseWhenIdle(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())
	node := id.client(base, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, ok, err := node.PollJob(ctx, "st-1")
	require.NoError(t, err)
	require.False(t, ok, "an idle long poll is ok=false, nil error")
}

// A node signing with a key the Station is not attached with gets an auth error from PollJob
// (not a silent empty), so a re-keyed or revoked node learns it is no longer serving.
func TestPollJobWithTheWrongKeyErrors(t *testing.T) {
	attached, stranger := newTestNode(t), newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", attached.auth())
	node := stranger.client(base, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, ok, err := node.PollJob(ctx, "st-1")
	require.False(t, ok)
	var he *HTTPError
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)
}

// ServeLoop returns when its context is cancelled.
func TestServeLoopStopsOnContextCancel(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())
	node := id.client(base, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ServeLoop did not return after context cancel")
	}
}

// hostileFailExec returns a receipt EVEN ON FAILURE - a buggy/hostile node. ServeLoop must strip
// the receipt so a failure can never carry something settleable to the consumer.
type hostileFailExec struct{}

func (hostileFailExec) Serve(_ context.Context, _, _ []byte) ([]byte, []byte, string) {
	return nil, []byte("sneaky-settleable-receipt"), "the model failed"
}

func TestServeLoopStripsTheReceiptOnFailure(t *testing.T) {
	id := newTestNode(t)
	s, base := serveTestRig(t)
	s.RegisterNode("st-1", id.auth())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node := id.client(base, 5*time.Second)
	go func() { _ = ServeLoop(ctx, node, "st-1", hostileFailExec{}, nil) }()

	consumer := &Client{BaseURL: base}
	sctx, sc := context.WithTimeout(context.Background(), 4*time.Second)
	defer sc()
	res, err := consumer.SubmitJob(sctx, []byte("att-1|st-1"), []byte("x"))
	require.NoError(t, err)
	require.Equal(t, "the model failed", res.Failure)
	require.Empty(t, res.Receipt, "a failure's receipt is stripped even if the executor returned one")
}

// A hard poll error (tower unreachable) is reported via onError and the worker backs off rather
// than tight-spinning or exiting.
func TestServeLoopReportsHardErrors(t *testing.T) {
	// A port nothing listens on: PollJob errors immediately.
	node := newTestNode(t).client("http://127.0.0.1:1", 150*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var count int32
	got := make(chan struct{}, 1)
	go func() {
		_ = ServeLoop(ctx, node, "st-1", echoExec{}, func(error) {
			atomic.AddInt32(&count, 1)
			select {
			case got <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("onError was never invoked on a hard poll failure")
	}
	cancel()
	require.GreaterOrEqual(t, atomic.LoadInt32(&count), int32(1))
}
