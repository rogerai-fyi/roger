package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newBenchValkey spins an in-process miniredis + wired valkeyStore for a benchmark
// (miniredis.RunT needs *testing.T, so a benchmark uses the manual Run + Cleanup form).
func newBenchValkey(b *testing.B) (*valkeyStore, *miniredis.Miniredis) {
	b.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("miniredis: %v", err)
	}
	b.Cleanup(mr.Close)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		b.Fatalf("newValkeyStore: %v", err)
	}
	b.Cleanup(func() { _ = vs.Close() })
	return vs, mr
}

// sharedstore_bench_test.go benchmarks the background SYNC-LOOP reads that fan out one
// Valkey command PER NODE (liveness/allNodes/inflightByNode). At higher node counts these
// were O(N) sequential round-trips; the pipelined versions issue ONE round-trip. Against
// in-process miniredis the per-call latency floor is tiny (no network), so the headline
// win here is the reduction in protocol round-trips - in production each saved round-trip
// is a full Valkey RTT, so the real-world speedup scales with N x RTT.

const benchNodes = 200

func benchSeedLiveness(b *testing.B, vs *valkeyStore, n int) {
	b.Helper()
	now := time.Now()
	for i := 0; i < n; i++ {
		if err := vs.markSeen("node-"+strconv.Itoa(i), now); err != nil {
			b.Fatalf("markSeen: %v", err)
		}
	}
}

func BenchmarkLiveness(b *testing.B) {
	vs, _ := newBenchValkey(b)
	benchSeedLiveness(b, vs, benchNodes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, err := vs.liveness()
		if err != nil || len(snap) != benchNodes {
			b.Fatalf("liveness: got %d nodes err %v", len(snap), err)
		}
	}
}

func BenchmarkAllNodes(b *testing.B) {
	vs, _ := newBenchValkey(b)
	for i := 0; i < benchNodes; i++ {
		if err := vs.putNode("node-"+strconv.Itoa(i), []byte(`{"node_id":"x"}`), time.Minute); err != nil {
			b.Fatalf("putNode: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		regs, err := vs.allNodes()
		if err != nil || len(regs) != benchNodes {
			b.Fatalf("allNodes: got %d err %v", len(regs), err)
		}
	}
}

func BenchmarkInflightByNode(b *testing.B) {
	vs, _ := newBenchValkey(b)
	now := time.Now()
	for i := 0; i < benchNodes; i++ {
		// A DIFFERENT instance id than the reader's self, so every node is summed.
		if err := vs.markInflight("peer-inst", "node-"+strconv.Itoa(i), 3, now); err != nil {
			b.Fatalf("markInflight: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, err := vs.inflightByNode("self-inst")
		if err != nil || len(snap) != benchNodes {
			b.Fatalf("inflightByNode: got %d err %v", len(snap), err)
		}
	}
}
