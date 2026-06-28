package main

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// benchRouteBroker builds a broker with n healthy, probed, on-air nodes all offering "m" at
// slightly varied prices - a realistic routing population for the pickFor benchmark (P1).
func benchRouteBroker(n int) *broker {
	now := time.Now()
	nodes := map[string]protocol.NodeRegistration{}
	for i := 0; i < n; i++ {
		id := "n" + strconv.Itoa(i)
		nodes[id] = protocol.NodeRegistration{
			NodeID: id,
			Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.4 + float64(i%10)*0.03}},
		}
	}
	b := routeBroker(now, nodes)
	for id := range nodes {
		b.tps[id] = 60 + float64(len(id)%40)
		b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 180}
		b.success[id] = 0.92
	}
	return b
}

// BenchmarkPickFor measures the per-relay routing cost (+ allocs) at 10/100/500 on-air nodes -
// the data to decide whether P1 (the metricsMu-held full pass) is worth restructuring. The
// relay path holds b.mu and pickFor takes metricsMu internally, mirrored here.
func BenchmarkPickFor(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		br := benchRouteBroker(n)
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				br.mu.Lock()
				br.pick("m", false, 0, 0, 0, "", nil, nil, nil)
				br.mu.Unlock()
			}
		})
	}
}

// BenchmarkPickForParallel exposes the lock serialization: every relay takes b.mu + metricsMu
// for the whole candidate pass, so concurrent picks can't overlap. ns/op here vs the sequential
// nodes=100 case quantifies the contention P1 would relieve (measure before optimizing).
func BenchmarkPickForParallel(b *testing.B) {
	br := benchRouteBroker(100)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			br.mu.Lock()
			br.pick("m", false, 0, 0, 0, "", nil, nil, nil)
			br.mu.Unlock()
		}
	})
}
