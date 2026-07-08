package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// toolsweep_race_test.go pins the fix for PR #33 review, minor #2: the toolsVerified stale-sweep
// HDEL'd fields computed from a stale HGETALL snapshot, so a field that was >45min stale at read
// but RE-MARKED fresh (markToolsVerified) sub-second later - after the snapshot, before the HDEL -
// could be dropped for a sync cycle, flickering a verified bit off. The sweep must re-check
// freshness immediately before the delete and spare a field re-marked within the window.
//
// RED against origin/main: sweepStaleToolsFields does not exist; the sweep is a blind HDEL.

func newToolStore(t *testing.T) *valkeyStore {
	t.Helper()
	mr := miniredis.RunT(t)
	v, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func setToolField(t *testing.T, v *valkeyStore, node, model string, ms int64) {
	t.Helper()
	if err := v.rdb.HSet(context.Background(), toolsKey(), node+"\x00"+model, strconv.FormatInt(ms, 10)).Err(); err != nil {
		t.Fatalf("HSet %s/%s: %v", node, model, err)
	}
}

func toolFieldExists(t *testing.T, v *valkeyStore, node, model string) bool {
	t.Helper()
	ok, err := v.rdb.HExists(context.Background(), toolsKey(), node+"\x00"+model).Result()
	if err != nil {
		t.Fatalf("HExists: %v", err)
	}
	return ok
}

// TestSweepStaleToolsSparesReMarkedField is the race guard: a field STALE at the snapshot but
// re-marked FRESH before the sweep must SURVIVE (the sweep re-checks current freshness), while a
// genuinely-still-stale field and an unparseable field are swept.
func TestSweepStaleToolsSparesReMarkedField(t *testing.T) {
	v := newToolStore(t)
	ctx := context.Background()
	now := time.Now()
	ttl := 45 * time.Minute
	cutoff := now.Add(-ttl).UnixMilli()

	// Snapshot-time state: three fields all look STALE / unparseable to the HGETALL caller.
	setToolField(t, v, "n", "reMarked", cutoff-1000) // stale at snapshot...
	setToolField(t, v, "n", "stillStale", cutoff-1000)
	if err := v.rdb.HSet(ctx, toolsKey(), "n\x00garbage", "not-a-number").Err(); err != nil {
		t.Fatal(err)
	}
	candidates := []string{"n\x00reMarked", "n\x00stillStale", "n\x00garbage"}

	// The RACE: between the snapshot and the sweep, an authoritative host re-marks one field.
	setToolField(t, v, "n", "reMarked", now.UnixMilli()) // ...fresh again before the sweep

	if err := v.sweepStaleToolsFields(ctx, cutoff, candidates); err != nil {
		t.Fatalf("sweepStaleToolsFields: %v", err)
	}

	if !toolFieldExists(t, v, "n", "reMarked") {
		t.Fatal("a field re-marked within the freshness window was wrongly swept (the flicker race)")
	}
	if toolFieldExists(t, v, "n", "stillStale") {
		t.Fatal("a genuinely-stale field survived the sweep")
	}
	if toolFieldExists(t, v, "n", "garbage") {
		t.Fatal("an unparseable field survived the sweep")
	}
}

// TestToolsVerifiedSweepStillClearsStale confirms the end-to-end read still ages out a stale
// field (the sweep runs) while returning only fresh fields - the guard did not disable the sweep.
func TestToolsVerifiedSweepStillClearsStale(t *testing.T) {
	v := newToolStore(t)
	now := time.Now()
	ttl := 45 * time.Minute

	setToolField(t, v, "n", "fresh", now.UnixMilli())
	setToolField(t, v, "n", "old", now.Add(-ttl-time.Minute).UnixMilli())

	got, err := v.toolsVerified(ttl)
	if err != nil {
		t.Fatalf("toolsVerified: %v", err)
	}
	if !got[toolKey("n", "fresh")] {
		t.Fatal("fresh field missing from the verified union")
	}
	if got[toolKey("n", "old")] {
		t.Fatal("stale field wrongly present in the verified union")
	}
	if toolFieldExists(t, v, "n", "old") {
		t.Fatal("stale field was not swept from the hash")
	}
}
