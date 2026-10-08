package main

// totallatency.go: the time to a whole answer per node (contract §14.B7 #19, founder ruling
// 2026-10-05). sort:latency ranks on it: a bridged row's first token can be a keepalive, so only
// the total says how long the consumer waits. Every served attempt, direct or bridged, folds its
// elapsed time into a per-node EWMA. With a shared store the figure lives there (write-through on
// each sample, merged into memory on the sync loop), so every instance ranks on the same number;
// without one it is this instance's own.
//
// The shared figure is BEST EFFORT and LAST WRITER WINS: each instance blends a sample into its
// own copy and writes the result, so two instances serving one node at once can overwrite each
// other's sample. For a ranking hint that is fine (both figures are recent measurements of the
// same node); nothing money- or trust-bearing reads it. Each node's field carries its write
// time and is dropped once older than totalLatencyTTL, so a node that stopped serving falls out.

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// totalLatencyTTL is how long a node's figure is served after its last sample (and keeps the
// shared hash alive while any node is being measured).
const totalLatencyTTL = 24 * time.Hour

// observeTotalLatency folds one served attempt's time to the whole answer (ms) into the node's
// EWMA and writes it through to the shared store (best effort: the local figure stands).
func (b *broker) observeTotalLatency(node string, ms float64) {
	if ms <= 0 || node == "" {
		return
	}
	b.metricsMu.Lock()
	if b.totalLat == nil {
		b.totalLat = map[string]float64{}
	}
	v := ms
	if cur := b.totalLat[node]; cur > 0 {
		v = 0.3*ms + 0.7*cur
	}
	b.totalLat[node] = v
	b.metricsMu.Unlock()
	if b.shared != nil {
		_ = b.shared.setTotalLatency(node, v)
	}
}

// msSince is the time since t in milliseconds, kept to the microsecond: a sub-millisecond serve
// is a real sample, never a 0 that would skip the measurement.
func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// totalLatencyOf is the node's total latency in ms (0 = unmeasured).
func (b *broker) totalLatencyOf(node string) float64 {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.totalLat[node]
}

// latencyRank is what sort:latency ranks a node on: its total latency, else its TTFT, else 0
// (unmeasured, ranked last).
func (b *broker) latencyRank(node string, ttftMs float64) float64 {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.latencyRankLocked(node, ttftMs)
}

// latencyRankLocked is latencyRank for a caller holding metricsMu.
func (b *broker) latencyRankLocked(node string, ttftMs float64) float64 {
	if v := b.totalLat[node]; v > 0 {
		return v
	}
	return ttftMs
}

// syncTotalLatency merges the shared figures into memory (the shared value wins for every node it
// holds). A store error keeps the last merged view.
func (b *broker) syncTotalLatency() {
	if b.shared == nil {
		return
	}
	shared, err := b.shared.totalLatencies()
	if err != nil {
		return
	}
	b.metricsMu.Lock()
	if b.totalLat == nil {
		b.totalLat = map[string]float64{}
	}
	for node, v := range shared {
		b.totalLat[node] = v
	}
	b.metricsMu.Unlock()
}

func totalLatencyKey() string { return keyPrefix + "totlat" }

func (v *valkeyStore) setTotalLatency(node string, ms float64) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.HSet(ctx, totalLatencyKey(), node, strconv.FormatFloat(ms, 'g', -1, 64)+"@"+strconv.FormatInt(time.Now().UnixMilli(), 10))
	pipe.PExpire(ctx, totalLatencyKey(), totalLatencyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("setTotalLatency", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) totalLatencies() (map[string]float64, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	fields, err := v.rdb.HGetAll(ctx, totalLatencyKey()).Result()
	if err != nil {
		v.noteErr("totalLatencies", err)
		return nil, err
	}
	out := make(map[string]float64, len(fields))
	cutoff := time.Now().Add(-totalLatencyTTL).UnixMilli()
	var stale []string
	for node, s := range fields {
		val, at, _ := strings.Cut(s, "@")
		f, perr := strconv.ParseFloat(val, 64)
		ts, terr := strconv.ParseInt(at, 10, 64)
		if perr != nil || terr != nil || f <= 0 || ts < cutoff {
			stale = append(stale, node) // unreadable or older than the TTL: not served, and swept
			continue
		}
		out[node] = f
	}
	if len(stale) > 0 {
		_ = v.rdb.HDel(ctx, totalLatencyKey(), stale...).Err()
	}
	v.setUp(true)
	return out, nil
}

// The figure is inert on memStore: a broker without a shared backend keeps its own.
func (m *memStore) setTotalLatency(string, float64) error { return errNoSharedStore }
func (m *memStore) totalLatencies() (map[string]float64, error) {
	return nil, errNoSharedStore
}
