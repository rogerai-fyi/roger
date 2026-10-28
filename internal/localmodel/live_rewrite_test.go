package localmodel

// A serve's digest arrives after it is on the air (digestLater, for a hand-placed file). Review
// 2026-10-27: it was written by atomically replacing the serve's record, and a directory listing
// taken during that rename can miss the file, so Live said nothing was serving and a switch could
// start a second serve instead of replacing the first. Whoever lists the serves must see a live one
// while its digest is being recorded, and must then see the digest.

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestALiveServeIsSeenWhileItsRecordIsRewritten(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	s := Serving{Supervisor: os.Getpid(), Port: 41234, Band: "tiny", Started: time.Now().UTC()}
	require.NoError(t, h.writeServing(s))

	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				_ = h.recordDigest(s, "sum-"+string(rune('a'+(i+w)%26)))
			}
		}(w)
	}
	misses := 0
	deadline := time.Now().Add(2 * time.Second)
	for n := 0; time.Now().Before(deadline); n++ {
		if _, ok := h.Live(); !ok {
			misses++
		}
	}
	stop.Store(true)
	wg.Wait()
	require.Zero(t, misses, "a live serve went unseen while its digest was recorded")
	live, ok := h.Live()
	require.True(t, ok)
	require.Contains(t, live.SHA256, "sum-", "the digest is read back with the serve")

	// A digest left by an earlier serve on the same port is not this serve's.
	next := s
	next.Started = s.Started.Add(time.Minute)
	require.NoError(t, h.writeServing(next))
	live, ok = h.Live()
	require.True(t, ok)
	require.Empty(t, live.SHA256, "another serve's digest is never read as this one's")
}
