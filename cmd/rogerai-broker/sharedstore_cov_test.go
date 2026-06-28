package main

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// sharedErrWanted fails when err is nil — used by the downed-backend battery, where every
// valkeyStore op must surface a backend failure (so the broker call site falls back to its
// in-memory path) rather than panic or silently succeed.
func sharedErrWanted(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s against a dead backend should return an error", op)
	}
}

// TestValkeyDownedBackendErrorsExtra extends TestValkeyGracefulDegradeOnClose to the
// cache/counter/registry/inflight/bus methods: with the backend gone, each must error and
// the store must report unhealthy. Covers the noteErr error branch of each method.
func TestValkeyDownedBackendErrorsExtra(t *testing.T) {
	vs, mr := newTestValkey(t)
	mr.Close() // backend gone

	sharedErrWanted(t, "cacheSet", vs.cacheSet("k", []byte("v"), time.Second))
	if _, _, err := vs.cacheGet("k"); err == nil {
		t.Error("cacheGet against a dead backend should error")
	}
	sharedErrWanted(t, "cacheDel", vs.cacheDel("k"))

	if _, _, err := vs.counterGet("c"); err == nil {
		t.Error("counterGet against a dead backend should error")
	}
	sharedErrWanted(t, "counterSet", vs.counterSet("c", 1, time.Second))
	if _, err := vs.counterIncr("c", 1); err == nil {
		t.Error("counterIncr against a dead backend should error")
	}
	if _, err := vs.setIfAbsent("c", "1", time.Second); err == nil {
		t.Error("setIfAbsent against a dead backend should error")
	}

	sharedErrWanted(t, "putNode", vs.putNode("n", []byte("{}"), time.Second))
	if _, _, err := vs.getNode("n"); err == nil {
		t.Error("getNode against a dead backend should error")
	}
	if _, err := vs.allNodes(); err == nil {
		t.Error("allNodes against a dead backend should error")
	}

	sharedErrWanted(t, "markInflight", vs.markInflight("inst", "n", 3, time.Now()))
	if _, err := vs.inflightByNode("inst"); err == nil {
		t.Error("inflightByNode against a dead backend should error")
	}

	if _, err := vs.busPublishJob("n", []byte("j")); err == nil {
		t.Error("busPublishJob against a dead backend should error")
	}
	sharedErrWanted(t, "busPublishResult", vs.busPublishResult("j", []byte("r")))
	sharedErrWanted(t, "busPublishStreamChunk", vs.busPublishStreamChunk("j", []byte("c")))
	sharedErrWanted(t, "busPublishStreamDone", vs.busPublishStreamDone("j"))

	ctx := context.Background()
	if _, _, err := vs.busSubscribeJobs(ctx, "n"); err == nil {
		t.Error("busSubscribeJobs against a dead backend should error")
	}
	if _, _, err := vs.busSubscribeResult(ctx, "j"); err == nil {
		t.Error("busSubscribeResult against a dead backend should error")
	}
	if _, _, err := vs.busSubscribeStream(ctx, "j"); err == nil {
		t.Error("busSubscribeStream against a dead backend should error")
	}

	if vs.healthy() {
		t.Error("store should report unhealthy after a wave of backend failures")
	}
}

// TestValkeyCounterRoundTrip exercises the numeric fast-path counters end-to-end on a live
// backend: set, get (hit), incr, and the SETNX single-winner.
func TestValkeyCounterRoundTrip(t *testing.T) {
	vs, _ := newTestValkey(t)
	if err := vs.counterSet("spend", 12.5, time.Minute); err != nil {
		t.Fatalf("counterSet: %v", err)
	}
	got, found, err := vs.counterGet("spend")
	if err != nil || !found || got != 12.5 {
		t.Fatalf("counterGet = (%v, found=%v, %v), want (12.5,true,nil)", got, found, err)
	}
	if v, err := vs.counterIncr("spend", 2.5); err != nil || v != 15 {
		t.Fatalf("counterIncr = (%v, %v), want (15,nil)", v, err)
	}
	if set, err := vs.setIfAbsent("once", "a", time.Minute); err != nil || !set {
		t.Fatalf("setIfAbsent(first) = (%v,%v), want (true,nil)", set, err)
	}
	if set, err := vs.setIfAbsent("once", "b", time.Minute); err != nil || set {
		t.Fatalf("setIfAbsent(second) = (%v,%v), want (false,nil) — key already present", set, err)
	}
	// A clean miss on an unset counter is (0,false,nil) so the caller reconciles from PG.
	if v, found, err := vs.counterGet("never"); err != nil || found || v != 0 {
		t.Fatalf("counterGet(miss) = (%v,found=%v,%v), want (0,false,nil)", v, found, err)
	}
}

// TestValkeyNodeRegistryRoundTrip exercises the cross-instance registry mirror: putNode,
// getNode (hit + miss), and allNodes.
func TestValkeyNodeRegistryRoundTrip(t *testing.T) {
	vs, _ := newTestValkey(t)
	if err := vs.putNode("node-a", []byte(`{"node_id":"node-a"}`), time.Minute); err != nil {
		t.Fatalf("putNode: %v", err)
	}
	raw, ok, err := vs.getNode("node-a")
	if err != nil || !ok || string(raw) != `{"node_id":"node-a"}` {
		t.Fatalf("getNode(node-a) = (%q, ok=%v, %v), want the stored reg", raw, ok, err)
	}
	if _, ok, err := vs.getNode("ghost"); err != nil || ok {
		t.Fatalf("getNode(ghost) = (ok=%v, %v), want (false,nil) miss", ok, err)
	}
	all, err := vs.allNodes()
	if err != nil {
		t.Fatalf("allNodes: %v", err)
	}
	if string(all["node-a"]) != `{"node_id":"node-a"}` {
		t.Fatalf("allNodes missing node-a: %v", all)
	}
}

// TestValkeyInflightRoundTrip exercises the cross-instance inflight write-through + merge:
// a peer's reported count is summed, but the caller's OWN instance id is excluded (the
// caller adds its exact local count separately).
func TestValkeyInflightRoundTrip(t *testing.T) {
	vs, _ := newTestValkey(t)
	now := time.Now()
	if err := vs.markInflight("inst-a", "node-x", 5, now); err != nil {
		t.Fatalf("markInflight: %v", err)
	}
	// From inst-b's view, inst-a's 5 counts.
	peer, err := vs.inflightByNode("inst-b")
	if err != nil {
		t.Fatalf("inflightByNode: %v", err)
	}
	if peer["node-x"] != 5 {
		t.Fatalf("inflightByNode(inst-b)[node-x] = %d, want 5 (peer count)", peer["node-x"])
	}
	// From inst-a's own view, its own field is excluded -> no peer load.
	self, err := vs.inflightByNode("inst-a")
	if err != nil {
		t.Fatalf("inflightByNode(self): %v", err)
	}
	if _, present := self["node-x"]; present {
		t.Fatalf("inflightByNode(inst-a) must exclude self, got %v", self)
	}
}

// TestValkeyBusJobResultRoundTrip publishes a job/result through the rendezvous bus and
// receives it on a live subscription (the cross-instance dispatch path).
func TestValkeyBusJobResultRoundTrip(t *testing.T) {
	vs, _ := newTestValkey(t)
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	jobs, cancelJobs, err := vs.busSubscribeJobs(ctx, "node-a")
	if err != nil {
		t.Fatalf("busSubscribeJobs: %v", err)
	}
	defer cancelJobs()
	if n, err := vs.busPublishJob("node-a", []byte("hello-job")); err != nil || n < 1 {
		t.Fatalf("busPublishJob = (%d,%v), want >=1 subscriber, nil", n, err)
	}
	select {
	case msg := <-jobs:
		if string(msg) != "hello-job" {
			t.Fatalf("job payload = %q, want hello-job", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the published job")
	}

	res, cancelRes, err := vs.busSubscribeResult(ctx, "job-1")
	if err != nil {
		t.Fatalf("busSubscribeResult: %v", err)
	}
	defer cancelRes()
	if err := vs.busPublishResult("job-1", []byte("the-answer")); err != nil {
		t.Fatalf("busPublishResult: %v", err)
	}
	select {
	case msg := <-res:
		if string(msg) != "the-answer" {
			t.Fatalf("result payload = %q, want the-answer", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the published result")
	}
}

// TestValkeyBusStreamRoundTrip streams chunks then the done sentinel through the bus and
// confirms the subscriber decodes a payload frame followed by an isDone frame.
func TestValkeyBusStreamRoundTrip(t *testing.T) {
	vs, _ := newTestValkey(t)
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	frames, cancel, err := vs.busSubscribeStream(ctx, "job-s")
	if err != nil {
		t.Fatalf("busSubscribeStream: %v", err)
	}
	defer cancel()
	if err := vs.busPublishStreamChunk("job-s", []byte("chunk-1")); err != nil {
		t.Fatalf("busPublishStreamChunk: %v", err)
	}
	if err := vs.busPublishStreamDone("job-s"); err != nil {
		t.Fatalf("busPublishStreamDone: %v", err)
	}

	first := recvFrame(t, frames)
	if first.isDone || string(first.payload) != "chunk-1" {
		t.Fatalf("first frame = %+v, want payload chunk-1", first)
	}
	last := recvFrame(t, frames)
	if !last.isDone {
		t.Fatalf("second frame = %+v, want the done sentinel", last)
	}
}

func recvFrame(t *testing.T, ch <-chan streamFrame) streamFrame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a stream frame")
		return streamFrame{}
	}
}

// TestCacheTTLJitter: a non-positive TTL is returned unchanged (no jitter); a positive TTL
// is only ever lengthened, and stays within the +0..15% jitter band.
func TestCacheTTLJitter(t *testing.T) {
	if got := cacheTTLJitter(0); got != 0 {
		t.Errorf("cacheTTLJitter(0) = %v, want 0", got)
	}
	if got := cacheTTLJitter(-5 * time.Second); got != -5*time.Second {
		t.Errorf("cacheTTLJitter(negative) = %v, want unchanged", got)
	}
	base := 10 * time.Second
	for i := 0; i < 50; i++ {
		got := cacheTTLJitter(base)
		if got < base || got > base+base*15/100 {
			t.Fatalf("cacheTTLJitter(%v) = %v, want within [base, base+15%%]", base, got)
		}
	}
}

// TestValkeyCloseNilSafe: Close on a nil/uninitialized valkeyStore is a safe no-op.
func TestValkeyCloseNilSafe(t *testing.T) {
	var v *valkeyStore
	if err := v.Close(); err != nil {
		t.Errorf("nil valkeyStore Close = %v, want nil", err)
	}
	if err := (&valkeyStore{}).Close(); err != nil {
		t.Errorf("empty valkeyStore Close = %v, want nil", err)
	}
}

// TestNoteErrNonErrorMarksHealthy: noteErr with a nil or redis.Nil error is treated as a
// success signal (the backend answered) — it flips the store back to healthy.
func TestNoteErrNonErrorMarksHealthy(t *testing.T) {
	vs, mr := newTestValkey(t)
	mr.Close()
	_ = vs.cacheDel("x") // force an error -> unhealthy
	if vs.healthy() {
		t.Fatal("precondition: store should be unhealthy after a backend error")
	}
	vs.noteErr("probe", nil) // a clean answer
	if !vs.healthy() {
		t.Error("noteErr(nil) should mark the store healthy again")
	}
	vs.noteErr("probe", redis.Nil) // a clean miss is not a failure
	if !vs.healthy() {
		t.Error("noteErr(redis.Nil) should keep the store healthy")
	}
}
