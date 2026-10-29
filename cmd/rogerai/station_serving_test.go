package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/detect"
)

// features/join/invite.feature: a station's poll says what it serves now. The list starts from what
// the share knew, never waits on a probe, and follows the upstream once a background probe lands.
func TestStationServingNeverWaitsAndFollowsTheUpstream(t *testing.T) {
	release := make(chan struct{})
	probed := make(chan string, 4)
	prev := detectProbeKey
	detectProbeKey = func(base, key string) (detect.Found, detect.Status) {
		probed <- base
		<-release // a slow upstream: the poll must not wait for this
		return detect.Found{Models: []string{"gpt-oss-20b"}}, detect.Reachable
	}
	t.Cleanup(func() { detectProbeKey = prev })
	// The upstream is one roger serves itself, so its models are offered by default
	// (founder ruling 2026-10-11, stationoffer.go).
	prevServed := rogerServedBases
	rogerServedBases = func() []string { return []string{"http://127.0.0.1:8080/v1"} }
	t.Cleanup(func() { rogerServedBases = prevServed })

	serving := stationServing("http://127.0.0.1:8080/v1", "k", "qwen3-8b", []string{"qwen3-8b", "smollm2"}, StationOffer{})
	require.Equal(t, []string{"qwen3-8b", "smollm2"}, serving(), "the share's own knowledge first, the shared model leading")

	// Make the view stale: the next read starts a probe and still answers at once.
	backdateStationServing(t)
	start := time.Now()
	require.Equal(t, []string{"qwen3-8b", "smollm2"}, serving())
	require.Less(t, time.Since(start), 500*time.Millisecond, "a poll never waits on a probe")
	select {
	case base := <-probed:
		require.Equal(t, "http://127.0.0.1:8080/v1", base, "only this share's own upstream is probed")
	case <-time.After(5 * time.Second):
		t.Fatal("no background probe started")
	}
	require.Equal(t, []string{"qwen3-8b", "smollm2"}, serving(), "one probe at a time; the old view until it lands")
	close(release)
	require.Eventually(t, func() bool {
		got := serving()
		return len(got) == 2 && got[0] == "qwen3-8b" && got[1] == "gpt-oss-20b"
	}, 5*time.Second, 10*time.Millisecond, "the refreshed list keeps the shared model and follows the upstream")

	// No upstream (nothing to probe): the shared model alone, when the station named it.
	require.Equal(t, []string{"m"}, stationServing("", "", "m", nil, StationOffer{Explicit: []string{"m"}})())
	// Not named, not roger-served: nothing.
	require.Empty(t, stationServing("", "", "m", nil, StationOffer{})())
}

// backdateStationServing makes every station view stale from now on, for this test.
func backdateStationServing(t *testing.T) {
	prev := stationServingTTL
	stationServingTTL = 0
	t.Cleanup(func() { stationServingTTL = prev })
}
