package main

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// capRefusalNotices runs capRefusal once and returns the cap-notice thresholds it decided
// and the response it wrote.
func capRefusalNotices(t *testing.T, spend, pending, amount, cap float64) ([]string, *httptest.ResponseRecorder) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	capNoticeHookForTest = func(_, threshold string) {
		mu.Lock()
		got = append(got, threshold)
		mu.Unlock()
	}
	t.Cleanup(func() { capNoticeHookForTest = nil })
	b := &broker{db: store.NewMem()}
	w := httptest.NewRecorder()
	b.capRefusal(w, "acct", spend, pending, amount, cap, time.Now())
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), got...), w
}

// The refusal judges "fits on spend alone" with the store's own tolerance: a request the
// store would let through on captured spend (float noise under store.CapEpsilon) is refused
// only because of the open holds, so it gets the in-progress refusal and no 100% notice.
func TestCapRefusalUsesTheStoreTolerance(t *testing.T) {
	notices, w := capRefusalNotices(t, 0.9+5e-10, 0.05, 0.1, 1)
	require.Equal(t, "0.05", w.Header().Get("X-RogerAI-Monthly-Pending"), "held requests are the reason")
	require.Empty(t, notices, "no 100%% notice while spend is under the cap")
}

// The once-a-month 100% notice is for spend that has reached the cap. A request too large
// for the room left (spend 85%, nothing in flight) is refused plainly without spending it.
func TestCapRefusalKeepsTheFullNoticeForSpendAtTheCap(t *testing.T) {
	notices, w := capRefusalNotices(t, 0.85, 0, 0.3, 1)
	require.Empty(t, w.Header().Get("X-RogerAI-Monthly-Pending"))
	require.Empty(t, notices, "85%% spend is not the cap")

	notices, _ = capRefusalNotices(t, 1, 0, 0.1, 1)
	require.Equal(t, []string{"100"}, notices)
	notices, _ = capRefusalNotices(t, 1-5e-10, 0, 0.1, 1)
	require.Equal(t, []string{"100"}, notices, "spend within the store tolerance of the cap is at the cap")
}
